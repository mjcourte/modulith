// Package grpcapi serves iam over gRPC (Server, AuthzServer) and calls a
// remote one (Client, AuthzClient). The clients return the same domain types
// and errors as in-process, so callers can't tell them apart.
//
// This file has what both directions share: the error table and the
// conversions between domain types and protobuf messages.
package grpcapi

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	pb "gyoza/contracts/gen/go/gyoza/iam/v1"
	storagepb "gyoza/contracts/gen/go/gyoza/storage/v1"
	"gyoza/services/iam/internal/directory"
	"gyoza/services/iam/internal/manager"
	"gyoza/services/storage"
)

// ---- Errors

// errorDomain identifies this service in google.rpc.ErrorInfo details.
const errorDomain = "iam.gyoza"

type wireError struct {
	err    error
	code   codes.Code
	reason string
}

// wireErrors lists every sentinel that may cross IAMService, with its status
// code and its stable name on the wire (ErrorInfo's reason), which tells the
// client which one it was when several share a code. Both directions use
// this one table, so they can't disagree.
var wireErrors = []wireError{
	{manager.ErrForbidden, codes.PermissionDenied, "FORBIDDEN"},
	{directory.ErrUserNotFound, codes.NotFound, "USER_NOT_FOUND"},
	{directory.ErrOrgNotFound, codes.NotFound, "ORG_NOT_FOUND"},
	{directory.ErrInviteNotFound, codes.NotFound, "INVITE_NOT_FOUND"},
	{directory.ErrProjectNotFound, codes.NotFound, "PROJECT_NOT_FOUND"},
	{directory.ErrShareNotFound, codes.NotFound, "SHARE_NOT_FOUND"},
	{directory.ErrInvalidUser, codes.InvalidArgument, "INVALID_USER"},
	{directory.ErrInvalidOrg, codes.InvalidArgument, "INVALID_ORG"},
	{directory.ErrInvalidInvite, codes.InvalidArgument, "INVALID_INVITE"},
	{directory.ErrInvalidProject, codes.InvalidArgument, "INVALID_PROJECT"},
	{directory.ErrInvalidShare, codes.InvalidArgument, "INVALID_SHARE"},
	{directory.ErrInvalidQuery, codes.InvalidArgument, "INVALID_QUERY"},
	{directory.ErrEmailTaken, codes.AlreadyExists, "EMAIL_TAKEN"},
	{directory.ErrAlreadyMember, codes.AlreadyExists, "ALREADY_MEMBER"},
	{directory.ErrNotMember, codes.FailedPrecondition, "NOT_MEMBER"},
	{directory.ErrLastAdmin, codes.FailedPrecondition, "LAST_ADMIN"},
	{directory.ErrInviteExpired, codes.FailedPrecondition, "INVITE_EXPIRED"},
	{directory.ErrNotDeleting, codes.FailedPrecondition, "NOT_DELETING"},
}

// authzWireErrors is AuthzService's table: its refusals are storage's
// ErrForbidden, which is what storage's Authorizer contract asks for.
var authzWireErrors = []wireError{
	{storage.ErrForbidden, codes.PermissionDenied, "FORBIDDEN"},
}

// toStatus turns a domain error into a gRPC status for the server to return.
// An error can match several sentinels: each gets an ErrorInfo detail, and
// the first decides the code. Unexpected errors are logged here and reach
// the caller only as "internal error", so database details never leak.
func toStatus(err error, table []wireError) error {
	var details []*errdetails.ErrorInfo
	code := codes.Unknown
	for _, w := range table {
		if !errors.Is(err, w.err) {
			continue
		}
		if len(details) == 0 {
			code = w.code
		}
		details = append(details, &errdetails.ErrorInfo{Domain: errorDomain, Reason: w.reason})
	}
	switch {
	case len(details) > 0:
		st := status.New(code, err.Error())
		for _, d := range details {
			if withDetail, err := st.WithDetails(d); err == nil {
				st = withDetail
			}
		}
		return st.Err()
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, err.Error())
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, err.Error())
	default:
		slog.Error("iam: internal error", "err", err)
		return status.Error(codes.Internal, "internal error")
	}
}

// remoteError is a server error on the client side. It keeps the server's
// message and matches the same sentinels with errors.Is as the original did.
type remoteError struct {
	msg  string
	errs []error
}

func (e *remoteError) Error() string { return e.msg }

// Unwrap returning a slice lets errors.Is check every error in it.
func (e *remoteError) Unwrap() []error { return e.errs }

