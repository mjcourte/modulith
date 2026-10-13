package manager

import (
	"context"

	"gyoza/services/iam/internal/directory"
	"gyoza/services/storage"
)

// Service is what other components call: the gateway, for a signed-in user
// (actorID) or from its sign-in hook (SignIn). *Manager implements it
// in-process; the gRPC client implements it too, so callers can't tell which
// they have. Callers depend on Service, never on *Manager. Cloning (step 7)
// adds to it.
type Service interface {
	// Users (manager_users.go; DeleteAccount in manager_deletions.go).
	SignIn(ctx context.Context, id directory.Identity) (SignInResult, error)
	GetMe(ctx context.Context, actorID string) (Me, error)
	DeleteAccount(ctx context.Context, actorID string) error

	// Orgs and members (manager_orgs.go; DeleteOrg in manager_deletions.go).
	CreateOrg(ctx context.Context, actorID, name string) (directory.Org, directory.Member, error)
	GetOrg(ctx context.Context, actorID, orgID string) (directory.Org, error)
	RenameOrg(ctx context.Context, actorID, orgID, name string) (directory.Org, error)
	ListMembers(ctx context.Context, actorID, orgID string) ([]directory.Member, error)
	SetRole(ctx context.Context, actorID, orgID, userID string, role directory.Role) (directory.Member, error)
	RemoveMember(ctx context.Context, actorID, orgID, userID string) (int, error)
	LeaveOrg(ctx context.Context, actorID string) (int, error)
	DeleteOrg(ctx context.Context, actorID, orgID string) error

	// Invites (manager_invites.go).
	InviteMember(ctx context.Context, actorID, orgID, email string, role directory.Role) (directory.Invite, error)
	ListOrgInvites(ctx context.Context, actorID, orgID string) ([]directory.Invite, error)
	RevokeInvite(ctx context.Context, actorID, inviteID string) error
	ListMyInvites(ctx context.Context, actorID string) ([]directory.Invite, error)
	AcceptInvite(ctx context.Context, actorID, inviteID string) (directory.Member, error)
	DeclineInvite(ctx context.Context, actorID, inviteID string) error

	// Projects and sharing (manager_projects.go; DeleteProject in manager_deletions.go).
	CreateProject(ctx context.Context, actorID string, owner storage.Owner, name string, orgLocked bool) (directory.Project, error)
	CloneProject(ctx context.Context, actorID, sourceID string, target storage.Owner) (directory.Project, error) // manager_clone.go
	GetProject(ctx context.Context, actorID, projectID string) (ProjectView, error)
	ListMyProjects(ctx context.Context, actorID string, pageSize int, pageToken string) (directory.ProjectPage, error)
	ListTemplates(ctx context.Context, actorID string, pageSize int, pageToken string) (directory.ProjectPage, error)
	RenameProject(ctx context.Context, actorID, projectID, name string) (directory.Project, error)
	MoveProjectToOrg(ctx context.Context, actorID, projectID string) (directory.Project, error)
	SetOrgLocked(ctx context.Context, actorID, projectID string, locked bool) (directory.Project, error)
	ShareProject(ctx context.Context, actorID, projectID, email string, access directory.Access) (directory.Share, error)
	ListShares(ctx context.Context, actorID, projectID string) ([]directory.Share, error)
	Unshare(ctx context.Context, actorID, projectID, userID string) error
	DeleteProject(ctx context.Context, actorID, projectID string) error

	// Service to service only (seeding/admin tooling); never exposed by the gateway.
	CreateTemplate(ctx context.Context, name string) (directory.Project, error)
	DeleteTemplate(ctx context.Context, projectID string) error
}

// Compile-time check that *Manager satisfies Service.
var _ Service = (*Manager)(nil)
