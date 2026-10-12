package manager

// MemoryStore's PurgeStore methods (struct and lock in assets_memory.go).

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"time"
)

// byDeletion orders trashed items longest-trashed first, ties by ID: the
// order the cleanup listings use.
func byDeletion(aDeleted, bDeleted *time.Time, aID, bID string) int {
	if c := aDeleted.Compare(*bDeleted); c != 0 {
		return c
	}
	return cmp.Compare(aID, bID)
}

func (m *MemoryStore) ListAssetsTrashedBefore(ctx context.Context, before time.Time, limit int) ([]Asset, error) {
	if err := checkLimit(limit); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Asset
	for _, a := range m.assets {
		if a.DeletedAt != nil && a.DeletedAt.Before(before) {
			out = append(out, cloneAsset(a))
		}
	}
	slices.SortFunc(out, func(a, b Asset) int { return byDeletion(a.DeletedAt, b.DeletedAt, a.ID, b.ID) })
	return out[:min(limit, len(out))], nil
}

func (m *MemoryStore) PurgeAsset(ctx context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.assets[id]
	switch {
	case !ok:
		return nil // already gone
	case a.DeletedAt == nil:
		return fmt.Errorf("%w: asset %s is not in the trash", ErrInvalidAsset, id)
	}
	delete(m.assets, id)
	delete(m.versions, id)
	return nil
}

func (m *MemoryStore) PurgeFolders(ctx context.Context, before time.Time, limit int) (int, error) {
	if err := checkLimit(limit); err != nil {
		return 0, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var due []Folder
	for _, f := range m.folders {
		if f.DeletedAt != nil && f.DeletedAt.Before(before) {
			due = append(due, f)
		}
	}
	slices.SortFunc(due, func(a, b Folder) int { return byDeletion(a.DeletedAt, b.DeletedAt, a.ID, b.ID) })
	due = due[:min(limit, len(due))]

	purged := make(map[string]bool, len(due))
	for _, f := range due {
		purged[f.ID] = true
	}
	// Nothing live may point at a trashed folder; trashed things that do
	// move to the top level. Postgres's foreign keys enforce the same.
	for id, f := range m.folders {
		if purged[f.ParentID] && !purged[id] {
			if f.DeletedAt == nil {
				return 0, fmt.Errorf("live folder %s is inside trashed folder %s", id, f.ParentID)
			}
			f.ParentID = ""
			m.folders[id] = f
		}
	}
	for id, a := range m.assets {
		if purged[a.FolderID] {
			if a.DeletedAt == nil {
				return 0, fmt.Errorf("live asset %s is in trashed folder %s", id, a.FolderID)
			}
			a.FolderID = ""
			m.assets[id] = a
		}
	}
	for id := range purged {
		delete(m.folders, id)
	}
	return len(due), nil
}

func (m *MemoryStore) DeleteCompletedUploads(ctx context.Context, before time.Time, limit int) (int, error) {
	if err := checkLimit(limit); err != nil {
		return 0, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var due []Upload
	for _, u := range m.uploads {
		if u.CompletedAt != nil && u.CompletedAt.Before(before) {
			due = append(due, u)
		}
	}
	slices.SortFunc(due, func(a, b Upload) int { return byDeletion(a.CompletedAt, b.CompletedAt, a.ID, b.ID) })
	due = due[:min(limit, len(due))]
	for _, u := range due {
		delete(m.uploads, u.ID)
	}
	return len(due), nil
}
