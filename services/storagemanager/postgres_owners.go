package storagemanager

// Postgres: owner lifecycle.

import (
	"context"
	"fmt"

	"gyoza/services/storageledger"
	"gyoza/services/storagemanager/internal/db"
)

func (p *Postgres) TransferOwner(ctx context.Context, from, to storageledger.Owner) (OwnerTransfer, error) {
	if err := checkTransfer(from, to); err != nil {
		return OwnerTransfer{}, err
	}
	var result OwnerTransfer
	err := p.inTx(ctx, func(q *db.Queries) error {
		if err := lockOwnerTree(ctx, q, from, to); err != nil {
			return err
		}
		// Top-level folders move one at a time, owner and (possibly numbered)
		// name together, so each is checked against the new owner's folders,
		// including those moved before it, and never clashes on the way.
		top, err := q.ListTopFolders(ctx, db.ListTopFoldersParams{OwnerKind: string(from.Kind), OwnerID: from.ID})
		if err != nil {
			return fmt.Errorf("list folders: %w", err)
		}
		for _, f := range sortedByName(foldersFromRows(top)) {
			var lookupErr error
			name := uniqueFolderName(f.Name, func(n string) bool {
				taken, err := nameTakenTx(ctx, q, to, "", n, f.ID)
				if err != nil {
					lookupErr = err
				}
				return taken
			})
			if lookupErr != nil {
				return lookupErr
			}
			if name != f.Name {
				result.Renamed++
			}
			err := q.TransferFolder(ctx, db.TransferFolderParams{ID: f.ID, OwnerKind: string(to.Kind), OwnerID: to.ID, Name: name, NameKey: folderNameKey(name)})
			if err := nameClash(err, name); err != nil {
				return fmt.Errorf("transfer folder: %w", err)
			}
			result.Folders++
		}
		// The rest (sub-folders keep their parents; trashed folders aren't
		// in the name rule) can move in bulk.
		rest, err := q.TransferFolders(ctx, db.TransferFoldersParams{FromKind: string(from.Kind), FromID: from.ID, ToKind: string(to.Kind), ToID: to.ID})
		if err != nil {
			return fmt.Errorf("transfer folders: %w", err)
		}
		result.Folders += int(rest)
		assets, err := q.TransferAssets(ctx, db.TransferAssetsParams{FromKind: string(from.Kind), FromID: from.ID, ToKind: string(to.Kind), ToID: to.ID})
		if err != nil {
			return fmt.Errorf("transfer assets: %w", err)
		}
		result.Assets = int(assets)
		return nil
	})
	if err != nil {
		return OwnerTransfer{}, err
	}
	return result, nil
}

func (p *Postgres) MarkDeletedByOwner(ctx context.Context, owner storageledger.Owner) (FolderDeletion, error) {
	if err := owner.Validate(); err != nil {
		return FolderDeletion{}, fmt.Errorf("%w: %w", ErrInvalidQuery, err)
	}
	var result FolderDeletion
	err := p.inTx(ctx, func(q *db.Queries) error {
		if err := lockOwnerTree(ctx, q, owner); err != nil {
			return err
		}
		t, deletion := now(), newID()
		folders, err := q.TrashOwnerFolders(ctx, db.TrashOwnerFoldersParams{At: &t, DeletionID: deletion, OwnerKind: string(owner.Kind), OwnerID: owner.ID})
		if err != nil {
			return fmt.Errorf("trash folders: %w", err)
		}
		assets, err := q.TrashOwnerAssets(ctx, db.TrashOwnerAssetsParams{At: &t, DeletionID: deletion, OwnerKind: string(owner.Kind), OwnerID: owner.ID})
		if err != nil {
			return fmt.Errorf("trash assets: %w", err)
		}
		result = FolderDeletion{DeletionID: deletion, Folders: int(folders), Assets: int(assets)}
		return nil
	})
	return result, err
}

func (p *Postgres) ClearCreatedBy(ctx context.Context, userID string) (int, error) {
	if userID == "" {
		return 0, fmt.Errorf("%w: empty userID", ErrInvalidQuery)
	}
	total := 0
	err := p.inTx(ctx, func(q *db.Queries) error {
		for _, clear := range []func(context.Context, string) (int64, error){q.ClearAssetCreator, q.ClearFolderCreator, q.ClearVersionCreator} {
			n, err := clear(ctx, userID)
			if err != nil {
				return fmt.Errorf("clear creator: %w", err)
			}
			total += int(n)
		}
		return nil
	})
	return total, err
}

func (p *Postgres) ExpireUploadsBy(ctx context.Context, userID string) (int, error) {
	if userID == "" {
		return 0, fmt.Errorf("%w: empty userID", ErrInvalidQuery)
	}
	n, err := p.q.ExpireUploadsBy(ctx, db.ExpireUploadsByParams{UserID: userID, At: now()})
	if err != nil {
		return 0, fmt.Errorf("expire uploads: %w", err)
	}
	return int(n), nil
}
