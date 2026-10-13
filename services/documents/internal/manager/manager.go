package manager

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"gyoza/internal/engine"
	"gyoza/services/documents/internal/live"
	"gyoza/services/documents/internal/snapshots"
	"gyoza/services/documents/internal/wal"
)

// Config holds the Manager's settings.
type Config struct {
	// Replica identifies this documents instance: it stamps the edits made
	// here (engine.Settings.Replica). It must be unique per running instance.
	Replica string
	Live    live.Config
	// WAL is the NATS write-ahead log; nil runs without one (each replica
	// alone, nothing distributed), as unit tests do.
	WAL WAL
}

// WAL is what the Manager needs from the write-ahead log (*wal.Log).
type WAL interface {
	PublishDelta(ctx context.Context, d wal.Delta) (uint64, error)
	PublishSnapshot(ctx context.Context, projectID string, p wal.Pointer) error
	Compact(ctx context.Context, projectID string, coveredSeq uint64) error
	DeltasAfter(ctx context.Context, projectID string, seq uint64, fn func(wal.Delta) error, onBad func(error)) error
}

// Manager implements Service.
type Manager struct {
	eng   engine.Engine
	store snapshots.Store
	authz Authorizer
	uses  AssetUses
	cfg   Config
	docs  *live.Registry

	usesMu      sync.Mutex
	projectUses map[string]*projectUses
}

func New(eng engine.Engine, store snapshots.Store, authz Authorizer, uses AssetUses, cfg Config) *Manager {
	m := &Manager{
		eng: eng, store: store, authz: authz, uses: uses, cfg: cfg,
		projectUses: map[string]*projectUses{},
	}
	m.docs = live.New(m.load, m.save, cfg.Live)
	return m
}

// Close saves every open document with unsaved changes and closes them.
func (m *Manager) Close(ctx context.Context) error {
	return m.docs.Close(ctx)
}

// load is the registry's Loader (README "LoadOrInit"): the latest snapshot,
// or a new empty document stored at once as version 1, then the deltas the
// stream holds after it.
func (m *Manager) load(ctx context.Context, projectID string) (engine.Document, uint64, error) {
	doc, seq, err := m.restore(ctx, projectID)
	if err != nil {
		return nil, 0, err
	}
	if seq, err = m.replay(ctx, projectID, doc, seq); err != nil {
		doc.Close()
		return nil, 0, err
	}
	return doc, seq, nil
}

// restore loads the project's latest snapshot, or creates an empty document
// and stores it as the first snapshot if there is none. It returns the
// stream sequence the document covers.
func (m *Manager) restore(ctx context.Context, projectID string) (engine.Document, uint64, error) {
	settings := engine.Settings{ID: projectID, Replica: m.cfg.Replica}
	ref, err := m.store.Latest(ctx, projectID)
	if errors.Is(err, snapshots.ErrNotFound) {
		doc, err := m.eng.New(ctx, settings)
		if err != nil {
			return nil, 0, err
		}
		snap, err := doc.Snapshot(ctx)
		if err == nil {
			// Another replica may store a first snapshot at the same time:
			// save then merges it, and reports the sequence it covers.
			var seq uint64
			if seq, err = m.save(ctx, projectID, doc, snap, 0, live.Edit{}); err == nil {
				return doc, seq, nil
			}
			err = fmt.Errorf("store first snapshot: %w", err)
		}
		doc.Close()
		return nil, 0, err
	}
	if err != nil {
		return nil, 0, err
	}
	snap, err := m.store.Get(ctx, ref)
	if err != nil {
		return nil, 0, err
	}
	doc, err := m.eng.Load(ctx, settings, snap)
	if err != nil {
		return nil, 0, fmt.Errorf("load snapshot v%d: %w", ref.Version, err)
	}
	return doc, ref.Seq, nil
}

