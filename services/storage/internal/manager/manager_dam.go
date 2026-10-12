package manager

// The DAM operations a file browser needs: assets, versions, folders,
// folder contents and the trash. Each checks permission, then delegates to
// the Store, which enforces the data rules.

import (
	"context"
	"fmt"

	"gyoza/services/storage/internal/ledger"
)

// scopeOf returns the project to authorize against: the given one, or the
// owner itself when the owner is a project. So a user a project was shared
// with can reach the project's own assets and folders.
func scopeOf(owner ledger.Owner, projectID string) string {
	if projectID == "" && owner.Kind == ledger.OwnerProject {
		return owner.ID
	}
	return projectID
}

// canRead and canWrite are the only way Manager methods ask the Authorizer,
// so the project rule in scopeOf can't be forgotten.
func (m *Manager) canRead(ctx context.Context, actorID string, owner ledger.Owner, projectID string) error {
	return m.authz.CanRead(ctx, actorID, owner, scopeOf(owner, projectID))
}

func (m *Manager) canWrite(ctx context.Context, actorID string, owner ledger.Owner, projectID string) error {
	return m.authz.CanWrite(ctx, actorID, owner, scopeOf(owner, projectID))
}

// asset loads an asset and checks the actor may read it (write=false) or
// change it (write=true).
func (m *Manager) asset(ctx context.Context, actorID, id string, write bool) (Asset, error) {
	a, err := m.store.GetAsset(ctx, id)
	if err != nil {
		return Asset{}, err
	}
	check := m.canRead
	if write {
		check = m.canWrite
	}
	if err := check(ctx, actorID, a.Owner, a.ProjectID); err != nil {
		return Asset{}, err
	}
	return a, nil
}

// folder is asset's counterpart for folders.
func (m *Manager) folder(ctx context.Context, actorID, id string, write bool) (Folder, error) {
	f, err := m.store.GetFolder(ctx, id)
	if err != nil {
		return Folder{}, err
	}
	check := m.canRead
	if write {
		check = m.canWrite
	}
	if err := check(ctx, actorID, f.Owner, ""); err != nil {
		return Folder{}, err
	}
	return f, nil
}

// ---- Assets

// GetAsset returns an asset the actor may read, trashed ones included (for
// the trash view).
func (m *Manager) GetAsset(ctx context.Context, actorID, id string) (Asset, error) {
	return m.asset(ctx, actorID, id, false)
}

// ListAssets lists assets in a project or an owner's library, with the
// query's filters. The actor needs read access to that scope.
func (m *Manager) ListAssets(ctx context.Context, actorID string, q AssetQuery) (AssetPage, error) {
	if (q.ProjectID == "") == q.Owner.IsZero() {
		return AssetPage{}, fmt.Errorf("%w: set exactly one of ProjectID or Owner", ErrInvalidQuery)
	}
	if err := m.canRead(ctx, actorID, q.Owner, q.ProjectID); err != nil {
		return AssetPage{}, err
	}
	return m.store.ListAssets(ctx, q)
}

// UpdateAsset renames, re-describes, re-tags or moves an asset.
func (m *Manager) UpdateAsset(ctx context.Context, actorID, id string, u AssetUpdate) (Asset, error) {
	if _, err := m.asset(ctx, actorID, id, true); err != nil {
		return Asset{}, err
	}
	return m.store.UpdateAsset(ctx, id, u)
}

// DeleteAsset moves an asset to the trash. Its files stay until the purge job.
func (m *Manager) DeleteAsset(ctx context.Context, actorID, id string) error {
	if _, err := m.asset(ctx, actorID, id, true); err != nil {
		return err
	}
	return m.store.MarkAssetDeleted(ctx, id)
}

// RestoreAsset takes an asset out of the trash (to the top level if its
// folder is gone).
func (m *Manager) RestoreAsset(ctx context.Context, actorID, id string) (Asset, error) {
	a, err := m.asset(ctx, actorID, id, true)
	if err != nil {
		return Asset{}, err
	}
	if !m.restorable(a.DeletedAt) {
		return Asset{}, fmt.Errorf("%w: its time in the trash is over", ErrAssetNotFound)
	}
	return m.store.RestoreAsset(ctx, id)
}

// ListVersions returns an asset's version history, oldest first.
func (m *Manager) ListVersions(ctx context.Context, actorID, id string) ([]Version, error) {
	if _, err := m.asset(ctx, actorID, id, false); err != nil {
		return nil, err
	}
	return m.store.ListVersions(ctx, id)
}

// RevertAsset makes an older version current again.
func (m *Manager) RevertAsset(ctx context.Context, actorID, id string, version int) (Asset, error) {
	if _, err := m.asset(ctx, actorID, id, true); err != nil {
		return Asset{}, err
	}
	return m.store.SetCurrentVersion(ctx, id, version)
}

// ---- Folders

// CreateFolder creates a folder in f.Owner's tree, recording the actor as
// its creator.
func (m *Manager) CreateFolder(ctx context.Context, actorID string, f Folder) (Folder, error) {
	if err := m.canWrite(ctx, actorID, f.Owner, ""); err != nil {
		return Folder{}, err
	}
	f.CreatedBy = actorID
	return m.store.CreateFolder(ctx, f)
}

