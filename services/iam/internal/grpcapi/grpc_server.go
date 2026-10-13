package grpcapi

import (
	"context"

	pb "gyoza/contracts/gen/go/gyoza/iam/v1"
	"gyoza/services/iam/internal/manager"
	"gyoza/services/storage"
)

// Server exposes a Service over gRPC. Register it with
// pb.RegisterIAMServiceServer.
type Server struct {
	// Embedded by value, as the generated code requires: an RPC added to the
	// proto but not yet here returns UNIMPLEMENTED instead of breaking the build.
	pb.UnimplementedIAMServiceServer
	svc manager.Service
}

func NewServer(svc manager.Service) *Server { return &Server{svc: svc} }

func errStatus(err error) error { return toStatus(err, wireErrors) }

// ---- Users

func (s *Server) SignIn(ctx context.Context, req *pb.SignInRequest) (*pb.SignInResponse, error) {
	r, err := s.svc.SignIn(ctx, identityFromProto(req.GetIdentity()))
	if err != nil {
		return nil, errStatus(err)
	}
	return &pb.SignInResponse{User: userToProto(r.User), Created: r.Created, Invites: mapSlice(r.Invites, inviteToProto)}, nil
}

func (s *Server) GetMe(ctx context.Context, req *pb.GetMeRequest) (*pb.GetMeResponse, error) {
	me, err := s.svc.GetMe(ctx, req.GetActorId())
	if err != nil {
		return nil, errStatus(err)
	}
	resp := &pb.GetMeResponse{User: userToProto(me.User)}
	if me.Membership != nil {
		resp.Membership = memberToProto(*me.Membership)
	}
	if me.Org != nil {
		resp.Org = orgToProto(*me.Org)
	}
	return resp, nil
}

func (s *Server) DeleteAccount(ctx context.Context, req *pb.DeleteAccountRequest) (*pb.DeleteAccountResponse, error) {
	if err := s.svc.DeleteAccount(ctx, req.GetActorId()); err != nil {
		return nil, errStatus(err)
	}
	return &pb.DeleteAccountResponse{}, nil
}

// ---- Orgs and members

func (s *Server) CreateOrg(ctx context.Context, req *pb.CreateOrgRequest) (*pb.CreateOrgResponse, error) {
	o, m, err := s.svc.CreateOrg(ctx, req.GetActorId(), req.GetName())
	if err != nil {
		return nil, errStatus(err)
	}
	return &pb.CreateOrgResponse{Org: orgToProto(o), Member: memberToProto(m)}, nil
}

func (s *Server) GetOrg(ctx context.Context, req *pb.GetOrgRequest) (*pb.GetOrgResponse, error) {
	o, err := s.svc.GetOrg(ctx, req.GetActorId(), req.GetOrgId())
	if err != nil {
		return nil, errStatus(err)
	}
	return &pb.GetOrgResponse{Org: orgToProto(o)}, nil
}

func (s *Server) RenameOrg(ctx context.Context, req *pb.RenameOrgRequest) (*pb.RenameOrgResponse, error) {
	o, err := s.svc.RenameOrg(ctx, req.GetActorId(), req.GetOrgId(), req.GetName())
	if err != nil {
		return nil, errStatus(err)
	}
	return &pb.RenameOrgResponse{Org: orgToProto(o)}, nil
}

func (s *Server) ListMembers(ctx context.Context, req *pb.ListMembersRequest) (*pb.ListMembersResponse, error) {
	ms, err := s.svc.ListMembers(ctx, req.GetActorId(), req.GetOrgId())
	if err != nil {
		return nil, errStatus(err)
	}
	return &pb.ListMembersResponse{Members: mapSlice(ms, memberToProto)}, nil
}

func (s *Server) SetRole(ctx context.Context, req *pb.SetRoleRequest) (*pb.SetRoleResponse, error) {
	m, err := s.svc.SetRole(ctx, req.GetActorId(), req.GetOrgId(), req.GetUserId(), roleFromProto[req.GetRole()])
	if err != nil {
		return nil, errStatus(err)
	}
	return &pb.SetRoleResponse{Member: memberToProto(m)}, nil
}

func (s *Server) RemoveMember(ctx context.Context, req *pb.RemoveMemberRequest) (*pb.RemoveMemberResponse, error) {
	n, err := s.svc.RemoveMember(ctx, req.GetActorId(), req.GetOrgId(), req.GetUserId())
	if err != nil {
		return nil, errStatus(err)
	}
	return &pb.RemoveMemberResponse{Remaining: int32(n)}, nil
}

func (s *Server) LeaveOrg(ctx context.Context, req *pb.LeaveOrgRequest) (*pb.LeaveOrgResponse, error) {
	n, err := s.svc.LeaveOrg(ctx, req.GetActorId())
	if err != nil {
		return nil, errStatus(err)
	}
	return &pb.LeaveOrgResponse{Remaining: int32(n)}, nil
}

