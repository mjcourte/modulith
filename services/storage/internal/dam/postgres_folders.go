package dam

// Postgres: folders, breadcrumbs, trash and restore.

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/mjcourte/modulith/services/storage/internal/db"
	"github.com/mjcourte/modulith/services/storage/internal/ledger"
)

func getFolderTx(ctx context.Context, q *db.Queries, id string) (Folder, error) {
	if !validID(id) {
		return Folder{}, ErrFolderNotFound
	}
	r, err := q.GetFolder(ctx, id)
	if noRows(err) {
		return Folder{}, ErrFolderNotFound
	}
	if err != nil {
		return Folder{}, fmt.Errorf("get folder: %w", err)
	}
	return folderFromRow(r), nil
}

// liveOwnedFolderTx returns folder id if it's live and owned by owner.
func liveOwnedFolderTx(ctx context.Context, q *db.Queries, id string, owner ledger.Owner) (Folder, error) {
	f, err := getFolderTx(ctx, q, id)
	if err != nil {
		return Folder{}, err
	}
	if f.DeletedAt != nil || f.Owner != owner {
		return Folder{}, ErrFolderNotFound
	}
	return f, nil
}

func nameTakenTx(ctx context.Context, q *db.Queries, owner ledger.Owner, parentID, name, except string) (bool, error) {
	if except == "" {
		except = zeroID
	}
	taken, err := q.FolderNameTaken(ctx, db.FolderNameTakenParams{
		OwnerKind: string(owner.Kind), OwnerID: owner.ID, ParentID: optID(parentID), NameKey: folderNameKey(name), ExceptID: except,
	})
	if err != nil {
		return false, fmt.Errorf("check folder name: %w", err)
	}
	return taken, nil
}

// depthTx is how deep folder id sits (1 = top level); 0 for "".
func depthTx(ctx context.Context, q *db.Queries, id string) (int, error) {
	if id == "" {
		return 0, nil
	}
	path, err := q.FolderPath(ctx, id)
	if err != nil {
		return 0, fmt.Errorf("folder path: %w", err)
	}
	return len(path), nil
}

// liveSubtreeTx returns folder id and its live descendants, and how many
// levels they span (1 = no sub-folders).
func liveSubtreeTx(ctx context.Context, q *db.Queries, id string) (ids []string, height int, err error) {
	rows, err := q.LiveSubtree(ctx, id)
	if err != nil {
		return nil, 0, fmt.Errorf("folder subtree: %w", err)
	}
	for _, r := range rows {
		ids = append(ids, r.ID)
		height = max(height, int(r.Level))
	}
	return ids, height, nil
}

func (p *Postgres) CreateFolder(ctx context.Context, f Folder) (Folder, error) {
	f, err := f.normalize()
	if err != nil {
		return Folder{}, err
	}
	err = p.inTx(ctx, func(q *db.Queries) error {
		if err := lockOwnerTree(ctx, q, f.Owner); err != nil {
			return err
		}
		if f.ID != "" {
			// A chosen ID already stored: a retried copy gets the same folder.
			if r, err := q.GetFolder(ctx, f.ID); err == nil {
				f = folderFromRow(r)
				return nil
			} else if !noRows(err) {
				return fmt.Errorf("get folder: %w", err)
			}
		}
		if f.ParentID != "" {
			if _, err := liveOwnedFolderTx(ctx, q, f.ParentID, f.Owner); err != nil {
				return err
			}
		}
		depth, err := depthTx(ctx, q, f.ParentID)
		if err != nil {
			return err
		}
		if err := checkFolderDepth(depth + 1); err != nil {
			return err
		}
		if taken, err := nameTakenTx(ctx, q, f.Owner, f.ParentID, f.Name, ""); err != nil || taken {
			if err == nil {
				err = fmt.Errorf("%w: %q", ErrNameTaken, f.Name)
			}
			return err
		}
		t := now()
		if f.ID == "" {
			f.ID = NewID()
		}
		f.CreatedAt, f.UpdatedAt, f.DeletedAt, f.DeletionID = t, t, nil, ""
		err = q.InsertFolder(ctx, db.InsertFolderParams{
			ID: f.ID, OwnerKind: string(f.Owner.Kind), OwnerID: f.Owner.ID, ParentID: optID(f.ParentID),
			Name: f.Name, NameKey: folderNameKey(f.Name), CreatedBy: f.CreatedBy, CreatedAt: t, UpdatedAt: t,
		})
		return nameClash(err, f.Name)
	})
	if err != nil {
		return Folder{}, err
	}
	return f, nil
}

