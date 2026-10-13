package manager

import (
	"context"
	"errors"
	"fmt"
)

// ErrForbidden means the acting user may not do what they asked.
var ErrForbidden = errors.New("documents: forbidden")

// Authorizer decides whether a user may read or edit a project's document.
// It's declared here, where it's used; iam implements it, so documents never
// reads iam's tables. Each method returns nil if allowed, or an error
// wrapping ErrForbidden (an unknown project is forbidden too).
type Authorizer interface {
	CanRead(ctx context.Context, actorID, projectID string) error
	CanWrite(ctx context.Context, actorID, projectID string) error
}

// AllowAll is the stand-in Authorizer until iam's is wired: every identified
// user may do anything. Never deploy it where real users can reach it.
type AllowAll struct{}

var _ Authorizer = AllowAll{}

func (AllowAll) CanRead(ctx context.Context, actorID, projectID string) error {
	return requireActor(actorID)
}

func (AllowAll) CanWrite(ctx context.Context, actorID, projectID string) error {
	return requireActor(actorID)
}

// requireActor rejects calls without an acting user: every request should
// carry one (the gateway authenticates), so a missing ID is a wiring bug.
func requireActor(actorID string) error {
	if actorID == "" {
		return fmt.Errorf("%w: no acting user", ErrForbidden)
	}
	return nil
}
