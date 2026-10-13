package manager

// Asset uses: which asset versions each project's document uses. These are
// service-to-service operations for documents, which calls them for the
// user editing the document; they must never be exposed to browsers through
// the gateway. Both are idempotent.

import (
	"context"
	"fmt"

	"gyoza/services/storage/internal/dam"
	"gyoza/services/storage/internal/ledger"
)

// RecordAssetUse records that projectID's document uses ref. The actor must
// be able to write the project and read the asset.
//
// A project only uses its own files in place: assets it owns, or that its
// owner (a user, an org, gyoza) owns, plus gyoza's shared files, which any
// project may use. Anything else, such as a file of a
// project shared with the actor, returns ErrForkRequired (with
// ErrForbidden): the client forks it into a copy the project owns. So a
// project never depends on files another owner controls and may purge
// (decided 2026-10-12). A new use needs a live asset (not in the trash) and
// an existing version.
func (m *Manager) RecordAssetUse(ctx context.Context, actorID, projectID string, ref dam.AssetRef) error {
	if err := m.canWriteProject(ctx, actorID, projectID); err != nil {
		return err
	}
	a, err := m.asset(ctx, actorID, ref.AssetID, false)
	if err != nil {
		return err
	}
	// gyoza's shared files can be used in place anywhere: no user can trash
	// or purge them, so nobody's project can break.
	if a.Owner.Kind != ledger.OwnerGyoza && a.Owner != (ledger.Owner{Kind: ledger.OwnerProject, ID: projectID}) {
		projectOwner, err := m.authz.ProjectOwner(ctx, projectID)
		if err != nil {
			return err
		}
		if a.Owner != projectOwner {
			return fmt.Errorf("%w: %w: project %s is owned by %s %q, the asset by %s %q",
				ErrForbidden, ErrForkRequired, projectID, projectOwner.Kind, projectOwner.ID, a.Owner.Kind, a.Owner.ID)
		}
	}
	return m.store.RecordUse(ctx, projectID, ref)
}

// ForgetAssetUse records that projectID's document no longer uses ref. The
// actor must be able to write the project; the asset may since be
// unreadable to them, or gone.
func (m *Manager) ForgetAssetUse(ctx context.Context, actorID, projectID string, ref dam.AssetRef) error {
	if err := m.canWriteProject(ctx, actorID, projectID); err != nil {
		return err
	}
	return m.store.ForgetUse(ctx, projectID, ref)
}

func (m *Manager) canWriteProject(ctx context.Context, actorID, projectID string) error {
	if projectID == "" {
		return dam.ErrInvalidQuery // checked again by the store; here so "" never reaches the Authorizer
	}
	return m.canWrite(ctx, actorID, ledger.Owner{Kind: ledger.OwnerProject, ID: projectID}, "")
}
