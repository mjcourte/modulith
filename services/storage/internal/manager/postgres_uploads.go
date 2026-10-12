package manager

// Postgres: upload sessions.

import (
	"context"
	"fmt"
	"time"

	"gyoza/services/storage/internal/db"
)

// checkUploadTargetTx repeats, against stored data, the checks CreateUpload
// promises: the folder for a new asset, or the existing asset for a version.
func checkUploadTargetTx(ctx context.Context, q *db.Queries, u Upload) error {
	if u.AssetID == "" {
		return checkFolderTx(ctx, q, u.FolderID, u.Owner)
	}
	if !validID(u.AssetID) {
		return ErrAssetNotFound
	}
	r, err := q.GetAsset(ctx, u.AssetID)
	if noRows(err) {
		return ErrAssetNotFound
	}
	if err != nil {
		return fmt.Errorf("get asset: %w", err)
	}
	a := assetFromRow(r)
	if a.DeletedAt != nil || a.Owner != u.Owner {
		return ErrAssetNotFound
	}
	return a.acceptsVersion(Version{ObjectID: "pending", ContentType: u.ContentType, SizeBytes: u.SizeBytes})
}

func (p *Postgres) CreateUpload(ctx context.Context, u Upload) (Upload, error) {
	u, err := u.normalize()
	if err != nil {
		return Upload{}, err
	}
	if err := checkUploadTargetTx(ctx, p.q, u); err != nil {
		return Upload{}, err
	}
	u.ID, u.ObjectID, u.CreatedAt = newID(), newID(), now()
	u.CompletedAt, u.ResultAssetID, u.ResultVersion = nil, "", 0
	u.Tags = nonNil(u.Tags)
	err = p.q.InsertUpload(ctx, db.InsertUploadParams{
		ID: u.ID, OwnerKind: string(u.Owner.Kind), OwnerID: u.Owner.ID, ProjectID: u.ProjectID, CreatedBy: u.CreatedBy,
		AssetID: optID(u.AssetID), Name: u.Name, Description: u.Description, Tags: u.Tags, FolderID: optID(u.FolderID),
		Bucket: u.Bucket, Key: u.Key, ContentType: u.ContentType, SizeBytes: u.SizeBytes,
		ExpiresAt: u.ExpiresAt, CreatedAt: u.CreatedAt, ObjectID: u.ObjectID,
	})
	if err != nil {
		return Upload{}, fmt.Errorf("insert upload: %w", err)
	}
	return u, nil
}

func getUploadTx(ctx context.Context, q *db.Queries, id string, forUpdate bool) (Upload, error) {
	if !validID(id) {
		return Upload{}, ErrUploadNotFound
	}
	get := q.GetUpload
	if forUpdate {
		get = q.GetUploadForUpdate
	}
	r, err := get(ctx, id)
	if noRows(err) {
		return Upload{}, ErrUploadNotFound
	}
	if err != nil {
		return Upload{}, fmt.Errorf("get upload: %w", err)
	}
	return uploadFromRow(r), nil
}

func (p *Postgres) GetUpload(ctx context.Context, id string) (Upload, error) {
	return getUploadTx(ctx, p.q, id, false)
}

func (p *Postgres) CompleteUpload(ctx context.Context, id string, v Version) (Asset, error) {
	var result Asset
	err := p.inTx(ctx, func(q *db.Queries) error {
		// Locking the session makes overlapping completions take turns.
		u, err := getUploadTx(ctx, q, id, true)
		if err != nil {
			return err
		}
		if u.CompletedAt != nil {
			r, err := q.GetAsset(ctx, u.ResultAssetID) // already done: the same result again
			if err != nil {
				return fmt.Errorf("get completed upload's asset: %w", err)
			}
			result = assetFromRow(r)
			return nil
		}
		t := now()
		if u.expired(t) {
			return fmt.Errorf("%w: expired at %s", ErrUploadExpired, u.ExpiresAt.Format(time.RFC3339))
		}
		if err := u.matches(v); err != nil {
			return err
		}
		if v.CreatedBy == "" {
			v.CreatedBy = u.CreatedBy
		}

		if u.AssetID == "" {
			a, err := u.newAsset().normalize(v)
			if err != nil {
				return err
			}
			if result, err = insertAssetTx(ctx, q, a, v); err != nil {
				return err
			}
			u.ResultVersion = 1
		} else {
			if err := checkUploadTargetTx(ctx, q, u); err != nil { // e.g. the asset was trashed meanwhile
				return err
			}
			added, err := appendVersionTx(ctx, q, u.AssetID, v)
			if err != nil {
				return err
			}
			r, err := q.GetAsset(ctx, u.AssetID)
			if err != nil {
				return fmt.Errorf("get asset: %w", err)
			}
			result, u.ResultVersion = assetFromRow(r), added.Number
		}
		return q.MarkUploadCompleted(ctx, db.MarkUploadCompletedParams{
			ID: id, CompletedAt: &t, ResultAssetID: &result.ID, ResultVersion: int32(u.ResultVersion),
		})
	})
	if err != nil {
		return Asset{}, err
	}
	return result, nil
}

func (p *Postgres) ListExpiredUploads(ctx context.Context, before time.Time, limit int) ([]Upload, error) {
	if err := checkLimit(limit); err != nil {
		return nil, err
	}
	rows, err := p.q.ListExpiredUploads(ctx, db.ListExpiredUploadsParams{Before: before, RowLimit: int32(limit)})
	if err != nil {
		return nil, fmt.Errorf("list expired uploads: %w", err)
	}
	out := make([]Upload, len(rows))
	for i, r := range rows {
		out[i] = uploadFromRow(r)
	}
	return out, nil
}

func (p *Postgres) DeleteUpload(ctx context.Context, id string) error {
	if !validID(id) {
		return nil // nothing by that ID: deleting it is a no-op
	}
	if err := p.q.DeleteUpload(ctx, id); err != nil {
		return fmt.Errorf("delete upload: %w", err)
	}
	return nil
}