func (s *Server) DeleteOrg(ctx context.Context, req *pb.DeleteOrgRequest) (*pb.DeleteOrgResponse, error) {
	if err := s.svc.DeleteOrg(ctx, req.GetActorId(), req.GetOrgId()); err != nil {
		return nil, errStatus(err)
	}
	return &pb.DeleteOrgResponse{}, nil
}

// ---- Invites

func (s *Server) InviteMember(ctx context.Context, req *pb.InviteMemberRequest) (*pb.InviteMemberResponse, error) {
	inv, err := s.svc.InviteMember(ctx, req.GetActorId(), req.GetOrgId(), req.GetEmail(), roleFromProto[req.GetRole()])
	if err != nil {
		return nil, errStatus(err)
	}
	return &pb.InviteMemberResponse{Invite: inviteToProto(inv)}, nil
}

func (s *Server) ListOrgInvites(ctx context.Context, req *pb.ListOrgInvitesRequest) (*pb.ListOrgInvitesResponse, error) {
	invs, err := s.svc.ListOrgInvites(ctx, req.GetActorId(), req.GetOrgId())
	if err != nil {
		return nil, errStatus(err)
	}
	return &pb.ListOrgInvitesResponse{Invites: mapSlice(invs, inviteToProto)}, nil
}

func (s *Server) RevokeInvite(ctx context.Context, req *pb.RevokeInviteRequest) (*pb.RevokeInviteResponse, error) {
	if err := s.svc.RevokeInvite(ctx, req.GetActorId(), req.GetInviteId()); err != nil {
		return nil, errStatus(err)
	}
	return &pb.RevokeInviteResponse{}, nil
}

func (s *Server) ListMyInvites(ctx context.Context, req *pb.ListMyInvitesRequest) (*pb.ListMyInvitesResponse, error) {
	invs, err := s.svc.ListMyInvites(ctx, req.GetActorId())
	if err != nil {
		return nil, errStatus(err)
	}
	return &pb.ListMyInvitesResponse{Invites: mapSlice(invs, inviteToProto)}, nil
}

func (s *Server) AcceptInvite(ctx context.Context, req *pb.AcceptInviteRequest) (*pb.AcceptInviteResponse, error) {
	m, err := s.svc.AcceptInvite(ctx, req.GetActorId(), req.GetInviteId())
	if err != nil {
		return nil, errStatus(err)
	}
	return &pb.AcceptInviteResponse{Member: memberToProto(m)}, nil
}

func (s *Server) DeclineInvite(ctx context.Context, req *pb.DeclineInviteRequest) (*pb.DeclineInviteResponse, error) {
	if err := s.svc.DeclineInvite(ctx, req.GetActorId(), req.GetInviteId()); err != nil {
		return nil, errStatus(err)
	}
	return &pb.DeclineInviteResponse{}, nil
}

// ---- Projects and sharing

func (s *Server) CreateProject(ctx context.Context, req *pb.CreateProjectRequest) (*pb.CreateProjectResponse, error) {
	p, err := s.svc.CreateProject(ctx, req.GetActorId(), ownerFromProto(req.GetOwner()), req.GetName(), req.GetOrgLocked())
	if err != nil {
		return nil, errStatus(err)
	}
	return &pb.CreateProjectResponse{Project: projectToProto(p)}, nil
}

func (s *Server) CloneProject(ctx context.Context, req *pb.CloneProjectRequest) (*pb.CloneProjectResponse, error) {
	p, err := s.svc.CloneProject(ctx, req.GetActorId(), req.GetSourceProjectId(), ownerFromProto(req.GetTarget()))
	if err != nil {
		return nil, errStatus(err)
	}
	return &pb.CloneProjectResponse{Project: projectToProto(p)}, nil
}

func (s *Server) GetProject(ctx context.Context, req *pb.GetProjectRequest) (*pb.GetProjectResponse, error) {
	v, err := s.svc.GetProject(ctx, req.GetActorId(), req.GetProjectId())
	if err != nil {
		return nil, errStatus(err)
	}
	return &pb.GetProjectResponse{Project: projectToProto(v.Project), Access: accessToProto[v.Access]}, nil
}

func (s *Server) ListMyProjects(ctx context.Context, req *pb.ListMyProjectsRequest) (*pb.ListMyProjectsResponse, error) {
	page, err := s.svc.ListMyProjects(ctx, req.GetActorId(), int(req.GetPageSize()), req.GetPageToken())
	if err != nil {
		return nil, errStatus(err)
	}
	return &pb.ListMyProjectsResponse{Projects: mapSlice(page.Projects, projectToProto), NextPageToken: page.NextPageToken}, nil
}

func (s *Server) ListTemplates(ctx context.Context, req *pb.ListTemplatesRequest) (*pb.ListTemplatesResponse, error) {
	page, err := s.svc.ListTemplates(ctx, req.GetActorId(), int(req.GetPageSize()), req.GetPageToken())
	if err != nil {
		return nil, errStatus(err)
	}
	return &pb.ListTemplatesResponse{Projects: mapSlice(page.Projects, projectToProto), NextPageToken: page.NextPageToken}, nil
}