// fromStatus turns a gRPC error back into a domain error, using table.
func fromStatus(err error, table []wireError) error {
	st, ok := status.FromError(err)
	if !ok {
		return err // not from gRPC
	}
	var errs []error
	for _, d := range st.Details() {
		info, ok := d.(*errdetails.ErrorInfo)
		if !ok || info.GetDomain() != errorDomain {
			continue
		}
		for _, w := range table {
			if w.reason == info.GetReason() {
				errs = append(errs, w.err)
			}
		}
	}
	switch {
	case len(errs) > 0:
		return &remoteError{msg: st.Message(), errs: errs}
	case st.Code() == codes.Canceled:
		return &remoteError{msg: st.Message(), errs: []error{context.Canceled, err}}
	case st.Code() == codes.DeadlineExceeded:
		return &remoteError{msg: st.Message(), errs: []error{context.DeadlineExceeded, err}}
	default:
		return err
	}
}

// ---- Times

// timestamp converts t, leaving the zero time unset.
func timestamp(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() {
		return nil
	}
	return timestamppb.New(t)
}

func optTimestamp(t *time.Time) *timestamppb.Timestamp {
	if t == nil {
		return nil
	}
	return timestamppb.New(*t)
}

// timeOf converts back, in UTC like the stores; unset is the zero time.
func timeOf(p *timestamppb.Timestamp) time.Time {
	if p == nil {
		return time.Time{}
	}
	return p.AsTime().UTC()
}

func optTimeOf(p *timestamppb.Timestamp) *time.Time {
	if p == nil {
		return nil
	}
	t := p.AsTime().UTC()
	return &t
}

// ---- Enums. Unknown values map to the zero value both ways (a missing map
// key returns zero), and the server's validation rejects them.

var roleToProto = map[directory.Role]pb.Role{
	directory.RoleAdmin:  pb.Role_ROLE_ADMIN,
	directory.RoleMember: pb.Role_ROLE_MEMBER,
}

var roleFromProto = map[pb.Role]directory.Role{
	pb.Role_ROLE_ADMIN:  directory.RoleAdmin,
	pb.Role_ROLE_MEMBER: directory.RoleMember,
}

var accessToProto = map[directory.Access]pb.Access{
	directory.AccessViewer: pb.Access_ACCESS_VIEWER,
	directory.AccessEditor: pb.Access_ACCESS_EDITOR,
}

var accessFromProto = map[pb.Access]directory.Access{
	pb.Access_ACCESS_VIEWER: directory.AccessViewer,
	pb.Access_ACCESS_EDITOR: directory.AccessEditor,
}

var stateToProto = map[directory.ProjectState]pb.ProjectState{
	directory.ProjectReady:   pb.ProjectState_PROJECT_STATE_READY,
	directory.ProjectCloning: pb.ProjectState_PROJECT_STATE_CLONING,
}

var stateFromProto = map[pb.ProjectState]directory.ProjectState{
	pb.ProjectState_PROJECT_STATE_READY:   directory.ProjectReady,
	pb.ProjectState_PROJECT_STATE_CLONING: directory.ProjectCloning,
}

var ownerKindToProto = map[storage.OwnerKind]storagepb.OwnerKind{
	storage.OwnerProject: storagepb.OwnerKind_OWNER_KIND_PROJECT,
	storage.OwnerUser:    storagepb.OwnerKind_OWNER_KIND_USER,
	storage.OwnerOrg:     storagepb.OwnerKind_OWNER_KIND_ORG,
	storage.OwnerGyoza:   storagepb.OwnerKind_OWNER_KIND_GYOZA,
}

var ownerKindFromProto = map[storagepb.OwnerKind]storage.OwnerKind{
	storagepb.OwnerKind_OWNER_KIND_PROJECT: storage.OwnerProject,
	storagepb.OwnerKind_OWNER_KIND_USER:    storage.OwnerUser,
	storagepb.OwnerKind_OWNER_KIND_ORG:     storage.OwnerOrg,
	storagepb.OwnerKind_OWNER_KIND_GYOZA:   storage.OwnerGyoza,
}

func ownerToProto(o storage.Owner) *storagepb.Owner {
	return &storagepb.Owner{Kind: ownerKindToProto[o.Kind], Id: o.ID}
}

func ownerFromProto(o *storagepb.Owner) storage.Owner {
	if o == nil {
		return storage.Owner{}
	}
	return storage.Owner{Kind: ownerKindFromProto[o.GetKind()], ID: o.GetId()}
}

// ---- Messages

func userToProto(u directory.User) *pb.User {
	return &pb.User{
		Id: u.ID, Email: u.Email, EmailVerified: u.EmailVerified, Name: u.Name, ProviderAvatarUrl: u.ProviderAvatarURL,
		CreatedAt: timestamp(u.CreatedAt), UpdatedAt: timestamp(u.UpdatedAt),
		DeletingAt: optTimestamp(u.DeletingAt), DeletingToOrg: u.DeletingToOrg,
	}
}

func userFromProto(p *pb.User) directory.User {
	return directory.User{
		ID: p.GetId(), Email: p.GetEmail(), EmailVerified: p.GetEmailVerified(), Name: p.GetName(),
		ProviderAvatarURL: p.GetProviderAvatarUrl(), CreatedAt: timeOf(p.GetCreatedAt()), UpdatedAt: timeOf(p.GetUpdatedAt()),
		DeletingAt: optTimeOf(p.GetDeletingAt()), DeletingToOrg: p.GetDeletingToOrg(),
	}
}

