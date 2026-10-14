package grpcapi

import (
	"context"

	"google.golang.org/grpc"

	pb "gyoza/contracts/gen/go/gyoza/iam/v1"
	"gyoza/services/iam/internal/directory"
	"gyoza/services/iam/internal/manager"
	"gyoza/services/storage"
)

// Client is a Service that calls a remote iam over gRPC.
type Client struct {
	c pb.IAMServiceClient
}

// NewClient uses conn, which the caller creates (grpc.NewClient) and closes.
func NewClient(conn grpc.ClientConnInterface) *Client {
	return &Client{c: pb.NewIAMServiceClient(conn)}
}

// Compile-time check that *Client satisfies Service.
var _ manager.Service = (*Client)(nil)

func errFrom(err error) error { return fromStatus(err, wireErrors) }

// ---- Users

func (c *Client) SignIn(ctx context.Context, id directory.Identity) (manager.SignInResult, error) {
	resp, err := c.c.SignIn(ctx, &pb.SignInRequest{Identity: identityToProto(id)})
	if err != nil {
		return manager.SignInResult{}, errFrom(err)
	}
	return manager.SignInResult{
		User: userFromProto(resp.GetUser()), Created: resp.GetCreated(), Invites: mapSlice(resp.GetInvites(), inviteFromProto),
	}, nil
}

func (c *Client) GetMe(ctx context.Context, actorID string) (manager.Me, error) {
	resp, err := c.c.GetMe(ctx, &pb.GetMeRequest{ActorId: actorID})
	if err != nil {
		return manager.Me{}, errFrom(err)
	}
	me := manager.Me{User: userFromProto(resp.GetUser())}
	if resp.GetMembership() != nil {
		m := memberFromProto(resp.GetMembership())
		me.Membership = &m
	}
	if resp.GetOrg() != nil {
		o := orgFromProto(resp.GetOrg())
		me.Org = &o
	}
	return me, nil
}

func (c *Client) DeleteAccount(ctx context.Context, actorID string) error {
	_, err := c.c.DeleteAccount(ctx, &pb.DeleteAccountRequest{ActorId: actorID})
	return errFromOrNil(err)
}

// errFromOrNil is errFrom for calls with nothing to return.
func errFromOrNil(err error) error {
	if err != nil {
		return errFrom(err)
	}
	return nil
}

// ---- Orgs and members

func (c *Client) CreateOrg(ctx context.Context, actorID, name string) (directory.Org, directory.Member, error) {
	resp, err := c.c.CreateOrg(ctx, &pb.CreateOrgRequest{ActorId: actorID, Name: name})
	if err != nil {
		return directory.Org{}, directory.Member{}, errFrom(err)
	}
	return orgFromProto(resp.GetOrg()), memberFromProto(resp.GetMember()), nil
}

func (c *Client) GetOrg(ctx context.Context, actorID, orgID string) (directory.Org, error) {
	resp, err := c.c.GetOrg(ctx, &pb.GetOrgRequest{ActorId: actorID, OrgId: orgID})
	if err != nil {
		return directory.Org{}, errFrom(err)
	}
	return orgFromProto(resp.GetOrg()), nil
}

func (c *Client) RenameOrg(ctx context.Context, actorID, orgID, name string) (directory.Org, error) {
	resp, err := c.c.RenameOrg(ctx, &pb.RenameOrgRequest{ActorId: actorID, OrgId: orgID, Name: name})
	if err != nil {
		return directory.Org{}, errFrom(err)
	}
	return orgFromProto(resp.GetOrg()), nil
}

func (c *Client) ListMembers(ctx context.Context, actorID, orgID string) ([]directory.Member, error) {
	resp, err := c.c.ListMembers(ctx, &pb.ListMembersRequest{ActorId: actorID, OrgId: orgID})
	if err != nil {
		return nil, errFrom(err)
	}
	return mapSlice(resp.GetMembers(), memberFromProto), nil
}

func (c *Client) SetRole(ctx context.Context, actorID, orgID, userID string, role directory.Role) (directory.Member, error) {
	resp, err := c.c.SetRole(ctx, &pb.SetRoleRequest{ActorId: actorID, OrgId: orgID, UserId: userID, Role: roleToProto[role]})
	if err != nil {
		return directory.Member{}, errFrom(err)
	}
	return memberFromProto(resp.GetMember()), nil
}

func (c *Client) RemoveMember(ctx context.Context, actorID, orgID, userID string) (int, error) {
	resp, err := c.c.RemoveMember(ctx, &pb.RemoveMemberRequest{ActorId: actorID, OrgId: orgID, UserId: userID})
	if err != nil {
		return 0, errFrom(err)
	}
	return int(resp.GetRemaining()), nil
}

func (c *Client) LeaveOrg(ctx context.Context, actorID string) (int, error) {
	resp, err := c.c.LeaveOrg(ctx, &pb.LeaveOrgRequest{ActorId: actorID})
	if err != nil {
		return 0, errFrom(err)
	}
	return int(resp.GetRemaining()), nil
}

