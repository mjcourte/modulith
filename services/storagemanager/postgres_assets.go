package storagemanager

// Postgres: assets and versions.

import (
	"context"
	"fmt"

	"gyoza/services/storageledger"
	"gyoza/services/storagemanager/internal/db"
)

// checkFolderTx accepts "" or a live folder of owner, inside a transaction.
func checkFolderTx(ctx context.Context, q *db.Queries, folderID string, owner storageledger.Owner) error {
	if folderID == "" {
		return nil
	}
	if !validID(folderID) {
		return ErrFolderNotFound
	}
	f, err := q.GetFolder(ctx, folderID)
	if noRows(err) {
		return ErrFolderNotFound
	}
	if err != nil {
		return fmt.Errorf("get folder: %w", err)
	}
	if f.DeletedAt != nil || ownerOf(f.OwnerKind, f.OwnerID) != owner {
		return ErrFolderNotFound
	}
	return nil
}

// insertAssetTx stores a normalised asset with first as version 1.
func insertAssetTx(ctx context.Context, q *db.Queries, a Asset, first Version) (Asset, error) {
	if a.FolderID != "" {
		// Lock the tree so a concurrent rm -rf can't trash the folder between
		// this check and the insert, leaving a live asset in a trashed folder.
		if err := lockOwnerTree(ctx, q, a.Owner); err != nil {
			return Asset{}, err
		}
		if err := checkFolderTx(ctx, q, a.FolderID, a.Owner); err != nil {
			return Asset{}, err
		}
	}
	t := now()
	a.ID = newID()
	a.CreatedAt, a.UpdatedAt, a.DeletedAt, a.DeletionID = t, t, nil, ""
	first.AssetID, first.Number, first.CreatedAt = a.ID, 1, t
	err := q.InsertAsset(ctx, db.InsertAssetParams{
		ID: a.ID, OwnerKind: string(a.Owner.Kind), OwnerID: a.Owner.ID, ProjectID: a.ProjectID, FolderID: optID(a.FolderID),
		Name: a.Name, Description: a.Description, Tags: nonNil(a.Tags), ContentType: a.ContentType, Category: string(a.Category),
		CurrentVersion: 1, CreatedBy: a.CreatedBy, CreatedAt: t, UpdatedAt: t,
	})
	if err != nil {
		return Asset{}, fmt.Errorf("insert asset: %w", err)
	}
	if err := insertVersionTx(ctx, q, first); err != nil {
		return Asset{}, err
	}
	a.Tags = nonNil(a.Tags)
	return a, nil
}

func insertVersionTx(ctx context.Context, q *db.Queries, v Version) error {
	err := q.InsertVersion(ctx, db.InsertVersionParams{
		AssetID: v.AssetID, Number: int32(v.Number), ObjectID: v.ObjectID, ContentType: v.ContentType,
		SizeBytes: v.SizeBytes, CreatedBy: v.CreatedBy, CreatedAt: v.CreatedAt,
	})
	if err != nil {
		return fmt.Errorf("insert version: %w", err)
	}
	return nil
}

// liveAssetForUpdateTx locks and returns a live asset, or ErrAssetNotFound.
func liveAssetForUpdateTx(ctx context.Context, q *db.Queries, id string) (Asset, error) {
	if !validID(id) {
		return Asset{}, ErrAssetNotFound
	}
	r, err := q.GetAssetForUpdate(ctx, id)
	if noRows(err) {
		return Asset{}, ErrAssetNotFound
	}
	if err != nil {
		return Asset{}, fmt.Errorf("get asset: %w", err)
	}
	if r.DeletedAt != nil {
		return Asset{}, ErrAssetNotFound
	}
	return assetFromRow(r), nil
}

// appendVersionTx adds v as the asset's next, current version.
func appendVersionTx(ctx context.Context, q *db.Queries, assetID string, v Version) (Version, error) {
	a, err := liveAssetForUpdateTx(ctx, q, assetID)
	if err != nil {
		return Version{}, err
	}
	if err := a.acceptsVersion(v); err != nil {
		return Version{}, err
	}
	count, err := q.CountVersions(ctx, assetID)
	if err != nil {
		return Version{}, fmt.Errorf("count versions: %w", err)
	}
	t := now()
	v.AssetID, v.Number, v.CreatedAt = assetID, int(count)+1, t
	if err := insertVersionTx(ctx, q, v); err != nil {
		return Version{}, err
	}
	err = q.SetCurrentVersion(ctx, db.SetCurrentVersionParams{ID: assetID, CurrentVersion: int32(v.Number), ContentType: v.ContentType, UpdatedAt: t})
	if err != nil {
		return Version{}, fmt.Errorf("set current version: %w", err)
	}
	return v, nil
}

