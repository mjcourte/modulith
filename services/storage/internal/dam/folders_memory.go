package dam

// MemoryStore's FolderStore methods. They share the struct, mutex and maps
// declared in assets_memory.go; Go lets a type's methods live in any file of
// its package.

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"gyoza/services/storage/internal/ledger"
)

// The helpers below read m.folders and m.assets, so the caller must hold m.mu.
// "Live" means not in the trash.

// liveFolder returns folder id if it exists and isn't trashed.
func (m *MemoryStore) liveFolder(id string) (Folder, error) {
	f, ok := m.folders[id]
	if !ok || f.DeletedAt != nil {
		return Folder{}, ErrFolderNotFound
	}
	return f, nil
}

// ownedFolder returns folder id if it is live and belongs to owner. A folder
// of another owner is reported as not found, not as forbidden: callers learn
// nothing about other owners' trees.
func (m *MemoryStore) ownedFolder(id string, owner ledger.Owner) (Folder, error) {
	f, err := m.liveFolder(id)
	if err != nil || f.Owner != owner {
		return Folder{}, ErrFolderNotFound
	}
	return f, nil
}

// checkAssetFolder accepts "" (top level) or a live folder owned by owner.
func (m *MemoryStore) checkAssetFolder(folderID string, owner ledger.Owner) error {
	if folderID == "" {
		return nil
	}
	_, err := m.ownedFolder(folderID, owner)
	return err
}

// liveChildren returns the IDs of folder id's live sub-folders ("" for top level).
func (m *MemoryStore) liveChildren(owner ledger.Owner, id string) []string {
	var ids []string
	for _, f := range m.folders {
		if f.ParentID == id && f.Owner == owner && f.DeletedAt == nil {
			ids = append(ids, f.ID)
		}
	}
	return ids
}

// liveSubtree returns folder id and all its live descendants, parents before
// children (breadth-first).
func (m *MemoryStore) liveSubtree(f Folder) []string {
	ids := []string{f.ID}
	for i := 0; i < len(ids); i++ { // ids grows as children are found
		ids = append(ids, m.liveChildren(f.Owner, ids[i])...)
	}
	return ids
}

// depthOf returns how deep folder id sits: 0 for the top level (""), 1 for a
// top-level folder, and so on. It walks up parent links; MaxFolderDepth keeps
// that walk short.
func (m *MemoryStore) depthOf(id string) int {
	depth := 0
	for id != "" {
		depth++
		id = m.folders[id].ParentID
	}
	return depth
}

// height returns how many levels a live folder and its live sub-folders span:
// 1 for a folder with no sub-folders. Moving the folder moves all of them.
func (m *MemoryStore) height(f Folder) int {
	tallest := 0
	for _, child := range m.liveChildren(f.Owner, f.ID) {
		tallest = max(tallest, m.height(m.folders[child]))
	}
	return 1 + tallest
}

// isWithin reports whether folder id is ancestor or one of ancestor's
// descendants, by walking up from id.
func (m *MemoryStore) isWithin(id, ancestor string) bool {
	for id != "" {
		if id == ancestor {
			return true
		}
		id = m.folders[id].ParentID
	}
	return false
}

// nameTaken reports whether another live folder (not except) under parentID
// in owner's tree has the same name, ignoring case. Trashed folders never
// clash, so a folder name can be reused right after deleting.
func (m *MemoryStore) nameTaken(owner ledger.Owner, parentID, name, except string) bool {
	key := folderNameKey(name)
	for _, f := range m.folders {
		if f.ID != except && f.Owner == owner && f.ParentID == parentID &&
			f.DeletedAt == nil && folderNameKey(f.Name) == key {
			return true
		}
	}
	return false
}

