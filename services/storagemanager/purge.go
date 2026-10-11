package storagemanager

import (
	"context"
	"fmt"
	"time"
)

// PurgeStore removes for good what the cleanup job (Manager.Cleanup) has
// finished with: trash past its retention, and old completed upload sessions.
// Like the rest of Store it decides nothing: the Manager picks the cut-off
// times and deletes files from S3 and the ledger before calling these.
//
// Every method is safe to retry: purging what's already gone is not an error.
type PurgeStore interface {
	// ListAssetsTrashedBefore returns up to limit trashed assets deleted
	// before the given time, longest-trashed first.
	ListAssetsTrashedBefore(ctx context.Context, before time.Time, limit int) ([]Asset, error)
	// PurgeAsset deletes a trashed asset and all its versions for good.
	// A live asset is ErrInvalidAsset; a missing one is fine.
	PurgeAsset(ctx context.Context, id string) error
	// PurgeFolders deletes up to limit folders trashed before the given time,
	// longest-trashed first, and returns how many. Trashed folders and assets
	// that still point at them as their parent or folder are moved to the
	// top level, where restoring them would put them anyway.
	PurgeFolders(ctx context.Context, before time.Time, limit int) (int, error)
	// DeleteCompletedUploads deletes up to limit sessions completed before
	// the given time and returns how many. After that a retried
	// CompleteUpload gets ErrUploadNotFound instead of the earlier result.
	DeleteCompletedUploads(ctx context.Context, before time.Time, limit int) (int, error)
}

// checkLimit validates the batch size of the cleanup listings.
func checkLimit(limit int) error {
	if limit < 1 || limit > MaxPageSize {
		return fmt.Errorf("%w: limit %d, want 1 to %d", ErrInvalidQuery, limit, MaxPageSize)
	}
	return nil
}
