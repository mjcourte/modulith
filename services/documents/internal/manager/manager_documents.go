package manager

import (
	"context"
	"errors"
	"fmt"

	"github.com/mjcourte/modulith/internal/engine"
	"github.com/mjcourte/modulith/services/documents/internal/live"
	"github.com/mjcourte/modulith/services/documents/internal/snapshots"
	"github.com/mjcourte/modulith/services/documents/internal/wal"
)

func (m *Manager) GetDocument(ctx context.Context, actorID, projectID string) (Document, error) {
	if err := m.authz.CanRead(ctx, actorID, projectID); err != nil {
		return Document{}, err
	}
	return m.current(ctx, projectID)
}

func (m *Manager) QueryDocument(ctx context.Context, actorID, projectID string, q engine.Query) (*engine.View, error) {
	if err := m.authz.CanRead(ctx, actorID, projectID); err != nil {
		return nil, err
	}
	doc, release, err := m.docs.Acquire(ctx, projectID)
	if err != nil {
		return nil, err
	}
	defer release()
	return doc.Engine().Query(ctx, q)
}

// InitDocument needs write access: it creates state, unlike GetDocument's
// implicit creation, which only a reader triggers on first open.
func (m *Manager) InitDocument(ctx context.Context, actorID, projectID string) (Document, error) {
	if err := m.authz.CanWrite(ctx, actorID, projectID); err != nil {
		return Document{}, err
	}
	_, err := m.store.Latest(ctx, projectID)
	switch {
	case err == nil:
		return Document{}, fmt.Errorf("%w: project %s", ErrExists, projectID)
	case !errors.Is(err, snapshots.ErrNotFound):
		return Document{}, err
	}
	// Loading creates and stores it. Two racing inits both succeed with the
	// same document: the registry loads it once.
	return m.current(ctx, projectID)
}

func (m *Manager) UpdateDocument(ctx context.Context, actorID, projectID string, delta engine.DeltaInput) (engine.Delta, error) {
	if err := m.authz.CanWrite(ctx, actorID, projectID); err != nil {
		return engine.Delta{}, err
	}
	return m.localEdit(ctx, actorID, projectID, func(e engine.Document) (engine.Delta, error) {
		return e.Edit(ctx, delta)
	})
}

// RevertDocument puts the document back to a stored version, as one fresh
// edit (engine.Document.RevertTo): administrative un-sticking, not undo. It
// needs write access, like any edit. A version retention has removed is
// snapshots.ErrNotFound. Reverting to the current state changes nothing and
// returns an empty delta.
func (m *Manager) RevertDocument(ctx context.Context, actorID, projectID string, version int) (engine.Delta, error) {
	if err := m.authz.CanWrite(ctx, actorID, projectID); err != nil {
		return engine.Delta{}, err
	}
	ref, err := m.store.Version(ctx, projectID, version)
	if err != nil {
		return engine.Delta{}, err
	}
	snap, err := m.store.Get(ctx, ref)
	if err != nil {
		return engine.Delta{}, err
	}
	return m.localEdit(ctx, actorID, projectID, func(e engine.Document) (engine.Delta, error) {
		return e.RevertTo(ctx, snap)
	})
}

// localEdit runs an edit made here for actorID, then distributes it: publish
// to the stream and sync asset uses. Once the edit is applied, later failures
// are reported with it (ErrNotPublished, ErrUsesNotRecorded) rather than
// undoing it. An edit that changed nothing (empty Payload) isn't published.
// The caller has checked write access.
func (m *Manager) localEdit(ctx context.Context, actorID, projectID string, edit func(engine.Document) (engine.Delta, error)) (engine.Delta, error) {
	doc, release, err := m.docs.Acquire(ctx, projectID)
	if err != nil {
		return engine.Delta{}, err
	}
	defer release()
	var out engine.Delta
	err = doc.Update(0, actorID, func(e engine.Document) error {
		out, err = edit(e)
		return err
	})
	if err != nil || len(out.Payload) == 0 {
		return out, err
	}
	var errs []error
	if m.cfg.WAL != nil {
		_, err := m.cfg.WAL.PublishDelta(ctx, wal.Delta{ProjectID: projectID, ActorID: actorID, Origin: m.cfg.Replica, Delta: asInput(out)})
		if err != nil {
			errs = append(errs, fmt.Errorf("%w: %w", ErrNotPublished, err))
		}
	}
	// A failed sync is retried by the next edit's.
	if err := m.syncUses(ctx, actorID, projectID, doc.Engine()); err != nil {
		errs = append(errs, fmt.Errorf("%w: %w", ErrUsesNotRecorded, err))
	}
	return out, errors.Join(errs...)
}

// ApplyDelta applies a delta read from the stream: the consumer's handler.
// It loads the project if it isn't open, so every project with deltas is
// snapshotted (and compacted) before the stream's MaxAge could drop them.
// Deltas from the gateway carry their actor, whose asset uses are synced;
// this replica's own echoes were synced when it made them.
func (m *Manager) ApplyDelta(ctx context.Context, d wal.Delta) error {
	doc, release, err := m.docs.Acquire(ctx, d.ProjectID)
	if errors.Is(err, snapshots.ErrNoProject) {
		// A project never created, or deleted: its deltas can never apply.
		return fmt.Errorf("%w: %w", wal.ErrBadDelta, err)
	}
	if err != nil {
		return err
	}
	defer release()
	err = doc.Update(d.Seq, d.ActorID, func(e engine.Document) error {
		_, err := e.Edit(ctx, streamInput(d))
		return err
	})
	if errors.Is(err, live.ErrNotOpen) || errors.Is(err, engine.ErrClosed) {
		return err // evicted meanwhile: redelivery loads it again
	}
	if err != nil {
		return fmt.Errorf("%w: %w", wal.ErrBadDelta, err)
	}
	if d.Origin == m.cfg.Replica || d.ActorID == "" {
		return nil
	}
	return m.syncUses(ctx, d.ActorID, d.ProjectID, doc.Engine())
}

// streamInput is a delta read from the stream, ready to apply. One a
// documents replica published (Origin set) is trusted: that replica stamped
// it or checked its stamp, and its clock may run ahead of this one's
// (engine/PLAN.md row 2). The gateway publishes without Origin, so browsers'
// stamps are still bounded here.
func streamInput(d wal.Delta) engine.DeltaInput {
	in := d.Delta
	in.Trusted = d.Origin != ""
	return in
}

func asInput(d engine.Delta) engine.DeltaInput {
	return engine.DeltaInput{Format: d.Format, Version: d.Version, Payload: d.Payload}
}

func (m *Manager) current(ctx context.Context, projectID string) (Document, error) {
	doc, release, err := m.docs.Acquire(ctx, projectID)
	if err != nil {
		return Document{}, err
	}
	defer release()
	snap, seq, err := doc.Snapshot(ctx)
	if err != nil {
		return Document{}, err
	}
	return Document{Snapshot: snap, Seq: seq}, nil
}