func (p *Postgres) GetFolder(ctx context.Context, id string) (Folder, error) {
	return getFolderTx(ctx, p.q, id)
}

func (p *Postgres) ListFolders(ctx context.Context, owner ledger.Owner, parentID string) ([]Folder, error) {
	if err := owner.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidFolder, err)
	}
	if err := checkOptionalID(parentID, "parent folder", ErrInvalidFolder); err != nil {
		return nil, err
	}
	var rows []db.StorageFolder
	var err error
	if parentID == "" {
		rows, err = p.q.ListTopFolders(ctx, db.ListTopFoldersParams{OwnerKind: string(owner.Kind), OwnerID: owner.ID})
	} else {
		if _, err := liveOwnedFolderTx(ctx, p.q, parentID, owner); err != nil {
			return nil, err
		}
		rows, err = p.q.ListChildFolders(ctx, db.ListChildFoldersParams{OwnerKind: string(owner.Kind), OwnerID: owner.ID, ParentID: &parentID})
	}
	if err != nil {
		return nil, fmt.Errorf("list folders: %w", err)
	}
	return foldersFromRows(rows), nil
}

// lockedLiveFolderTx locks f's owner tree and re-reads f under the lock.
func lockedLiveFolderTx(ctx context.Context, q *db.Queries, id string) (Folder, error) {
	f, err := getFolderTx(ctx, q, id)
	if err != nil {
		return Folder{}, err
	}
	if err := lockOwnerTree(ctx, q, f.Owner); err != nil {
		return Folder{}, err
	}
	if f, err = getFolderTx(ctx, q, id); err != nil {
		return Folder{}, err
	}
	if f.DeletedAt != nil {
		return Folder{}, ErrFolderNotFound
	}
	return f, nil
}

func (p *Postgres) RenameFolder(ctx context.Context, id, name string) (Folder, error) {
	name, err := normalizeFolderName(name)
	if err != nil {
		return Folder{}, err
	}
	var result Folder
	err = p.inTx(ctx, func(q *db.Queries) error {
		f, err := lockedLiveFolderTx(ctx, q, id)
		if err != nil {
			return err
		}
		if taken, err := nameTakenTx(ctx, q, f.Owner, f.ParentID, name, id); err != nil || taken {
			if err == nil {
				err = fmt.Errorf("%w: %q", ErrNameTaken, name)
			}
			return err
		}
		f.Name, f.UpdatedAt = name, now()
		result = f
		return nameClash(q.RenameFolder(ctx, db.RenameFolderParams{ID: id, Name: name, NameKey: folderNameKey(name), UpdatedAt: f.UpdatedAt}), name)
	})
	return result, err
}

