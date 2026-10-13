package manager

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"

	"gyoza/internal/engine"
)

// AssetUses keeps storage's record of which asset versions each project's
// document uses (storage's RecordAssetUse/ForgetAssetUse). Both calls are
// idempotent. Declared here; storage implements it.
type AssetUses interface {
	RecordAssetUse(ctx context.Context, actorID, projectID string, ref engine.AssetRef) error
	ForgetAssetUse(ctx context.Context, actorID, projectID string, ref engine.AssetRef) error
}

// projectUses is what storage is known to hold for one project.
type projectUses struct {
	mu       sync.Mutex // one sync at a time per project, so calls can't interleave
	recorded []engine.AssetRef
}

// usesFor returns the project's usage state. It starts empty, so the first
// sync after a restart records every current use again (idempotent): a
// record that failed before the restart heals. A forget that failed then
// stays stale, the milder error (OPTIMIZATIONS.md).
func (m *Manager) usesFor(projectID string) *projectUses {
	m.usesMu.Lock()
	defer m.usesMu.Unlock()
	u, ok := m.projectUses[projectID]
	if !ok {
		u = &projectUses{}
		m.projectUses[projectID] = u
	}
	return u
}

// syncUses brings storage's usage rows in line with the asset versions the
// document uses now: it records the new ones and forgets the dropped ones.
// It compares against what storage is known to hold, not against the last
// edit, so a failed call is retried by the next sync and concurrent edits
// can't apply their calls out of order. It tries every call and returns all
// the errors.
func (m *Manager) syncUses(ctx context.Context, actorID, projectID string, doc engine.Document) error {
	u := m.usesFor(projectID)
	u.mu.Lock()
	defer u.mu.Unlock()
	current, err := doc.Assets(ctx, engine.AssetFilter{})
	if err != nil {
		return err
	}
	var errs []error
	recorded := slices.Clone(u.recorded)
	for _, ref := range current {
		if slices.Contains(recorded, ref) {
			continue
		}
		if err := m.uses.RecordAssetUse(ctx, actorID, projectID, ref); err != nil {
			errs = append(errs, fmt.Errorf("record use of %s@%d: %w", ref.ID, ref.Version, err))
			continue
		}
		recorded = append(recorded, ref)
	}
	recorded = slices.DeleteFunc(recorded, func(ref engine.AssetRef) bool {
		if slices.Contains(current, ref) {
			return false
		}
		if err := m.uses.ForgetAssetUse(ctx, actorID, projectID, ref); err != nil {
			errs = append(errs, fmt.Errorf("forget use of %s@%d: %w", ref.ID, ref.Version, err))
			return false
		}
		return true
	})
	u.recorded = recorded
	return errors.Join(errs...)
}
