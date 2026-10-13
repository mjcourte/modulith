package dam

import (
	"cmp"
	"context"
	"slices"
)

func (m *MemoryStore) RecordUse(ctx context.Context, projectID string, ref AssetRef) error {
	if err := checkUse(projectID, ref); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.recordUse(projectID, ref)
}

// recordUse is RecordUse with m.mu held.
func (m *MemoryStore) recordUse(projectID string, ref AssetRef) error {
	if _, ok := m.uses[projectID][ref]; ok {
		return nil // already recorded, even if the asset has been trashed since
	}
	if _, err := m.liveAsset(ref.AssetID); err != nil {
		return err
	}
	if ref.Version > len(m.versions[ref.AssetID]) {
		return ErrVersionNotFound
	}
	if m.uses[projectID] == nil {
		m.uses[projectID] = make(map[AssetRef]struct{})
	}
	m.uses[projectID][ref] = struct{}{}
	return nil
}

func (m *MemoryStore) ForgetUse(ctx context.Context, projectID string, ref AssetRef) error {
	if err := checkUse(projectID, ref); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.uses[projectID], ref) // delete on a nil map is a no-op
	if len(m.uses[projectID]) == 0 {
		delete(m.uses, projectID)
	}
	return nil
}

func (m *MemoryStore) ListUses(ctx context.Context, projectID string) ([]AssetRef, error) {
	if err := checkProjectID(projectID); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]AssetRef, 0, len(m.uses[projectID]))
	for ref := range m.uses[projectID] {
		out = append(out, ref)
	}
	slices.SortFunc(out, func(a, b AssetRef) int {
		return cmp.Or(cmp.Compare(a.AssetID, b.AssetID), cmp.Compare(a.Version, b.Version))
	})
	return out, nil
}

func (m *MemoryStore) CountUses(ctx context.Context, assetIDs []string) (map[string]int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]int)
	for _, id := range slices.Compact(slices.Sorted(slices.Values(assetIDs))) {
		for _, refs := range m.uses {
			for ref := range refs {
				if ref.AssetID == id {
					out[id]++
					break // count projects, not versions
				}
			}
		}
	}
	return out, nil
}

func (m *MemoryStore) ForgetProjectUses(ctx context.Context, projectID string) (int, error) {
	if err := checkProjectID(projectID); err != nil {
		return 0, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	n := len(m.uses[projectID])
	delete(m.uses, projectID)
	return n, nil
}

// forgetAssetUses drops every use of assetID, as Postgres's ON DELETE
// CASCADE does when its versions go. The caller must hold m.mu.
func (m *MemoryStore) forgetAssetUses(assetID string) {
	for projectID, refs := range m.uses {
		for ref := range refs {
			if ref.AssetID == assetID {
				delete(refs, ref) // deleting during range is safe in Go
			}
		}
		if len(refs) == 0 {
			delete(m.uses, projectID)
		}
	}
}

func (m *MemoryStore) HasUse(ctx context.Context, projectID string, ref AssetRef) (bool, error) {
	if err := checkUse(projectID, ref); err != nil {
		return false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.uses[projectID][ref]
	return ok, nil
}

func (m *MemoryStore) SwapUse(ctx context.Context, projectID string, from, to AssetRef) error {
	if err := checkUse(projectID, from); err != nil {
		return err
	}
	if err := checkUse(projectID, to); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.recordUse(projectID, to); err != nil {
		return err // nothing changed
	}
	if from != to {
		delete(m.uses[projectID], from)
	}
	return nil
}