// RenameFolder renames a folder.
func (m *Manager) RenameFolder(ctx context.Context, actorID, id, name string) (Folder, error) {
	if _, err := m.folder(ctx, actorID, id, true); err != nil {
		return Folder{}, err
	}
	return m.store.RenameFolder(ctx, id, name)
}

// MoveFolder moves a folder (and everything in it) under newParentID, or to
// the top level with "".
func (m *Manager) MoveFolder(ctx context.Context, actorID, id, newParentID string) (Folder, error) {
	if _, err := m.folder(ctx, actorID, id, true); err != nil {
		return Folder{}, err
	}
	return m.store.MoveFolder(ctx, id, newParentID)
}

// DeleteFolder trashes an empty folder. If it isn't empty, the error is a
// *FolderNotEmptyError with counts, so the UI can offer DeleteFolderTree.
func (m *Manager) DeleteFolder(ctx context.Context, actorID, id string) (FolderDeletion, error) {
	if _, err := m.folder(ctx, actorID, id, true); err != nil {
		return FolderDeletion{}, err
	}
	return m.store.DeleteFolder(ctx, id)
}

// DeleteFolderTree is rm -rf: the folder, its sub-folders and their assets
// go to the trash together, and can be restored together with RestoreFolder.
func (m *Manager) DeleteFolderTree(ctx context.Context, actorID, id string) (FolderDeletion, error) {
	if _, err := m.folder(ctx, actorID, id, true); err != nil {
		return FolderDeletion{}, err
	}
	return m.store.DeleteFolderTree(ctx, id)
}

// RestoreFolder takes a trashed folder out of the trash together with
// everything deleted with it (the whole rm -rf, if that's how it went).
// Restoring a live folder changes nothing.
func (m *Manager) RestoreFolder(ctx context.Context, actorID, id string) (FolderDeletion, error) {
	f, err := m.folder(ctx, actorID, id, true)
	if err != nil {
		return FolderDeletion{}, err
	}
	if f.DeletedAt == nil {
		return FolderDeletion{}, nil
	}
	// Everything under one DeletionID was trashed at the same moment, so the
	// folder's deletion time speaks for the assets restored with it.
	if !m.restorable(f.DeletedAt) {
		return FolderDeletion{}, fmt.Errorf("%w: its time in the trash is over", ErrFolderNotFound)
	}
	return m.store.RestoreDeletion(ctx, f.DeletionID)
}

// FolderContents is one screen of a file browser: where you are, the
// sub-folders, and a page of assets.
type FolderContents struct {
	Path    []Folder  // breadcrumbs, top level first; empty at the top level
	Folders []Folder  // sub-folders, by name; only on the first page
	Assets  AssetPage // the folder's assets, oldest first, paged
}

// ListFolderContents returns what's directly inside folderID ("" for the top
// level) of owner's library. Sub-folders come with the first page only, so
// paging through assets doesn't repeat them.
func (m *Manager) ListFolderContents(ctx context.Context, actorID string, owner ledger.Owner, folderID, pageToken string) (FolderContents, error) {
	if err := m.canRead(ctx, actorID, owner, ""); err != nil {
		return FolderContents{}, err
	}
	var contents FolderContents
	if folderID != "" {
		f, err := m.store.GetFolder(ctx, folderID)
		if err != nil {
			return FolderContents{}, err
		}
		if f.Owner != owner || f.DeletedAt != nil {
			return FolderContents{}, ErrFolderNotFound
		}
		if contents.Path, err = m.store.FolderPath(ctx, folderID); err != nil {
			return FolderContents{}, err
		}
	}
	if pageToken == "" {
		var err error
		if contents.Folders, err = m.store.ListFolders(ctx, owner, folderID); err != nil {
			return FolderContents{}, err
		}
	}
	var err error
	contents.Assets, err = m.store.ListAssets(ctx, AssetQuery{Owner: owner, Folder: &folderID, PageToken: pageToken})
	return contents, err
}

// FolderPath returns a folder's breadcrumbs, top level first.
func (m *Manager) FolderPath(ctx context.Context, actorID, id string) ([]Folder, error) {
	if _, err := m.folder(ctx, actorID, id, false); err != nil {
		return nil, err
	}
	return m.store.FolderPath(ctx, id)
}

// ---- Trash

// Trash is an owner's trash: trashed folders (most recent first) and a page
// of trashed assets.
type Trash struct {
	Folders []Folder // only on the first page
	Assets  AssetPage
}

// ListTrash returns what owner has in the trash.
func (m *Manager) ListTrash(ctx context.Context, actorID string, owner ledger.Owner, pageToken string) (Trash, error) {
	if err := m.canRead(ctx, actorID, owner, ""); err != nil {
		return Trash{}, err
	}
	var trash Trash
	if pageToken == "" {
		var err error
		if trash.Folders, err = m.store.ListTrashedFolders(ctx, owner); err != nil {
			return Trash{}, err
		}
	}
	var err error
	trash.Assets, err = m.store.ListAssets(ctx, AssetQuery{Owner: owner, OnlyDeleted: true, PageToken: pageToken})
	return trash, err
}