func (c *Client) DeleteOrg(ctx context.Context, actorID, orgID string) error {
	_, err := c.c.DeleteOrg(ctx, &pb.DeleteOrgRequest{ActorId: actorID, OrgId: orgID})
	return errFromOrNil(err)
}

// ---- Invites

func (c *Client) InviteMember(ctx context.Context, actorID, orgID, email string, role directory.Role) (directory.Invite, error) {
	resp, err := c.c.InviteMember(ctx, &pb.InviteMemberRequest{ActorId: actorID, OrgId: orgID, Email: email, Role: roleToProto[role]})
	if err != nil {
		return directory.Invite{}, errFrom(err)
	}
	return inviteFromProto(resp.GetInvite()), nil
}

func (c *Client) ListOrgInvites(ctx context.Context, actorID, orgID string) ([]directory.Invite, error) {
	resp, err := c.c.ListOrgInvites(ctx, &pb.ListOrgInvitesRequest{ActorId: actorID, OrgId: orgID})
	if err != nil {
		return nil, errFrom(err)
	}
	return mapSlice(resp.GetInvites(), inviteFromProto), nil
}

func (c *Client) RevokeInvite(ctx context.Context, actorID, inviteID string) error {
	_, err := c.c.RevokeInvite(ctx, &pb.RevokeInviteRequest{ActorId: actorID, InviteId: inviteID})
	return errFromOrNil(err)
}

func (c *Client) ListMyInvites(ctx context.Context, actorID string) ([]directory.Invite, error) {
	resp, err := c.c.ListMyInvites(ctx, &pb.ListMyInvitesRequest{ActorId: actorID})
	if err != nil {
		return nil, errFrom(err)
	}
	return mapSlice(resp.GetInvites(), inviteFromProto), nil
}

func (c *Client) AcceptInvite(ctx context.Context, actorID, inviteID string) (directory.Member, error) {
	resp, err := c.c.AcceptInvite(ctx, &pb.AcceptInviteRequest{ActorId: actorID, InviteId: inviteID})
	if err != nil {
		return directory.Member{}, errFrom(err)
	}
	return memberFromProto(resp.GetMember()), nil
}

func (c *Client) DeclineInvite(ctx context.Context, actorID, inviteID string) error {
	_, err := c.c.DeclineInvite(ctx, &pb.DeclineInviteRequest{ActorId: actorID, InviteId: inviteID})
	return errFromOrNil(err)
}

// ---- Projects and sharing

func (c *Client) CreateProject(ctx context.Context, actorID string, owner storage.Owner, name string, orgLocked bool) (directory.Project, error) {
	resp, err := c.c.CreateProject(ctx, &pb.CreateProjectRequest{ActorId: actorID, Owner: ownerToProto(owner), Name: name, OrgLocked: orgLocked})
	if err != nil {
		return directory.Project{}, errFrom(err)
	}
	return projectFromProto(resp.GetProject()), nil
}

// CloneProject returns no project with an error: a status carries no result.
// The half-made clone, if any, is the cleanup loop's to finish or delete.
func (c *Client) CloneProject(ctx context.Context, actorID, sourceID string, target storage.Owner) (directory.Project, error) {
	resp, err := c.c.CloneProject(ctx, &pb.CloneProjectRequest{ActorId: actorID, SourceProjectId: sourceID, Target: ownerToProto(target)})
	if err != nil {
		return directory.Project{}, errFrom(err)
	}
	return projectFromProto(resp.GetProject()), nil
}

func (c *Client) GetProject(ctx context.Context, actorID, projectID string) (manager.ProjectView, error) {
	resp, err := c.c.GetProject(ctx, &pb.GetProjectRequest{ActorId: actorID, ProjectId: projectID})
	if err != nil {
		return manager.ProjectView{}, errFrom(err)
	}
	return manager.ProjectView{Project: projectFromProto(resp.GetProject()), Access: accessFromProto[resp.GetAccess()]}, nil
}

func (c *Client) ListMyProjects(ctx context.Context, actorID string, pageSize int, pageToken string) (directory.ProjectPage, error) {
	resp, err := c.c.ListMyProjects(ctx, &pb.ListMyProjectsRequest{ActorId: actorID, PageSize: int32(pageSize), PageToken: pageToken})
	if err != nil {
		return directory.ProjectPage{}, errFrom(err)
	}
	return directory.ProjectPage{Projects: mapSlice(resp.GetProjects(), projectFromProto), NextPageToken: resp.GetNextPageToken()}, nil
}

func (c *Client) ListTemplates(ctx context.Context, actorID string, pageSize int, pageToken string) (directory.ProjectPage, error) {
	resp, err := c.c.ListTemplates(ctx, &pb.ListTemplatesRequest{ActorId: actorID, PageSize: int32(pageSize), PageToken: pageToken})
	if err != nil {
		return directory.ProjectPage{}, errFrom(err)
	}
	return directory.ProjectPage{Projects: mapSlice(resp.GetProjects(), projectFromProto), NextPageToken: resp.GetNextPageToken()}, nil
}

