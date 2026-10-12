package iam

import (
	"context"
	"errors"
	"fmt"

	"gyoza/services/storage"
)

// StorageAuthorizer answers storage's "may this user read/write these
// files?" from iam's data. It needs only a Store (reads), not the Manager:
// that's what lets the binaries build it before storage, and
// storage before iam's Manager (which calls storage).
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

// Compile-time check that *StorageAuthorizer satisfies storage.Authorizer.
var _ storage.Authorizer = (*StorageAuthorizer)(nil)

func (a *StorageAuthorizer) CanRead(ctx context.Context, actorID string, owner storage.Owner, projectID string) error {
	return a.check(ctx, actorID, owner, projectID, AccessViewer)
}

func (a *StorageAuthorizer) CanWrite(ctx context.Context, actorID string, owner storage.Owner, projectID string) error {
	return a.check(ctx, actorID, owner, projectID, AccessEditor)
}

// check allows the call if actorID reaches the files with at least want
// (viewer = read, editor = write). Refusals wrap storage.ErrForbidden,
// as the Authorizer contract asks; Store failures are returned as they are.
func (a *StorageAuthorizer) check(ctx context.Context, actorID string, owner storage.Owner, projectID string, want Access) error {
	verb := map[Access]string{AccessViewer: "read", AccessEditor: "write"}[want]
	forbidden := fmt.Errorf("%w: %s may not %s files of %s %q", storage.ErrForbidden, actorID, verb, owner.Kind, owner.ID)
	if actorID == "" {
		return fmt.Errorf("%w: no acting user", storage.ErrForbidden)
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
	if want == AccessViewer && projectID != "" && owner != (storage.Owner{Kind: storage.OwnerProject, ID: projectID}) {
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
func (a *StorageAuthorizer) ownerAccess(ctx context.Context, actorID string, owner storage.Owner) (Access, error) {
	switch owner.Kind {
	case storage.OwnerUser:
		if owner.ID == actorID {
			return AccessEditor, nil // your own library
		}
	case storage.OwnerOrg:
		org, err := actorOrg(ctx, a.store, actorID)
		if err != nil {
			return AccessNone, err
		}
		if org != "" && org == owner.ID {
			return AccessEditor, nil
		}
	case storage.OwnerProject:
		return a.projectAccess(ctx, actorID, owner.ID)
	case storage.OwnerGyoza:
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