func (s *Server) RenameProject(ctx context.Context, req *pb.RenameProjectRequest) (*pb.RenameProjectResponse, error) {
	p, err := s.svc.RenameProject(ctx, req.GetActorId(), req.GetProjectId(), req.GetName())
	if err != nil {
		return nil, errStatus(err)
	}
	return &pb.RenameProjectResponse{Project: projectToProto(p)}, nil
}

func (s *Server) MoveProjectToOrg(ctx context.Context, req *pb.MoveProjectToOrgRequest) (*pb.MoveProjectToOrgResponse, error) {
	p, err := s.svc.MoveProjectToOrg(ctx, req.GetActorId(), req.GetProjectId())
	if err != nil {
		return nil, errStatus(err)
	}
	return &pb.MoveProjectToOrgResponse{Project: projectToProto(p)}, nil
}

func (s *Server) SetOrgLocked(ctx context.Context, req *pb.SetOrgLockedRequest) (*pb.SetOrgLockedResponse, error) {
	p, err := s.svc.SetOrgLocked(ctx, req.GetActorId(), req.GetProjectId(), req.GetLocked())
	if err != nil {
		return nil, errStatus(err)
	}
	return &pb.SetOrgLockedResponse{Project: projectToProto(p)}, nil
}

func (s *Server) ShareProject(ctx context.Context, req *pb.ShareProjectRequest) (*pb.ShareProjectResponse, error) {
	sh, err := s.svc.ShareProject(ctx, req.GetActorId(), req.GetProjectId(), req.GetEmail(), accessFromProto[req.GetAccess()])
	if err != nil {
		return nil, errStatus(err)
	}
	return &pb.ShareProjectResponse{Share: shareToProto(sh)}, nil
}

func (s *Server) ListShares(ctx context.Context, req *pb.ListSharesRequest) (*pb.ListSharesResponse, error) {
	shares, err := s.svc.ListShares(ctx, req.GetActorId(), req.GetProjectId())
	if err != nil {
		return nil, errStatus(err)
	}
	return &pb.ListSharesResponse{Shares: mapSlice(shares, shareToProto)}, nil
}

func (s *Server) Unshare(ctx context.Context, req *pb.UnshareRequest) (*pb.UnshareResponse, error) {
	if err := s.svc.Unshare(ctx, req.GetActorId(), req.GetProjectId(), req.GetUserId()); err != nil {
		return nil, errStatus(err)
	}
	return &pb.UnshareResponse{}, nil
}

func (s *Server) DeleteProject(ctx context.Context, req *pb.DeleteProjectRequest) (*pb.DeleteProjectResponse, error) {
	if err := s.svc.DeleteProject(ctx, req.GetActorId(), req.GetProjectId()); err != nil {
		return nil, errStatus(err)
	}
	return &pb.DeleteProjectResponse{}, nil
}

// ---- Templates

func (s *Server) CreateTemplate(ctx context.Context, req *pb.CreateTemplateRequest) (*pb.CreateTemplateResponse, error) {
	p, err := s.svc.CreateTemplate(ctx, req.GetName())
	if err != nil {
		return nil, errStatus(err)
	}
	return &pb.CreateTemplateResponse{Project: projectToProto(p)}, nil
}

func (s *Server) DeleteTemplate(ctx context.Context, req *pb.DeleteTemplateRequest) (*pb.DeleteTemplateResponse, error) {
	if err := s.svc.DeleteTemplate(ctx, req.GetProjectId()); err != nil {
		return nil, errStatus(err)
	}
	return &pb.DeleteTemplateResponse{}, nil
}

// ---- Authz

// AuthzServer exposes a storage.Authorizer (iam's StorageAuthorizer) over
// gRPC, for storage. Register it with pb.RegisterAuthzServiceServer.
type AuthzServer struct {
	pb.UnimplementedAuthzServiceServer
	authz storage.Authorizer
}

func NewAuthzServer(a storage.Authorizer) *AuthzServer { return &AuthzServer{authz: a} }

func (s *AuthzServer) CanRead(ctx context.Context, req *pb.CanReadRequest) (*pb.CanReadResponse, error) {
	if err := s.authz.CanRead(ctx, req.GetActorId(), ownerFromProto(req.GetOwner()), req.GetProjectId()); err != nil {
		return nil, toStatus(err, authzWireErrors)
	}
	return &pb.CanReadResponse{}, nil
}

func (s *AuthzServer) CanWrite(ctx context.Context, req *pb.CanWriteRequest) (*pb.CanWriteResponse, error) {
	if err := s.authz.CanWrite(ctx, req.GetActorId(), ownerFromProto(req.GetOwner()), req.GetProjectId()); err != nil {
		return nil, toStatus(err, authzWireErrors)
	}
	return &pb.CanWriteResponse{}, nil
}

func (s *AuthzServer) ProjectOwner(ctx context.Context, req *pb.ProjectOwnerRequest) (*pb.ProjectOwnerResponse, error) {
	o, err := s.authz.ProjectOwner(ctx, req.GetProjectId())
	if err != nil {
		return nil, toStatus(err, authzWireErrors)
	}
	return &pb.ProjectOwnerResponse{Owner: ownerToProto(o)}, nil
}
