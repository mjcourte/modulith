package iam

import (
	"context"
	"errors"
	"time"
)

// Org errors returned by every Store (see OrgStore).
var (
	ErrOrgNotFound = errors.New("iam: org not found")
	ErrInvalidOrg  = errors.New("iam: invalid org")
	// ErrAlreadyMember: the user already belongs to an org (a user has at most one).
	ErrAlreadyMember = errors.New("iam: user already belongs to an org")
	// ErrNotMember: the user isn't a member of that org (or of any, for GetMembership).
	ErrNotMember = errors.New("iam: not a member")
	// ErrLastAdmin: the change would leave an org with members but no admin.
	ErrLastAdmin = errors.New("iam: an org needs at least one admin")
)

// Role is what a member may do in their org.
type Role string

const (
	RoleAdmin  Role = "admin"  // invites and removes people, changes roles, manages org projects
	RoleMember Role = "member" // uses the org's files and projects
)

// Org is a team of users that can own files and projects together.
type Org struct {
	ID         string // UUIDv7
	Name       string // not unique: orgs are found through their members, not by name
	CreatedAt  time.Time
	UpdatedAt  time.Time
	DeletingAt *time.Time // set when the org's deletion starts (see DeletionStore)
}

// Member is one user's membership of an org.
type Member struct {
	OrgID    string
	UserID   string
	Role     Role
	JoinedAt time.Time
}

// OrgStore persists orgs and their members. Pure data, no authorization: it
// enforces the invariants that must hold however the Manager is called,
// including under concurrent calls:
//   - a user belongs to at most one org (ErrAlreadyMember)
//   - an org with members always has an admin (ErrLastAdmin)
type OrgStore interface {
	// CreateOrg creates an org with its creator as the first admin, atomically,
	// assigning the ID and timestamps. The creator must exist (ErrUserNotFound)
	// and not already be in an org (ErrAlreadyMember).
	CreateOrg(ctx context.Context, o Org, creatorID string) (Org, Member, error)
	// GetOrg returns the org, or ErrOrgNotFound.
	GetOrg(ctx context.Context, id string) (Org, error)
	// RenameOrg changes the org's name.
	RenameOrg(ctx context.Context, id, name string) (Org, error)

	// GetMembership returns the user's membership, or ErrNotMember if they're
	// in no org.
	GetMembership(ctx context.Context, userID string) (Member, error)
	// ListMembers returns the org's members, admins first, then by join time.
	ListMembers(ctx context.Context, orgID string) ([]Member, error)
	// SetRole changes a member's role. Demoting the last admin is ErrLastAdmin.
	SetRole(ctx context.Context, orgID, userID string, role Role) (Member, error)
	// RemoveMember takes the user out of the org and returns how many members
	// remain. Removing the last admin while others remain is ErrLastAdmin;
	// removing the very last member is allowed (the Manager then deletes the
	// org). Removing a non-member is ErrNotMember.
	RemoveMember(ctx context.Context, orgID, userID string) (remaining int, err error)
}
