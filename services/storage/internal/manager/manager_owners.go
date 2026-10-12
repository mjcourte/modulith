package manager

// Owner lifecycle: what happens to files when a project, an org or a user
// goes away. These are service-to-service operations, called by iam
// once it has decided the owner is gone; they take no acting user and must
// never be exposed to browsers through the gateway.
//
// Every step is idempotent, so a caller that fails part-way (e.g. a job
// consumer that crashes) can simply run the whole operation again.

import (
	"context"
	"fmt"

	"gyoza/services/storage/internal/dam"
	"gyoza/services/storage/internal/ledger"
)

// OwnerDeletion reports what DeleteOwned put in the trash.
type OwnerDeletion struct {
	LedgerObjects int                // ledger objects newly marked deleted
	Trash         dam.FolderDeletion // DAM folders and assets trashed, under one DeletionID
}

// DeleteOwned trashes everything owner owns: a deleted project's files, an
// org's when its last user leaves, or an org-less user's. Files merely
// associated with a project but owned by someone else are untouched. Nothing
// is destroyed yet: the purge job removes it after the retention period.
func (m *Manager) DeleteOwned(ctx context.Context, owner ledger.Owner) (OwnerDeletion, error) {
	trash, err := m.store.MarkDeletedByOwner(ctx, owner)
	if err != nil {
		return OwnerDeletion{}, err
	}
	n, err := m.ledger.MarkDeletedByOwner(ctx, owner)
	if err != nil {
		return OwnerDeletion{}, fmt.Errorf("delete ledger objects of %v: %w", owner, err)
	}
	return OwnerDeletion{LedgerObjects: n, Trash: trash}, nil
}

// UserRemoval reports what RemoveUser did.
type UserRemoval struct {
	ExpiredUploads int
	// With an org: what moved to it. Without: what was trashed.
	TransferredTo ledger.Owner // zero if the user had no org
	Transfer      dam.OwnerTransfer
	LedgerObjects int // ledger objects transferred or deleted
	Deleted       OwnerDeletion
	// Creator references removed (GDPR), in the ledger and the DAM.
	ClearedLedger int
	ClearedDAM    int
}

// RemoveUser handles a user leaving. Their pending uploads are expired. If
// they belong to an org (orgID set; a user has at most one), their library
// moves to it; otherwise it's trashed. Either way they're removed as creator
// from every file, so no personal reference to them remains.
func (m *Manager) RemoveUser(ctx context.Context, userID, orgID string) (UserRemoval, error) {
	if userID == "" {
		return UserRemoval{}, fmt.Errorf("%w: empty userID", dam.ErrInvalidQuery)
	}
	user := ledger.Owner{Kind: ledger.OwnerUser, ID: userID}
	var r UserRemoval
	var err error

	// First, so nothing new can land in their library while it's handled.
	if r.ExpiredUploads, err = m.store.ExpireUploadsBy(ctx, userID); err != nil {
		return UserRemoval{}, err
	}

	if orgID != "" {
		org := ledger.Owner{Kind: ledger.OwnerOrg, ID: orgID}
		if r.Transfer, err = m.store.TransferOwner(ctx, user, org); err != nil {
			return UserRemoval{}, err
		}
		if r.LedgerObjects, err = m.ledger.TransferOwner(ctx, user, org); err != nil {
			return UserRemoval{}, fmt.Errorf("transfer ledger objects: %w", err)
		}
		r.TransferredTo = org
	} else {
		if r.Deleted, err = m.DeleteOwned(ctx, user); err != nil {
			return UserRemoval{}, err
		}
		r.LedgerObjects = r.Deleted.LedgerObjects
	}

	if r.ClearedDAM, err = m.store.ClearCreatedBy(ctx, userID); err != nil {
		return UserRemoval{}, err
	}
	if r.ClearedLedger, err = m.ledger.ClearCreatedBy(ctx, userID); err != nil {
		return UserRemoval{}, fmt.Errorf("clear ledger creators: %w", err)
	}
	return r, nil
}
