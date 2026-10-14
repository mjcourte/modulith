package directory

import (
	"context"
	"errors"
	"time"

	"gyoza/services/storage"
)

// ErrNotDeleting: a hard delete was asked for something whose deletion
// hasn't been started (marked) first.
var ErrNotDeleting = errors.New("iam: not marked for deletion")

// DeletionStore holds the steps of the Manager's deletion cascades (deleting
// a project, an org or an account). Every deletion starts by marking the row
// (DeletingAt), which the rest of the Store respects at once: a row being
// deleted takes no new references (no new projects, shares, members or
// invites for it), listings hide projects being deleted, and the Manager's
// access rules grant nothing through it. The cascade then works through the
// steps, each safe to repeat, and ends with a hard delete that refuses to run
// while the cascade is unfinished.
type DeletionStore interface {
	// MarkUserDeleting starts an account deletion, recording toOrgID: the org
	// the user's files and projects move to ("" if none). Marking again keeps
	// the first time and destination, so a resumed deletion can't change course.
	MarkUserDeleting(ctx context.Context, userID, toOrgID string) (User, error)
	// MarkOrgDeleting starts an org deletion and marks every project the org
	// owns as deleting too, in one step. Marking again changes nothing.
	MarkOrgDeleting(ctx context.Context, orgID string) (Org, error)
	// MarkProjectDeleting starts a project deletion. Marking again changes nothing.
	MarkProjectDeleting(ctx context.Context, projectID string) (Project, error)

	// ListDeletingUsers, ListDeletingOrgs and ListDeletingProjects return up to
	// limit rows whose deletion has started, longest-running first: the cleanup
	// loop's work list for resuming interrupted cascades.
	ListDeletingUsers(ctx context.Context, limit int) ([]User, error)
	ListDeletingOrgs(ctx context.Context, limit int) ([]Org, error)
	ListDeletingProjects(ctx context.Context, limit int) ([]Project, error)
	// ListOwnedProjects returns up to limit project IDs owned by owner, in any
	// state, for a cascade to work through.
	ListOwnedProjects(ctx context.Context, owner storage.Owner, limit int) ([]string, error)

	// TransferUserProjects gives every project userID owns to toOrgID (account
	// deletion with an org). They arrive unlocked (user projects can't be
	// locked); shares are kept. It returns how
	// many moved. The org must exist and not be deleting (ErrOrgNotFound).
	TransferUserProjects(ctx context.Context, userID, toOrgID string) (int, error)
	// ForgetUser removes a user being deleted from everything that isn't
	// theirs: shares to them, and invites and pending shares to their email,
	// are deleted; they're cleared as creator (projects), sharer (shares,
	// pending shares) and inviter (invites). It returns how many rows changed.
	ForgetUser(ctx context.Context, userID string) (int, error)

	// DeleteProject removes a project marked deleting, with its shares and
	// pending shares. ErrNotDeleting if it isn't marked; a missing project is
	// fine.
	DeleteProject(ctx context.Context, projectID string) error
	// DeleteOrg removes an org marked deleting, with its memberships and
	// invites. ErrNotDeleting if it isn't marked; ErrInvalidOrg while it still
	// owns projects (the cascade isn't done); a missing org is fine.
	DeleteOrg(ctx context.Context, orgID string) error
	// DeleteUser removes a user marked deleting, with any shares to them,
	// freeing their email.
	// ErrNotDeleting if they aren't marked; ErrInvalidUser while they still own
	// projects or belong to an org (the cascade isn't done); missing is fine.
	DeleteUser(ctx context.Context, userID string) error

	// DeleteExpiredInvites removes up to limit invites that expired before the
	// given time and returns how many.
	DeleteExpiredInvites(ctx context.Context, before time.Time, limit int) (int, error)
	// DeleteExpiredPendingShares removes up to limit pending shares that
	// expired before the given time and returns how many.
	DeleteExpiredPendingShares(ctx context.Context, before time.Time, limit int) (int, error)
}