func (c *Client) RenameProject(ctx context.Context, actorID, projectID, name string) (directory.Project, error) {
	resp, err := c.c.RenameProject(ctx, &pb.RenameProjectRequest{ActorId: actorID, ProjectId: projectID, Name: name})
	if err != nil {
		return directory.Project{}, errFrom(err)
	}
	return projectFromProto(resp.GetProject()), nil
}

func (c *Client) MoveProjectToOrg(ctx context.Context, actorID, projectID string) (directory.Project, error) {
	resp, err := c.c.MoveProjectToOrg(ctx, &pb.MoveProjectToOrgRequest{ActorId: actorID, ProjectId: projectID})
	if err != nil {
		return directory.Project{}, errFrom(err)
	}
	return projectFromProto(resp.GetProject()), nil
}

func (c *Client) SetOrgLocked(ctx context.Context, actorID, projectID string, locked bool) (directory.Project, error) {
	resp, err := c.c.SetOrgLocked(ctx, &pb.SetOrgLockedRequest{ActorId: actorID, ProjectId: projectID, Locked: locked})
	if err != nil {
		return directory.Project{}, errFrom(err)
	}
	return projectFromProto(resp.GetProject()), nil
}

func (c *Client) ShareProject(ctx context.Context, actorID, projectID, email string, access directory.Access) (directory.Share, error) {
	resp, err := c.c.ShareProject(ctx, &pb.ShareProjectRequest{ActorId: actorID, ProjectId: projectID, Email: email, Access: accessToProto[access]})
	if err != nil {
		return directory.Share{}, errFrom(err)
	}
	return shareFromProto(resp.GetShare()), nil
}

func (c *Client) ListShares(ctx context.Context, actorID, projectID string) ([]directory.Share, error) {
	resp, err := c.c.ListShares(ctx, &pb.ListSharesRequest{ActorId: actorID, ProjectId: projectID})
	if err != nil {
		return nil, errFrom(err)
	}
	return mapSlice(resp.GetShares(), shareFromProto), nil
}

func (c *Client) Unshare(ctx context.Context, actorID, projectID, userID string) error {
	_, err := c.c.Unshare(ctx, &pb.UnshareRequest{ActorId: actorID, ProjectId: projectID, UserId: userID})
	return errFromOrNil(err)
}

func (c *Client) RevokePendingShare(ctx context.Context, actorID, projectID, email string) error {
	_, err := c.c.RevokePendingShare(ctx, &pb.RevokePendingShareRequest{ActorId: actorID, ProjectId: projectID, Email: email})
	return errFromOrNil(err)
}

func (c *Client) DeleteProject(ctx context.Context, actorID, projectID string) error {
	_, err := c.c.DeleteProject(ctx, &pb.DeleteProjectRequest{ActorId: actorID, ProjectId: projectID})
	return errFromOrNil(err)
}

// ---- Templates

func (c *Client) CreateTemplate(ctx context.Context, name string) (directory.Project, error) {
	resp, err := c.c.CreateTemplate(ctx, &pb.CreateTemplateRequest{Name: name})
	if err != nil {
		return directory.Project{}, errFrom(err)
	}
	return projectFromProto(resp.GetProject()), nil
}

func (c *Client) DeleteTemplate(ctx context.Context, projectID string) error {
	_, err := c.c.DeleteTemplate(ctx, &pb.DeleteTemplateRequest{ProjectId: projectID})
	return errFromOrNil(err)
}

// ---- Authz

// AuthzClient is a storage.Authorizer asking a remote iam. Its refusals wrap
// storage.ErrForbidden, as the Authorizer contract asks; a failure to reach
// iam is returned as it is, so storage never mistakes an outage for "no".
type AuthzClient struct {
	c pb.AuthzServiceClient
}

func NewAuthzClient(conn grpc.ClientConnInterface) *AuthzClient {
	return &AuthzClient{c: pb.NewAuthzServiceClient(conn)}
}

var _ storage.Authorizer = (*AuthzClient)(nil)

func (a *AuthzClient) CanRead(ctx context.Context, actorID string, owner storage.Owner, projectID string) error {
	_, err := a.c.CanRead(ctx, &pb.CanReadRequest{ActorId: actorID, Owner: ownerToProto(owner), ProjectId: projectID})
	if err != nil {
		return fromStatus(err, authzWireErrors)
	}
	return nil
}

func (a *AuthzClient) CanWrite(ctx context.Context, actorID string, owner storage.Owner, projectID string) error {
	_, err := a.c.CanWrite(ctx, &pb.CanWriteRequest{ActorId: actorID, Owner: ownerToProto(owner), ProjectId: projectID})
	if err != nil {
		return fromStatus(err, authzWireErrors)
	}
	return nil
}

func (a *AuthzClient) ProjectOwner(ctx context.Context, projectID string) (storage.Owner, error) {
	resp, err := a.c.ProjectOwner(ctx, &pb.ProjectOwnerRequest{ProjectId: projectID})
	if err != nil {
		return storage.Owner{}, fromStatus(err, authzWireErrors)
	}
	return ownerFromProto(resp.GetOwner()), nil
}