func (p *Postgres) MoveFolder(ctx context.Context, id, newParentID string) (Folder, error) {
	if err := checkOptionalID(newParentID, "parent folder", ErrInvalidFolder); err != nil {
		return Folder{}, err
	}
	var result Folder
	err := p.inTx(ctx, func(q *db.Queries) error {
		f, err := lockedLiveFolderTx(ctx, q, id)
		if err != nil {
			return err
		}
		if newParentID == f.ParentID {
			result = f // already there
			return nil
		}
		if newParentID != "" {
			if _, err := liveOwnedFolderTx(ctx, q, newParentID, f.Owner); err != nil {
				return err
			}
			path, err := q.FolderPath(ctx, newParentID)
			if err != nil {
				return fmt.Errorf("folder path: %w", err)
			}
			for _, ancestor := range path {
				if ancestor.ID == id { // the new parent is the folder itself or inside it
					return fmt.Errorf("%w: can't move a folder into itself or its own sub-folder", ErrInvalidFolder)
				}
			}
		}
		depth, err := depthTx(ctx, q, newParentID)
		if err != nil {
			return err
		}
		_, height, err := liveSubtreeTx(ctx, q, id)
		if err != nil {
			return err
		}
		if err := checkFolderDepth(depth + height); err != nil {
			return err
		}
		if taken, err := nameTakenTx(ctx, q, f.Owner, newParentID, f.Name, id); err != nil || taken {
			if err == nil {
				err = fmt.Errorf("%w: %q", ErrNameTaken, f.Name)
			}
			return err
		}
		f.ParentID, f.UpdatedAt = newParentID, now()
		result = f
		return nameClash(q.MoveFolder(ctx, db.MoveFolderParams{ID: id, ParentID: optID(newParentID), UpdatedAt: f.UpdatedAt}), f.Name)
	})
	return result, err
}

func (p *Postgres) DeleteFolder(ctx context.Context, id string) (FolderDeletion, error) {
	return p.deleteFolder(ctx, id, false)
}

func (p *Postgres) DeleteFolderTree(ctx context.Context, id string) (FolderDeletion, error) {
	return p.deleteFolder(ctx, id, true)
}

func (p *Postgres) deleteFolder(ctx context.Context, id string, tree bool) (FolderDeletion, error) {
	var result FolderDeletion
	err := p.inTx(ctx, func(q *db.Queries) error {
		f, err := getFolderTx(ctx, q, id)
		if err != nil {
			return err
		}
		if f.DeletedAt != nil {
			return nil // already in the trash
		}
		if f, err = lockedLiveFolderTx(ctx, q, id); err != nil {
			return err
		}
		ids, _, err := liveSubtreeTx(ctx, q, f.ID)
		if err != nil {
			return err
		}
		if !tree {
			assets, err := q.CountLiveAssetsIn(ctx, ids)
			if err != nil {
				return fmt.Errorf("count assets: %w", err)
			}
			if folders := len(ids) - 1; folders > 0 || assets > 0 {
				return &FolderNotEmptyError{Folders: folders, Assets: int(assets)}
			}
		}
		result, err = trashFoldersTx(ctx, q, ids)
		return err
	})
	return result, err
}

// trashFoldersTx trashes the given folders and every live asset in them
// under one new DeletionID.
func trashFoldersTx(ctx context.Context, q *db.Queries, ids []string) (FolderDeletion, error) {
	t, deletion := now(), NewID()
	folders, err := q.TrashFolders(ctx, db.TrashFoldersParams{At: &t, DeletionID: deletion, Ids: ids})
	if err != nil {
		return FolderDeletion{}, fmt.Errorf("trash folders: %w", err)
	}
	assets, err := q.TrashAssetsIn(ctx, db.TrashAssetsInParams{At: &t, DeletionID: deletion, FolderIds: ids})
	if err != nil {
		return FolderDeletion{}, fmt.Errorf("trash assets: %w", err)
	}
	return FolderDeletion{DeletionID: deletion, Folders: int(folders), Assets: int(assets)}, nil
}

func (p *Postgres) FolderPath(ctx context.Context, id string) ([]Folder, error) {
	if !validID(id) {
		return nil, ErrFolderNotFound
	}
	rows, err := p.q.FolderPath(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("folder path: %w", err)
	}
	if len(rows) == 0 {
		return nil, ErrFolderNotFound
	}
	return foldersFromRows(rows), nil
}

