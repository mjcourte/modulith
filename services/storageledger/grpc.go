package storageledger

// Conversions between the domain types and the protobuf messages, and between
// domain errors and gRPC status codes. Shared by the gRPC server and client.

import (
	"errors"
	"log/slog"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	pb "gyoza/contracts/gen/go/gyoza/storageledger/v1"
)

var ownerKindToProto = map[OwnerKind]pb.OwnerKind{
	OwnerProject: pb.OwnerKind_OWNER_KIND_PROJECT,
	OwnerUser:    pb.OwnerKind_OWNER_KIND_USER,
	OwnerOrg:     pb.OwnerKind_OWNER_KIND_ORG,
	OwnerGyoza:   pb.OwnerKind_OWNER_KIND_GYOZA,
}

var ownerKindFromProto = map[pb.OwnerKind]OwnerKind{
	pb.OwnerKind_OWNER_KIND_PROJECT: OwnerProject,
	pb.OwnerKind_OWNER_KIND_USER:    OwnerUser,
	pb.OwnerKind_OWNER_KIND_ORG:     OwnerOrg,
	pb.OwnerKind_OWNER_KIND_GYOZA:   OwnerGyoza,
}

// Unknown kinds map to the zero value on both sides (missing map keys return
// zero), and the server's validation rejects them.

func ownerToProto(o Owner) *pb.Owner {
	return &pb.Owner{Kind: ownerKindToProto[o.Kind], Id: o.ID}
}

func ownerFromProto(o *pb.Owner) Owner {
	if o == nil {
		return Owner{}
	}
	return Owner{Kind: ownerKindFromProto[o.GetKind()], ID: o.GetId()}
}

func objectToProto(o Object) *pb.Object {
	p := &pb.Object{
		Id:          o.ID,
		Owner:       ownerToProto(o.Owner),
		CreatedBy:   o.CreatedBy,
		ProjectId:   o.ProjectID,
		Kind:        string(o.Kind),
		Bucket:      o.Bucket,
		Key:         o.Key,
		SizeBytes:   o.SizeBytes,
		ContentType: o.ContentType,
	}
	if !o.CreatedAt.IsZero() {
		p.CreatedAt = timestamppb.New(o.CreatedAt)
	}
	if o.DeletedAt != nil {
		p.DeletedAt = timestamppb.New(*o.DeletedAt)
	}
	return p
}

// objectFromProto accepts nil, since getters on a missing message field return nil.
func objectFromProto(p *pb.Object) Object {
	if p == nil {
		return Object{}
	}
	o := Object{
		ID:          p.GetId(),
		Owner:       ownerFromProto(p.GetOwner()),
		CreatedBy:   p.GetCreatedBy(),
		ProjectID:   p.GetProjectId(),
		Kind:        Kind(p.GetKind()),
		Bucket:      p.GetBucket(),
		Key:         p.GetKey(),
		SizeBytes:   p.GetSizeBytes(),
		ContentType: p.GetContentType(),
	}
	if p.GetCreatedAt() != nil {
		o.CreatedAt = p.GetCreatedAt().AsTime() // AsTime returns UTC
	}
	if p.GetDeletedAt() != nil {
		t := p.GetDeletedAt().AsTime()
		o.DeletedAt = &t
	}
	return o
}

// toStatus turns a Service error into a gRPC status for the server to return.
// Unexpected errors are logged here and reach the caller only as "internal
// error", so database details never leak across the API.
func toStatus(err error) error {
	switch {
	case errors.Is(err, ErrNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, ErrInvalidQuery), errors.Is(err, ErrInvalidObject):
		return status.Error(codes.InvalidArgument, err.Error())
	default:
		slog.Error("storageledger: internal error", "err", err)
		return status.Error(codes.Internal, "internal error")
	}
}

// remoteError carries the server's message while still matching the domain
// sentinel with errors.Is, so callers of the client handle errors exactly as
// they would with an in-process Service.
type remoteError struct {
	sentinel error
	msg      string
}

func (e *remoteError) Error() string { return e.msg }
func (e *remoteError) Unwrap() error { return e.sentinel }

// fromStatus turns a gRPC error back into a domain error. invalid is the
// sentinel the method uses for bad input: ErrInvalidObject for Create,
// ErrInvalidQuery otherwise.
func fromStatus(err error, invalid error) error {
	st, ok := status.FromError(err)
	if !ok {
		return err
	}
	switch st.Code() {
	case codes.NotFound:
		return &remoteError{sentinel: ErrNotFound, msg: st.Message()}
	case codes.InvalidArgument:
		return &remoteError{sentinel: invalid, msg: st.Message()}
	default:
		return err
	}
}
