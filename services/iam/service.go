package iam

import (
	"context"

	"gyoza/services/storageledger"
)

// Service is what other components call: the gateway, for a signed-in user
// (actorID) or from its sign-in hook (SignIn). *Manager implements it
// in-process; a gRPC client will implement it too (step 9), so callers can't
// tell which they have. Callers depend on Service, never on *Manager.
//
// It grows with the plan: account and org deletion in step 6, cloning in step 7.
type Service interface {
	// Users (manager_users.go).
	SignIn(ctx context.Context, id Identity) (SignInResult, error)
	GetMe(ctx context.Context, actorID string) (Me, error)

	// Orgs and members (manager_orgs.go).
	CreateOrg(ctx context.Context, actorID, name string) (Org, Member, error)
	GetOrg(ctx context.Context, actorID, orgID string) (Org, error)
	RenameOrg(ctx context.Context, actorID, orgID, name string) (Org, error)
	ListMembers(ctx context.Context, actorID, orgID string) ([]Member, error)
	SetRole(ctx context.Context, actorID, orgID, userID string, role Role) (Member, error)
	RemoveMember(ctx context.Context, actorID, orgID, userID string) (int, error)
	LeaveOrg(ctx context.Context, actorID string) (int, error)

	// Invites (manager_invites.go).
	InviteMember(ctx context.Context, actorID, orgID, email string, role Role) (Invite, error)
	ListOrgInvites(ctx context.Context, actorID, orgID string) ([]Invite, error)
	RevokeInvite(ctx context.Context, actorID, inviteID string) error
	ListMyInvites(ctx context.Context, actorID string) ([]Invite, error)
	AcceptInvite(ctx context.Context, actorID, inviteID string) (Member, error)
	DeclineInvite(ctx context.Context, actorID, inviteID string) error

	// Projects and sharing (manager_projects.go).
	CreateProject(ctx context.Context, actorID string, owner storageledger.Owner, name string, orgLocked bool) (Project, error)
	GetProject(ctx context.Context, actorID, projectID string) (ProjectView, error)
	ListMyProjects(ctx context.Context, actorID string, pageSize int, pageToken string) (ProjectPage, error)
	ListTemplates(ctx context.Context, actorID string, pageSize int, pageToken string) (ProjectPage, error)
	RenameProject(ctx context.Context, actorID, projectID, name string) (Project, error)
	MoveProjectToOrg(ctx context.Context, actorID, projectID string) (Project, error)
	SetOrgLocked(ctx context.Context, actorID, projectID string, locked bool) (Project, error)
	ShareProject(ctx context.Context, actorID, projectID, email string, access Access) (Share, error)
	ListShares(ctx context.Context, actorID, projectID string) ([]Share, error)
	Unshare(ctx context.Context, actorID, projectID, userID string) error

	// Service to service only (seeding/admin tooling); never exposed by the gateway.
	CreateTemplate(ctx context.Context, name string) (Project, error)
}

// Compile-time check that *Manager satisfies Service.
var _ Service = (*Manager)(nil)
