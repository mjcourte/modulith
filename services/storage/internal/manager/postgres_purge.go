package manager

// Postgres: permanent removal for the cleanup job.

import (
	"context"
	"fmt"
	"time"

	"gyoza/services/storage/internal/db"
)

func (p *Postgres) ListAssetsTrashedBefore(ctx context.Context, before time.Time, limit int) ([]Asset, error) {
	if err := checkLimit(limit); err != nil {
		return nil, err
	}
	rows, err := p.q.ListAssetsTrashedBefore(ctx, db.ListAssetsTrashedBeforeParams{Before: &before, RowLimit: int32(limit)})
	if err != nil {
		return nil, fmt.Errorf("list trashed assets: %w", err)
	}
	out := make([]Asset, len(rows))
	for i, r := range rows {
		out[i] = assetFromRow(r)
	}
	return out, nil
}

func (p *Postgres) PurgeAsset(ctx context.Context, id string) error {
	if !validID(id) {
		return nil // nothing by that ID
	}
	return p.inTx(ctx, func(q *db.Queries) error {
		r, err := q.GetAssetForUpdate(ctx, id)
		if noRows(err) {
			return nil // already gone
		}
		if err != nil {
			return fmt.Errorf("get asset: %w", err)
		}
		if r.DeletedAt == nil {
			return fmt.Errorf("%w: asset %s is not in the trash", ErrInvalidAsset, id)
		}
		if err := q.DeleteAssetVersions(ctx, id); err != nil {
			return fmt.Errorf("delete versions: %w", err)
		}
		if err := q.DeleteAsset(ctx, id); err != nil {
			return fmt.Errorf("delete asset: %w", err)
		}
		return nil
	})
}

func (p *Postgres) PurgeFolders(ctx context.Context, before time.Time, limit int) (int, error) {
	if err := checkLimit(limit); err != nil {
		return 0, err
	}
	var n int
	err := p.inTx(ctx, func(q *db.Queries) error {
		ids, err := q.ListFoldersTrashedBefore(ctx, db.ListFoldersTrashedBeforeParams{Before: &before, RowLimit: int32(limit)})
		if err != nil {
			return fmt.Errorf("list trashed folders: %w", err)
		}
		if len(ids) == 0 {
			return nil
		}
		if err := q.DetachFoldersFromParents(ctx, ids); err != nil {
			return fmt.Errorf("detach sub-folders: %w", err)
		}
		if err := q.DetachAssetsFromFolders(ctx, ids); err != nil {
			return fmt.Errorf("detach assets: %w", err)
		}
		// A live folder or asset still inside one of them makes this fail on
		// the foreign key: that would be a bug elsewhere, so it's reported,
		// not papered over.
		if err := q.DeleteFolders(ctx, ids); err != nil {
			return fmt.Errorf("delete folders: %w", err)
		}
		n = len(ids)
		return nil
	})
	return n, err
}

func (p *Postgres) DeleteCompletedUploads(ctx context.Context, before time.Time, limit int) (int, error) {
	if err := checkLimit(limit); err != nil {
		return 0, err
	}
	n, err := p.q.DeleteCompletedUploads(ctx, db.DeleteCompletedUploadsParams{Before: &before, RowLimit: int32(limit)})
	if err != nil {
		return 0, fmt.Errorf("delete completed uploads: %w", err)
	}
	return int(n), nil
}
