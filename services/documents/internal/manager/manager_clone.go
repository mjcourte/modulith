package manager

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mjcourte/modulith/internal/engine"
	"github.com/mjcourte/modulith/services/documents/internal/snapshots"
	"github.com/mjcourte/modulith/services/storage"
)

// CloneDocument copies from's document into the clone project to, with every
// asset ref pointing at the clone's copy of it: the copy storage's
// CopyProjectFiles made, owned by the clone project, so its ref is
// {storage.CopyAssetID(to, {project, to}, ref), 1}. Service to service, from iam's clone job: the
// actor must be able to read from; to is being cloned, which no actor check
// passes, so iam is trusted for it.
//
// Idempotent, as the clone job can resume: if to already has a document it
// does nothing. It records no asset uses: CopyProjectFiles already did.
func (m *Manager) CloneDocument(ctx context.Context, actorID, from, to string) error {
	if err := m.authz.CanRead(ctx, actorID, from); err != nil {
		return err
	}
	switch _, err := m.store.Latest(ctx, to); {
	case err == nil:
		return nil
	case !errors.Is(err, snapshots.ErrNotFound):
		return err
	}
	// The current state, deltas not yet snapshotted included.
	src, err := m.current(ctx, from)
	if err != nil {
		return err
	}
	doc, err := m.eng.New(ctx, engine.Settings{ID: to, Replica: m.cfg.Replica})
	if err != nil {
		return err
	}
	defer doc.Close()
	if err := doc.Merge(ctx, src.Snapshot); err != nil {
		return fmt.Errorf("copy %s into %s: %w", from, to, err)
	}
	used, err := doc.Assets(ctx, engine.AssetFilter{})
	if err != nil {
		return err
	}
	if len(used) > 0 {
		clone := storage.Owner{Kind: storage.OwnerProject, ID: to}
		copies := make(map[engine.AssetRef]engine.AssetRef, len(used))
		for _, ref := range used {
			id := storage.CopyAssetID(to, clone, storage.AssetRef{AssetID: ref.ID, Version: ref.Version})
			copies[ref] = engine.AssetRef{ID: id, Version: 1}
		}
		if _, err := doc.ReplaceAssetRefs(ctx, copies); err != nil {
			return fmt.Errorf("point %s at its copies: %w", to, err)
		}
	}
	snap, err := doc.Snapshot(ctx)
	if err != nil {
		return err
	}
	// The clone's stream is empty, so it covers sequence 0. Two racing runs
	// may both store it: two equal versions, harmless.
	_, err = m.store.Put(ctx, to, snap, 0, snapshots.Edit{By: actorID, At: time.Now()})
	return err
}

// ReplaceAssetRef points every use of from in the document at to, as one
// edit returned like UpdateDocument's: after storage's ForkAsset, which moves
// the project's use of from to the fork. Needs write access.
func (m *Manager) ReplaceAssetRef(ctx context.Context, actorID, projectID string, from, to engine.AssetRef) (engine.Delta, error) {
	if err := m.authz.CanWrite(ctx, actorID, projectID); err != nil {
		return engine.Delta{}, err
	}
	return m.localEdit(ctx, actorID, projectID, func(e engine.Document) (engine.Delta, error) {
		return e.ReplaceAssetRefs(ctx, map[engine.AssetRef]engine.AssetRef{from: to})
	})
}