func (p *Postgres) CreateAsset(ctx context.Context, a Asset, first Version) (Asset, error) {
	a, err := a.normalize(first)
	if err != nil {
		return Asset{}, err
	}
	var created Asset
	err = p.inTx(ctx, func(q *db.Queries) error {
		created, err = insertAssetTx(ctx, q, a, first)
		return err
	})
	return created, err
}

func (p *Postgres) GetAsset(ctx context.Context, id string) (Asset, error) {
	if !validID(id) {
		return Asset{}, ErrAssetNotFound
	}
	r, err := p.q.GetAsset(ctx, id)
	if noRows(err) {
		return Asset{}, ErrAssetNotFound
	}
	if err != nil {
		return Asset{}, fmt.Errorf("get asset: %w", err)
	}
	return assetFromRow(r), nil
}

func (p *Postgres) ListAssets(ctx context.Context, q AssetQuery) (AssetPage, error) {
	q, err := q.normalize()
	if err != nil {
		return AssetPage{}, err
	}
	after, err := q.afterID()
	if err != nil {
		return AssetPage{}, err
	}
	if after == "" {
		after = zeroID
	}
	var folder *string
	if q.Folder != nil {
		folder = optID(*q.Folder) // a pointer to "" (top level) becomes NULL
	}
	limit := int32(q.PageSize + 1) // one extra row says whether there is a next page

	var rows []db.StoragemanagerAsset
	switch {
	case q.ProjectID == "" && q.Folder != nil && *q.Folder == "":
		// A folder listing at the top level: its own indexable query.
		rows, err = p.q.ListAssetsAtTop(ctx, db.ListAssetsAtTopParams{
			OwnerKind: string(q.Owner.Kind), OwnerID: q.Owner.ID, AfterID: after, Tag: q.Tag, Category: string(q.Category),
			IncludeDeleted: q.IncludeDeleted, OnlyDeleted: q.OnlyDeleted, RowLimit: limit,
		})
	case q.ProjectID == "" && q.Folder != nil:
		if !validID(*q.Folder) {
			return AssetPage{}, nil // no such folder, so nothing in it
		}
		rows, err = p.q.ListAssetsInFolder(ctx, db.ListAssetsInFolderParams{
			OwnerKind: string(q.Owner.Kind), OwnerID: q.Owner.ID, FolderID: *q.Folder, AfterID: after, Tag: q.Tag, Category: string(q.Category),
			IncludeDeleted: q.IncludeDeleted, OnlyDeleted: q.OnlyDeleted, RowLimit: limit,
		})
	case q.ProjectID != "":
		rows, err = p.q.ListAssetsByProject(ctx, db.ListAssetsByProjectParams{
			ProjectID: q.ProjectID, AfterID: after, Tag: q.Tag, Category: string(q.Category),
			FilterFolder: q.Folder != nil, FolderID: folder,
			IncludeDeleted: q.IncludeDeleted, OnlyDeleted: q.OnlyDeleted, RowLimit: limit,
		})
	default:
		rows, err = p.q.ListAssetsByOwner(ctx, db.ListAssetsByOwnerParams{
			OwnerKind: string(q.Owner.Kind), OwnerID: q.Owner.ID, AfterID: after, Tag: q.Tag, Category: string(q.Category),
			FilterFolder: q.Folder != nil, FolderID: folder,
			IncludeDeleted: q.IncludeDeleted, OnlyDeleted: q.OnlyDeleted, RowLimit: limit,
		})
	}
	if err != nil {
		return AssetPage{}, fmt.Errorf("list assets: %w", err)
	}
	assets := make([]Asset, len(rows))
	for i, r := range rows {
		assets[i] = assetFromRow(r)
	}
	return assetPageOf(assets, q.PageSize), nil
}

func (p *Postgres) UpdateAsset(ctx context.Context, id string, u AssetUpdate) (Asset, error) {
	u, err := u.normalize()
	if err != nil {
		return Asset{}, err
	}
	var updated Asset
	err = p.inTx(ctx, func(q *db.Queries) error {
		if u.FolderID != nil {
			// Take the tree lock before the row lock, the same order as
			// DeleteFolderTree (tree, then the assets in it), so the two can't deadlock.
			if !validID(id) {
				return ErrAssetNotFound
			}
			current, err := q.GetAsset(ctx, id) // inside the transaction, not on p's own connection
			if noRows(err) {
				return ErrAssetNotFound
			}
			if err != nil {
				return fmt.Errorf("get asset: %w", err)
			}
			if err := lockOwnerTree(ctx, q, ownerOf(current.OwnerKind, current.OwnerID)); err != nil {
				return err
			}
		}
		a, err := liveAssetForUpdateTx(ctx, q, id)
		if err != nil {
			return err
		}
		if u.Name != nil {
			a.Name = *u.Name
		}
		if u.Description != nil {
			a.Description = *u.Description
		}
		if u.Tags != nil {
			a.Tags = *u.Tags
		}
		if u.FolderID != nil {
			if err := checkFolderTx(ctx, q, *u.FolderID, a.Owner); err != nil {
				return err
			}
			a.FolderID = *u.FolderID
		}
		a.UpdatedAt = now()
		updated = a
		return q.SaveAssetFields(ctx, db.SaveAssetFieldsParams{
			ID: id, Name: a.Name, Description: a.Description, Tags: nonNil(a.Tags), FolderID: optID(a.FolderID), UpdatedAt: a.UpdatedAt,
		})
	})
	updated.Tags = nonNil(updated.Tags)
	return updated, err
}