func (p *Postgres) ListTrashedFolders(ctx context.Context, owner ledger.Owner) ([]Folder, error) {
	if err := owner.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidFolder, err)
	}
	rows, err := p.q.ListTrashedFolders(ctx, db.ListTrashedFoldersParams{OwnerKind: string(owner.Kind), OwnerID: owner.ID})
	if err != nil {
		return nil, fmt.Errorf("list trashed folders: %w", err)
	}
	return foldersFromRows(rows), nil
}

func (p *Postgres) RestoreDeletion(ctx context.Context, deletionID string) (FolderDeletion, error) {
	if !validID(deletionID) {
		return FolderDeletion{}, fmt.Errorf("%w: bad deletion ID %q", ErrInvalidFolder, deletionID)
	}
	result := FolderDeletion{DeletionID: deletionID}
	err := p.inTx(ctx, func(q *db.Queries) error {
		rows, err := q.FoldersInDeletion(ctx, &deletionID)
		if err != nil {
			return fmt.Errorf("list deleted folders: %w", err)
		}
		pending := foldersFromRows(rows)
		owners := make([]ledger.Owner, len(pending))
		for i, f := range pending {
			owners[i] = f.Owner
		}
		if err := lockOwnerTree(ctx, q, owners...); err != nil {
			return err
		}

		// Parents before children: each round restores the folders whose
		// parent isn't still waiting, so the parent is live when they check it.
		for len(pending) > 0 {
			waiting := map[string]bool{}
			for _, f := range pending {
				waiting[f.ID] = true
			}
			var next []Folder
			for _, f := range pending {
				if waiting[f.ParentID] {
					next = append(next, f)
					continue
				}
				if err := restoreFolderTx(ctx, q, f); err != nil {
					return err
				}
				result.Folders++
			}
			if len(next) == len(pending) {
				return fmt.Errorf("restore %s: folders form a cycle", deletionID) // can't happen in a valid tree
			}
			pending = next
		}

		assets, err := q.AssetsInDeletion(ctx, &deletionID)
		if err != nil {
			return fmt.Errorf("list deleted assets: %w", err)
		}
		for _, r := range assets {
			if err := restoreAssetTx(ctx, q, assetFromRow(r)); err != nil {
				return err
			}
			result.Assets++
		}
		return nil
	})
	if err != nil {
		return FolderDeletion{}, err
	}
	return result, nil
}

// restoreFolderTx brings f back: to the top level if its parent isn't live,
// renamed "Name (2)", ... if its name is now taken.
func restoreFolderTx(ctx context.Context, q *db.Queries, f Folder) error {
	if f.ParentID != "" {
		if _, err := liveOwnedFolderTx(ctx, q, f.ParentID, f.Owner); err != nil {
			f.ParentID = ""
		}
	}
	var lookupErr error
	name := uniqueFolderName(f.Name, func(n string) bool {
		taken, err := nameTakenTx(ctx, q, f.Owner, f.ParentID, n, f.ID)
		if err != nil {
			lookupErr = err
			return false
		}
		return taken
	})
	if lookupErr != nil {
		return lookupErr
	}
	return nameClash(q.RestoreFolder(ctx, db.RestoreFolderParams{ID: f.ID, ParentID: optID(f.ParentID), Name: name, NameKey: folderNameKey(name), UpdatedAt: now()}), name)
}

// nameClash turns the unique index refusing a write (a sibling with the same
// name, added by a concurrent transaction after our check) into ErrNameTaken.
// The checks in Go catch the normal case; the index is the backstop for races.
func nameClash(err error, name string) error {
	if uniqueViolation(err) {
		return fmt.Errorf("%w: %q", ErrNameTaken, name)
	}
	return err
}

// sortedByName returns folders sorted by name, as the memory store orders them.
func sortedByName(folders []Folder) []Folder {
	slices.SortFunc(folders, func(a, b Folder) int { return strings.Compare(a.Name, b.Name) })
	return folders
}
