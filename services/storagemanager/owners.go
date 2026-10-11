package storagemanager

import (
	"context"
	"fmt"

	"gyoza/services/storageledger"
)

// OwnerTransfer reports what TransferOwner moved.
type OwnerTransfer struct {
	Assets  int
	Folders int
	Renamed int // top-level folders renamed "Name (2)" to avoid clashing with the new owner's
}

// OwnerStore holds the bulk operations behind deleting projects and orgs and
// removing users. They're idempotent, so a caller can simply retry.
type OwnerStore interface {
	// TransferOwner gives every asset and folder of from, live or trashed, to
	// to (e.g. a departing user's library to their org). The folder trees move
	// whole; a live top-level folder whose name the new owner already uses is
	// renamed "Name (2)", "Name (3)", ... Neither side may be a project, and
	// they must differ (ErrInvalidQuery). Pending uploads don't move.
	TransferOwner(ctx context.Context, from, to storageledger.Owner) (OwnerTransfer, error)
	// MarkDeletedByOwner trashes every live folder and asset of owner under
	// one DeletionID, so the whole lot can be restored together.
	MarkDeletedByOwner(ctx context.Context, owner storageledger.Owner) (FolderDeletion, error)
	// ClearCreatedBy removes userID as creator from every asset, version and
	// folder, live or trashed. It returns how many it changed.
	ClearCreatedBy(ctx context.Context, userID string) (int, error)
	// ExpireUploadsBy ends every pending upload session userID began, now, so
	// none can be completed and the cleanup job removes them with their files.
	ExpireUploadsBy(ctx context.Context, userID string) (int, error)
}

// checkTransfer validates TransferOwner's arguments: the ledger's rule.
func checkTransfer(from, to storageledger.Owner) error {
	for _, o := range []storageledger.Owner{from, to} {
		if err := o.Validate(); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidQuery, err)
		}
		if o.Kind == storageledger.OwnerProject {
			return fmt.Errorf("%w: project-owned files can't change owner", ErrInvalidQuery)
		}
	}
	if from == to {
		return fmt.Errorf("%w: transfer to the same owner", ErrInvalidQuery)
	}
	return nil
}