// replay applies the deltas the stream holds after seq (README "check NATS
// for outstanding messages") and returns the last sequence applied. Deltas
// the snapshot already holds are harmless: applying is idempotent. A delta
// that can't be read or applied is skipped, as the consumer drops it too: one
// bad message must not make the project impossible to load.
func (m *Manager) replay(ctx context.Context, projectID string, doc engine.Document, seq uint64) (uint64, error) {
	if m.cfg.WAL == nil {
		return seq, nil
	}
	err := m.cfg.WAL.DeltasAfter(ctx, projectID, seq, func(d wal.Delta) error {
		if _, err := doc.Edit(ctx, d.Delta); err != nil {
			slog.Error("documents: skipping a delta that can't be applied", "project", projectID, "seq", d.Seq, "err", err)
		}
		seq = d.Seq
		return nil
	}, func(err error) {
		slog.Error("documents: skipping a delta that can't be read", "project", projectID, "err", err)
	})
	if err != nil {
		return 0, fmt.Errorf("replay deltas of %s: %w", projectID, err)
	}
	return seq, nil
}

// staleRetries bounds save's merge-and-retry loop. Each round merges the
// newest stored snapshot, so a second refusal means another replica stored
// again in between; more than a few means something is wrong.
const staleRetries = 3

// save is the registry's Saver: store the snapshot, then announce it and
// drop what it covers from the stream, in that order and only once the store
// is durable, so deltas are purged only when S3 holds a snapshot covering
// them. It returns the sequence stored.
//
// If a snapshot covering a later sequence is already stored (another
// replica saved; the store refuses to go back), the newer one is merged into
// doc, which loses nothing (CRDT merge), and the merge is stored at the
// newer sequence. The open document then holds the newer state too, whose
// covered deltas may already be compacted away.
//
// A failure is retried by the registry; a retry after a failed announcement
// stores the snapshot again (one extra version, harmless).
func (m *Manager) save(ctx context.Context, projectID string, doc engine.Document, snap engine.Snapshot, seq uint64, last live.Edit) (uint64, error) {
	edit := snapshots.Edit{By: last.By, At: last.At}
	ref, err := m.store.Put(ctx, projectID, snap, seq, edit)
	for i := 0; errors.Is(err, snapshots.ErrStale) && i < staleRetries; i++ {
		if snap, seq, err = m.catchUp(ctx, projectID, doc, seq); err != nil {
			return 0, err
		}
		ref, err = m.store.Put(ctx, projectID, snap, seq, edit)
	}
	if err != nil {
		return 0, err
	}
	if m.cfg.WAL != nil {
		ptr := wal.Pointer{Version: ref.Version, Seq: ref.Seq, ObjectID: ref.ObjectID, EditedBy: ref.EditedBy, EditedAt: ref.EditedAt}
		if err := m.cfg.WAL.PublishSnapshot(ctx, projectID, ptr); err != nil {
			return 0, err
		}
		// Only a durable snapshot may replace the deltas: from a store in RAM
		// a crash would lose both.
		if m.store.Durable() {
			if err := m.cfg.WAL.Compact(ctx, projectID, ref.Seq); err != nil {
				return 0, err
			}
		}
	}
	return ref.Seq, nil
}

// catchUp merges the project's latest stored snapshot into doc and returns
// doc's merged state with the sequence it now covers.
func (m *Manager) catchUp(ctx context.Context, projectID string, doc engine.Document, seq uint64) (engine.Snapshot, uint64, error) {
	latest, err := m.store.Latest(ctx, projectID)
	if err != nil {
		return engine.Snapshot{}, 0, err
	}
	theirs, err := m.store.Get(ctx, latest)
	if err != nil {
		return engine.Snapshot{}, 0, err
	}
	if err := doc.Merge(ctx, theirs); err != nil {
		return engine.Snapshot{}, 0, fmt.Errorf("merge newer snapshot v%d: %w", latest.Version, err)
	}
	snap, err := doc.Snapshot(ctx)
	if err != nil {
		return engine.Snapshot{}, 0, err
	}
	return snap, max(seq, latest.Seq), nil
}