func (m *MemoryStore) CreateFolder(ctx context.Context, f Folder) (Folder, error) {
	f, err := f.normalize()
	if err != nil {
		return Folder{}, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if existing, ok := m.folders[f.ID]; ok && f.ID != "" {
		return existing, nil // a chosen ID already stored: a retried copy
	}
	if f.ParentID != "" {
		if _, err := m.ownedFolder(f.ParentID, f.Owner); err != nil {
			return Folder{}, err
		}
	}
	if err := checkFolderDepth(m.depthOf(f.ParentID) + 1); err != nil {
		return Folder{}, err
	}
	if m.nameTaken(f.Owner, f.ParentID, f.Name, "") {
		return Folder{}, fmt.Errorf("%w: %q", ErrNameTaken, f.Name)
	}
	t := now()
	if f.ID == "" {
		f.ID = NewID()
	}
	f.CreatedAt, f.UpdatedAt = t, t
	f.DeletedAt, f.DeletionID = nil, ""
	m.folders[f.ID] = f
	// Folder's only pointer, DeletedAt, is nil here and only ever replaced
	// (never written through), so a plain copy is safe to hand out.
	return f, nil
}

func (m *MemoryStore) GetFolder(ctx context.Context, id string) (Folder, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	f, ok := m.folders[id] // trashed folders are returned too, like GetAsset
	if !ok {
		return Folder{}, ErrFolderNotFound
	}
	return f, nil
}

func (m *MemoryStore) ListFolders(ctx context.Context, owner ledger.Owner, parentID string) ([]Folder, error) {
	if err := owner.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidFolder, err)
	}
	if err := checkOptionalID(parentID, "parent folder", ErrInvalidFolder); err != nil {
		return nil, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if parentID != "" {
		if _, err := m.ownedFolder(parentID, owner); err != nil {
			return nil, err
		}
	}
	var children []Folder
	for _, id := range m.liveChildren(owner, parentID) {
		children = append(children, m.folders[id])
	}
	// By name ignoring case; live sibling names are unique ignoring case, so there are no ties.
	slices.SortFunc(children, func(a, b Folder) int {
		return strings.Compare(folderNameKey(a.Name), folderNameKey(b.Name))
	})
	return children, nil
}