func identityToProto(id directory.Identity) *pb.Identity {
	return &pb.Identity{
		UserId: id.UserID, Email: id.Email, EmailVerified: id.EmailVerified, Name: id.Name, ProviderAvatarUrl: id.ProviderAvatarURL,
	}
}

func identityFromProto(p *pb.Identity) directory.Identity {
	return directory.Identity{
		UserID: p.GetUserId(), Email: p.GetEmail(), EmailVerified: p.GetEmailVerified(), Name: p.GetName(),
		ProviderAvatarURL: p.GetProviderAvatarUrl(),
	}
}

func orgToProto(o directory.Org) *pb.Org {
	return &pb.Org{
		Id: o.ID, Name: o.Name, CreatedAt: timestamp(o.CreatedAt), UpdatedAt: timestamp(o.UpdatedAt),
		DeletingAt: optTimestamp(o.DeletingAt),
	}
}

func orgFromProto(p *pb.Org) directory.Org {
	return directory.Org{
		ID: p.GetId(), Name: p.GetName(), CreatedAt: timeOf(p.GetCreatedAt()), UpdatedAt: timeOf(p.GetUpdatedAt()),
		DeletingAt: optTimeOf(p.GetDeletingAt()),
	}
}

func memberToProto(m directory.Member) *pb.Member {
	return &pb.Member{OrgId: m.OrgID, UserId: m.UserID, Role: roleToProto[m.Role], JoinedAt: timestamp(m.JoinedAt)}
}

func memberFromProto(p *pb.Member) directory.Member {
	return directory.Member{OrgID: p.GetOrgId(), UserID: p.GetUserId(), Role: roleFromProto[p.GetRole()], JoinedAt: timeOf(p.GetJoinedAt())}
}

func inviteToProto(i directory.Invite) *pb.Invite {
	return &pb.Invite{
		Id: i.ID, OrgId: i.OrgID, Email: i.Email, Role: roleToProto[i.Role], InvitedBy: i.InvitedBy,
		CreatedAt: timestamp(i.CreatedAt), ExpiresAt: timestamp(i.ExpiresAt),
	}
}

func inviteFromProto(p *pb.Invite) directory.Invite {
	return directory.Invite{
		ID: p.GetId(), OrgID: p.GetOrgId(), Email: p.GetEmail(), Role: roleFromProto[p.GetRole()], InvitedBy: p.GetInvitedBy(),
		CreatedAt: timeOf(p.GetCreatedAt()), ExpiresAt: timeOf(p.GetExpiresAt()),
	}
}

func projectToProto(p directory.Project) *pb.Project {
	return &pb.Project{
		Id: p.ID, Owner: ownerToProto(p.Owner), Name: p.Name, State: stateToProto[p.State], ClonedFrom: p.ClonedFrom,
		OrgLocked: p.OrgLocked, CreatedBy: p.CreatedBy, CreatedAt: timestamp(p.CreatedAt), UpdatedAt: timestamp(p.UpdatedAt),
		DeletingAt: optTimestamp(p.DeletingAt),
	}
}

func projectFromProto(p *pb.Project) directory.Project {
	return directory.Project{
		ID: p.GetId(), Owner: ownerFromProto(p.GetOwner()), Name: p.GetName(), State: stateFromProto[p.GetState()],
		ClonedFrom: p.GetClonedFrom(), OrgLocked: p.GetOrgLocked(), CreatedBy: p.GetCreatedBy(),
		CreatedAt: timeOf(p.GetCreatedAt()), UpdatedAt: timeOf(p.GetUpdatedAt()), DeletingAt: optTimeOf(p.GetDeletingAt()),
	}
}

func shareToProto(s directory.Share) *pb.Share {
	return &pb.Share{
		ProjectId: s.ProjectID, UserId: s.UserID, Access: accessToProto[s.Access], SharedBy: s.SharedBy,
		CreatedAt: timestamp(s.CreatedAt), UpdatedAt: timestamp(s.UpdatedAt),
	}
}

func shareFromProto(p *pb.Share) directory.Share {
	return directory.Share{
		ProjectID: p.GetProjectId(), UserID: p.GetUserId(), Access: accessFromProto[p.GetAccess()], SharedBy: p.GetSharedBy(),
		CreatedAt: timeOf(p.GetCreatedAt()), UpdatedAt: timeOf(p.GetUpdatedAt()),
	}
}

// mapSlice converts every element of in with f; nil stays nil, so an empty
// list round-trips as the stores return it.
func mapSlice[T, U any](in []T, f func(T) U) []U {
	if in == nil {
		return nil
	}
	out := make([]U, len(in))
	for i, v := range in {
		out[i] = f(v)
	}
	return out
}
