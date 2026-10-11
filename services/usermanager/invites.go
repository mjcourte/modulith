package usermanager

import (
	"context"
	"errors"
	"time"
)

// Invite errors returned by every Store (see InviteStore).
var (
	ErrInviteNotFound = errors.New("usermanager: invite not found")
	ErrInvalidInvite  = errors.New("usermanager: invalid invite")
	ErrInviteExpired  = errors.New("usermanager: invite expired")
)

// Invite asks whoever signs in with Email to join an org. No email is sent
// (MVP): the invited person signs in with that address and sees the invite.
type Invite struct {
	ID        string // UUIDv7
	OrgID     string
	Email     string // lowercased
	Role      Role   // the role they get on accepting
	InvitedBy string // user ID of the admin who invited them
	CreatedAt time.Time
	ExpiresAt time.Time
}

// InviteStore persists pending invites. An accepted, declined or revoked
// invite is deleted; expired ones stay until the cleanup loop removes them.
type InviteStore interface {
	// CreateInvite stores an invite, assigning its ID and CreatedAt. The org
	// must exist (ErrOrgNotFound). Inviting an email that already has a
	// pending invite to the org replaces that invite's role and expiry and
	// keeps its ID, so inviting twice doesn't create two.
	CreateInvite(ctx context.Context, inv Invite) (Invite, error)
	// GetInvite returns the invite, expired or not, or ErrInviteNotFound.
	GetInvite(ctx context.Context, id string) (Invite, error)
	// ListOrgInvites returns the org's invites, expired ones included (so an
	// admin can re-invite), newest first.
	ListOrgInvites(ctx context.Context, orgID string) ([]Invite, error)
	// ListEmailInvites returns the unexpired invites for an email address
	// (any letter case), newest first: what a user sees after signing in.
	ListEmailInvites(ctx context.Context, email string) ([]Invite, error)
	// AcceptInvite makes the user a member of the invite's org with its role
	// and deletes the invite, atomically. An expired invite is
	// ErrInviteExpired; a user already in an org is ErrAlreadyMember. Whether
	// this user may accept (their email matches) is the Manager's check.
	AcceptInvite(ctx context.Context, inviteID, userID string) (Member, error)
	// DeleteInvite removes an invite (declined or revoked). Deleting a missing
	// one is not an error, so it's safe to retry.
	DeleteInvite(ctx context.Context, id string) error
}