func (m *MemoryStore) RenameFolder(ctx context.Context, id, name string) (Folder, error) {
	name, err := normalizeFolderName(name)
	if err != nil {
		return Folder{}, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	f, err := m.liveFolder(id)
	if err != nil {
		return Folder{}, err
	}
	// Excluding the folder itself allows changing only the case: "logos" -> "Logos".
	if m.nameTaken(f.Owner, f.ParentID, name, id) {
		return Folder{}, fmt.Errorf("%w: %q", ErrNameTaken, name)
	}
	f.Name = name
	f.UpdatedAt = now()
	m.folders[id] = f
	return f, nil
}

func (m *MemoryStore) MoveFolder(ctx context.Context, id, newParentID string) (Folder, error) {
	if err := checkOptionalID(newParentID, "parent folder", ErrInvalidFolder); err != nil {
		return Folder{}, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	f, err := m.liveFolder(id)
	if err != nil {
		return Folder{}, err
	}
	if newParentID == f.ParentID {
		return f, nil // already there: nothing to do
	}
	if newParentID != "" {
		if _, err := m.ownedFolder(newParentID, f.Owner); err != nil {
			return Folder{}, err
		}
		// Moving a folder under itself or its own descendant would detach it
		// from the tree into a loop.
		if m.isWithin(newParentID, id) {
			return Folder{}, fmt.Errorf("%w: can't move a folder into itself or its own sub-folder", ErrInvalidFolder)
		}
	}
	// The folder's whole subtree moves with it, so its deepest sub-folder must still fit.
	if err := checkFolderDepth(m.depthOf(newParentID) + m.height(f)); err != nil {
		return Folder{}, err
	}
	if m.nameTaken(f.Owner, newParentID, f.Name, id) {
		return Folder{}, fmt.Errorf("%w: %q", ErrNameTaken, f.Name)
	}
	f.ParentID = newParentID
	f.UpdatedAt = now()
	m.folders[id] = f
	return f, nil
}

func (m *MemoryStore) DeleteFolder(ctx context.Context, id string) (FolderDeletion, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	f, ok := m.folders[id]
	if !ok {
		return FolderDeletion{}, ErrFolderNotFound
	}
	if f.DeletedAt != nil {
		return FolderDeletion{}, nil // already in the trash
	}
	tree := m.liveSubtree(f)
	if folders, assets := len(tree)-1, m.countLiveAssetsIn(tree); folders > 0 || assets > 0 {
		return FolderDeletion{}, &FolderNotEmptyError{Folders: folders, Assets: assets}
	}
	return m.trash(tree), nil
}

func (m *MemoryStore) DeleteFolderTree(ctx context.Context, id string) (FolderDeletion, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	f, ok := m.folders[id]
	if !ok {
		return FolderDeletion{}, ErrFolderNotFound
	}
	if f.DeletedAt != nil {
		return FolderDeletion{}, nil // already in the trash
	}
	// Holding the lock for the whole operation makes it all-or-nothing: no
	// other call can see or change a half-trashed tree.
	return m.trash(m.liveSubtree(f)), nil
}

func (m *MemoryStore) FolderPath(ctx context.Context, id string) ([]Folder, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	f, ok := m.folders[id]
	if !ok {
		return nil, ErrFolderNotFound
	}
	path := []Folder{f}
	for f.ParentID != "" && len(path) <= MaxFolderDepth { // the bound guards against a corrupted (cyclic) tree
		f = m.folders[f.ParentID]
		path = append(path, f)
	}
	slices.Reverse(path) // top level first
	return path, nil
}

func (m *MemoryStore) ListTrashedFolders(ctx context.Context, owner ledger.Owner) ([]Folder, error) {
	if err := owner.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidFolder, err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var trashed []Folder
	for _, f := range m.folders {
		if f.Owner == owner && f.DeletedAt != nil {
			trashed = append(trashed, f)
		}
	}
	slices.SortFunc(trashed, func(a, b Folder) int {
		if c := b.DeletedAt.Compare(*a.DeletedAt); c != 0 {
			return c // most recently deleted first
		}
		return strings.Compare(a.ID, b.ID)
	})
	return trashed, nil
}

func (m *MemoryStore) RestoreDeletion(ctx context.Context, deletionID string) (FolderDeletion, error) {
	if err := checkOptionalID(deletionID, "deletion", ErrInvalidFolder); err != nil || deletionID == "" {
		return FolderDeletion{}, fmt.Errorf("%w: bad deletion ID %q", ErrInvalidFolder, deletionID)
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	// Folders in the batch, parents before children (by depth), so each
	// parent is live again before its children check it.
	var folders []Folder
	for _, f := range m.folders {
		if f.DeletionID == deletionID && f.DeletedAt != nil {
			folders = append(folders, f)
		}
	}
	slices.SortFunc(folders, func(a, b Folder) int { return m.depthOf(a.ID) - m.depthOf(b.ID) })

	result := FolderDeletion{DeletionID: deletionID}
	t := now()
	for _, f := range folders {
		if f.ParentID != "" {
			if _, err := m.ownedFolder(f.ParentID, f.Owner); err != nil {
				f.ParentID = "" // parent not live (deleted separately): restore to the top level
			}
		}
		f.Name = uniqueFolderName(f.Name, func(name string) bool {
			return m.nameTaken(f.Owner, f.ParentID, name, f.ID)
		})
		f.DeletedAt, f.DeletionID, f.UpdatedAt = nil, "", t
		m.folders[f.ID] = f
		result.Folders++
	}
	for id, a := range m.assets {
		if a.DeletionID == deletionID && a.DeletedAt != nil {
			m.restoreAssetLocked(&a)
			m.assets[id] = a
			result.Assets++
		}
	}
	return result, nil
}

// countLiveAssetsIn counts live assets in any of the given folders.
func (m *MemoryStore) countLiveAssetsIn(folderIDs []string) int {
	n := 0
	for _, a := range m.assets {
		if a.DeletedAt == nil && slices.Contains(folderIDs, a.FolderID) {
			n++
		}
	}
	return n
}

// trash moves the given live folders, and every live asset in them, to the
// trash under one new DeletionID.
func (m *MemoryStore) trash(folderIDs []string) FolderDeletion {
	t, deletion := now(), NewID()
	result := FolderDeletion{DeletionID: deletion}
	for _, id := range folderIDs {
		f := m.folders[id]
		f.DeletedAt, f.DeletionID = &t, deletion
		m.folders[id] = f
		result.Folders++
	}
	for id, a := range m.assets {
		if a.DeletedAt == nil && slices.Contains(folderIDs, a.FolderID) {
			a.DeletedAt, a.DeletionID = &t, deletion
			m.assets[id] = a
			result.Assets++
		}
	}
	return result
}
