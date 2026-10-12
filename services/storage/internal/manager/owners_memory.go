package manager

// MemoryStore's OwnerStore methods (struct and lock in assets_memory.go).

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"gyoza/services/storage/internal/ledger"
)

func (m *MemoryStore) TransferOwner(ctx context.Context, from, to ledger.Owner) (OwnerTransfer, error) {
	if err := checkTransfer(from, to); err != nil {
		return OwnerTransfer{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	var result OwnerTransfer
	// Folders in name order, so which one becomes "(2)" is predictable.
	var folders []Folder
	for _, f := range m.folders {
		if f.Owner == from {
			folders = append(folders, f)
		}
	}
	slices.SortFunc(folders, func(a, b Folder) int { return strings.Compare(a.Name, b.Name) })
	for _, f := range folders {
		// Only live top-level folders can clash: deeper ones keep their parent,
		// and trashed ones don't take part in the name rule.
		if f.ParentID == "" && f.DeletedAt == nil {
			name := uniqueFolderName(f.Name, func(n string) bool { return m.nameTaken(to, "", n, f.ID) })
			if name != f.Name {
				f.Name = name
				result.Renamed++
			}
		}
		f.Owner = to
		m.folders[f.ID] = f
		result.Folders++
	}
	for id, a := range m.assets {
		if a.Owner == from {
			a.Owner = to
			m.assets[id] = a
			result.Assets++
		}
	}
	return result, nil
}

func (m *MemoryStore) MarkDeletedByOwner(ctx context.Context, owner ledger.Owner) (FolderDeletion, error) {
	if err := owner.Validate(); err != nil {
		return FolderDeletion{}, fmt.Errorf("%w: %w", ErrInvalidQuery, err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	t, deletion := now(), newID()
	result := FolderDeletion{DeletionID: deletion}
	for id, f := range m.folders {
		if f.Owner == owner && f.DeletedAt == nil {
			f.DeletedAt, f.DeletionID = &t, deletion
			m.folders[id] = f
			result.Folders++
		}
	}
	for id, a := range m.assets {
		if a.Owner == owner && a.DeletedAt == nil {
			a.DeletedAt, a.DeletionID = &t, deletion
			m.assets[id] = a
			result.Assets++
		}
	}
	return result, nil
}

func (m *MemoryStore) ClearCreatedBy(ctx context.Context, userID string) (int, error) {
	if userID == "" {
		return 0, fmt.Errorf("%w: empty userID", ErrInvalidQuery)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for id, a := range m.assets {
		if a.CreatedBy == userID {
			a.CreatedBy = ""
			m.assets[id] = a
			n++
		}
	}
	for id, f := range m.folders {
		if f.CreatedBy == userID {
			f.CreatedBy = ""
			m.folders[id] = f
			n++
		}
	}
	for _, versions := range m.versions {
		for i := range versions { // index, so the change lands in the stored slice
			if versions[i].CreatedBy == userID {
				versions[i].CreatedBy = ""
				n++
			}
		}
	}
	return n, nil
}

func (m *MemoryStore) ExpireUploadsBy(ctx context.Context, userID string) (int, error) {
	if userID == "" {
		return 0, fmt.Errorf("%w: empty userID", ErrInvalidQuery)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	t := now()
	n := 0
	for id, u := range m.uploads {
		if u.CreatedBy == userID && u.CompletedAt == nil && u.ExpiresAt.After(t) {
			u.ExpiresAt = t
			m.uploads[id] = u
			n++
		}
	}
	return n, nil
}