func (p *Postgres) AddVersion(ctx context.Context, assetID string, v Version) (Version, error) {
	var added Version
	err := p.inTx(ctx, func(q *db.Queries) error {
		var err error
		added, err = appendVersionTx(ctx, q, assetID, v)
		return err
	})
	return added, err
}

func (p *Postgres) SetCurrentVersion(ctx context.Context, assetID string, number int) (Asset, error) {
	var result Asset
	err := p.inTx(ctx, func(q *db.Queries) error {
		a, err := liveAssetForUpdateTx(ctx, q, assetID)
		if err != nil {
			return err
		}
		if number < 1 {
			return ErrVersionNotFound
		}
		v, err := q.GetVersion(ctx, db.GetVersionParams{AssetID: assetID, Number: int32(number)})
		if noRows(err) {
			return ErrVersionNotFound
		}
		if err != nil {
			return fmt.Errorf("get version: %w", err)
		}
		if number == a.CurrentVersion {
			result = a // already current: a no-op, so retries change nothing
			return nil
		}
		a.CurrentVersion, a.ContentType, a.UpdatedAt = number, v.ContentType, now()
		result = a
		return q.SetCurrentVersion(ctx, db.SetCurrentVersionParams{ID: assetID, CurrentVersion: int32(number), ContentType: v.ContentType, UpdatedAt: a.UpdatedAt})
	})
	return result, err
}

func (p *Postgres) ListVersions(ctx context.Context, assetID string) ([]Version, error) {
	if _, err := p.GetAsset(ctx, assetID); err != nil {
		return nil, err
	}
	rows, err := p.q.ListVersions(ctx, assetID)
	if err != nil {
		return nil, fmt.Errorf("list versions: %w", err)
	}
	vs := make([]Version, len(rows))
	for i, r := range rows {
		vs[i] = versionFromRow(r)
	}
	return vs, nil
}

func (p *Postgres) GetVersion(ctx context.Context, assetID string, number int) (Version, error) {
	if _, err := p.GetAsset(ctx, assetID); err != nil {
		return Version{}, err
	}
	r, err := p.q.GetVersion(ctx, db.GetVersionParams{AssetID: assetID, Number: int32(number)})
	if noRows(err) {
		return Version{}, ErrVersionNotFound
	}
	if err != nil {
		return Version{}, fmt.Errorf("get version: %w", err)
	}
	return versionFromRow(r), nil
}

func (p *Postgres) MarkAssetDeleted(ctx context.Context, id string) error {
	if !validID(id) {
		return ErrAssetNotFound
	}
	t := now()
	n, err := p.q.MarkAssetDeleted(ctx, db.MarkAssetDeletedParams{ID: id, At: &t})
	if err != nil {
		return fmt.Errorf("delete asset: %w", err)
	}
	if n == 0 {
		return ErrAssetNotFound
	}
	return nil
}

func (p *Postgres) RestoreAsset(ctx context.Context, id string) (Asset, error) {
	if !validID(id) {
		return Asset{}, ErrAssetNotFound
	}
	err := p.inTx(ctx, func(q *db.Queries) error {
		r, err := q.GetAssetForUpdate(ctx, id)
		if noRows(err) {
			return ErrAssetNotFound
		}
		if err != nil {
			return fmt.Errorf("get asset: %w", err)
		}
		if r.DeletedAt == nil {
			return nil // not in the trash
		}
		return restoreAssetTx(ctx, q, assetFromRow(r))
	})
	if err != nil {
		return Asset{}, err
	}
	return p.GetAsset(ctx, id)
}

// restoreAssetTx takes a out of the trash, to the top level if its folder isn't live.
func restoreAssetTx(ctx context.Context, q *db.Queries, a Asset) error {
	folder := a.FolderID
	if err := checkFolderTx(ctx, q, folder, a.Owner); err != nil {
		folder = ""
	}
	return q.RestoreAsset(ctx, db.RestoreAssetParams{ID: a.ID, FolderID: optID(folder), UpdatedAt: now()})
}
