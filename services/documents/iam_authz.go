package documents

import (
	"context"
	"errors"
	"fmt"

	"gyoza/services/storage"
)

// IAMAuthorizer is documents' Authorizer, answered by iam. iam already
// implements storage's Authorizer, which resolves a project owner to the
// actor's access on that project (owner, org member, or share: viewer reads,
// editor writes) and refuses unknown actors, users being deleted, unknown
// projects, and projects being cloned or deleted. So documents asks the same
// question with the project as the files' owner.
//
// Wire iam.NewStorageAuthorizer(store) in the monolith (the value storage
// gets) and iam.NewAuthzClient(conn) in distributed mode.
type IAMAuthorizer struct {
	iam storage.Authorizer
}

func NewIAMAuthorizer(iam storage.Authorizer) *IAMAuthorizer {
	return &IAMAuthorizer{iam: iam}
}

var _ Authorizer = (*IAMAuthorizer)(nil)

func (a *IAMAuthorizer) CanRead(ctx context.Context, actorID, projectID string) error {
	return forbidden(a.iam.CanRead(ctx, actorID, projectOwner(projectID), projectID))
}

func (a *IAMAuthorizer) CanWrite(ctx context.Context, actorID, projectID string) error {
	return forbidden(a.iam.CanWrite(ctx, actorID, projectOwner(projectID), projectID))
}

func projectOwner(projectID string) storage.Owner {
	return storage.Owner{Kind: storage.OwnerProject, ID: projectID}
}

// forbidden re-wraps iam's refusals (storage.ErrForbidden) as documents'
// ErrForbidden, keeping the original in the chain. Any other error (iam's
// store failing) passes through: it isn't a refusal.
func forbidden(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, storage.ErrForbidden) {
		return fmt.Errorf("%w: %w", ErrForbidden, err)
	}
	return err
}
