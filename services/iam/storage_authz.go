package iam

import (
	"context"
	"errors"
	"fmt"

	"gyoza/services/storageledger"
	"gyoza/services/storagemanager"
)

// StorageAuthorizer answers storagemanager's "may this user read/write these
// files?" from iam's data. It needs only a Store (reads), not the Manager:
// that's what lets the binaries build it before storagemanager, and
// storagemanager before iam's Manager (which calls storagemanager).
//
// Files are reachable two ways:
//   - through their owner: a user's own library (read-write), their org's
//     (read-write for members), a project's (the project access: editor ->
//     write, viewer -> read), gyoza's shared files (read-only for everyone)
//   - through a project they're associated with (projectID): anyone who can
//     read that project can read them, but writing still needs the owner route
type StorageAuthorizer struct {
	store Store
}

// NewStorageAuthorizer returns an Authorizer reading from s.
func NewStorageAuthorizer(s Store) *StorageAuthorizer {
	return &StorageAuthorizer{store: s}
}

// Compile-time check that *StorageAuthorizer satisfies storagemanager.Authorizer.
var _ storagemanager.Authorizer = (*StorageAuthorizer)(nil)

func (a *StorageAuthorizer) CanRead(ctx context.Context, actorID string, owner storageledger.Owner, projectID string) error {
	return a.check(ctx, actorID, owner, projectID, AccessViewer)
}

func (a *StorageAuthorizer) CanWrite(ctx context.Context, actorID string, owner storageledger.Owner, projectID string) error {
	return a.check(ctx, actorID, owner, projectID, AccessEditor)
}

// check allows the call if actorID reaches the files with at least want
// (viewer = read, editor = write). Refusals wrap storagemanager.ErrForbidden,
// as the Authorizer contract asks; Store failures are returned as they are.
func (a *StorageAuthorizer) check(ctx context.Context, actorID string, owner storageledger.Owner, projectID string, want Access) error {
	verb := map[Access]string{AccessViewer: "read", AccessEditor: "write"}[want]
	forbidden := fmt.Errorf("%w: %s may not %s files of %s %q", storagemanager.ErrForbidden, actorID, verb, owner.Kind, owner.ID)
	if actorID == "" {
		return fmt.Errorf("%w: no acting user", storagemanager.ErrForbidden)
	}
	u, err := a.store.GetUser(ctx, actorID)
	if errors.Is(err, ErrUserNotFound) || (err == nil && u.DeletingAt != nil) {
		return forbidden // unknown, or their account is being deleted
	}
	if err != nil {
		return err
	}

	got, err := a.ownerAccess(ctx, actorID, owner)
	if err != nil {
		return err
	}
	if got.AtLeast(want) {
		return nil
	}
	// The project route grants read only, and only for a project other than
	// the owner itself (a project owner was already checked above).
	if want == AccessViewer && projectID != "" && owner != (storageledger.Owner{Kind: storageledger.OwnerProject, ID: projectID}) {
		access, err := a.projectAccess(ctx, actorID, projectID)
		if err != nil {
			return err
		}
		if access.AtLeast(AccessViewer) {
			return nil
		}
	}
	return forbidden
}

// ownerAccess is actorID's access to everything owner owns.
func (a *StorageAuthorizer) ownerAccess(ctx context.Context, actorID string, owner storageledger.Owner) (Access, error) {
	switch owner.Kind {
	case storageledger.OwnerUser:
		if owner.ID == actorID {
			return AccessEditor, nil // your own library
		}
	case storageledger.OwnerOrg:
		org, err := actorOrg(ctx, a.store, actorID)
		if err != nil {
			return AccessNone, err
		}
		if org != "" && org == owner.ID {
			return AccessEditor, nil
		}
	case storageledger.OwnerProject:
		return a.projectAccess(ctx, actorID, owner.ID)
	case storageledger.OwnerGyoza:
		return AccessViewer, nil // shared files: everyone reads, nobody writes
	}
	return AccessNone, nil
}

// projectAccess is actorID's access to a project; a missing project grants
// nothing (and isn't an error: the caller only learns "forbidden").
func (a *StorageAuthorizer) projectAccess(ctx context.Context, actorID, projectID string) (Access, error) {
	_, access, err := accessTo(ctx, a.store, actorID, projectID)
	if errors.Is(err, ErrProjectNotFound) {
		return AccessNone, nil
	}
	return access, err
}
