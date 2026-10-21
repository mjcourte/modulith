package grpcapi

// Conversions between the domain types and the protobuf messages, and between
// domain errors and gRPC statuses. Shared by the server and the client.

import (
	"context"
	"errors"
	"log/slog"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/mjcourte/modulith/contracts/gen/go/modulith/documents/v1"
	"github.com/mjcourte/modulith/internal/engine"
	"github.com/mjcourte/modulith/services/documents/internal/manager"
	"github.com/mjcourte/modulith/services/documents/internal/snapshots"
)

// errorDomain identifies this service in google.rpc.ErrorInfo details.
const errorDomain = "documents.modulith"

// wireErrors lists every sentinel that may cross the API, with its status
// code and stable name. One table drives both directions.
var wireErrors = []struct {
	err    error
	code   codes.Code
	reason string
}{
	{manager.ErrForbidden, codes.PermissionDenied, "FORBIDDEN"},
	{manager.ErrExists, codes.AlreadyExists, "EXISTS"},
	{engine.ErrUnsupportedFormat, codes.InvalidArgument, "UNSUPPORTED_FORMAT"},
	{engine.ErrBadStamp, codes.InvalidArgument, "BAD_STAMP"},
	{snapshots.ErrNotFound, codes.NotFound, "SNAPSHOT_NOT_FOUND"},
}

// toStatus turns a Service error into a gRPC status. Unexpected errors are
// logged here and reach the caller only as "internal error".
func toStatus(err error) error {
	var details []*errdetails.ErrorInfo
	code := codes.Unknown
	for _, w := range wireErrors {
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
		slog.Error("documents: internal error", "err", err)
		return status.Error(codes.Internal, "internal error")
	}
}

// remoteError is a server error on the client side: the server's message,
// matching the same sentinels with errors.Is as the original did.
type remoteError struct {
	msg  string
	errs []error
}

func (e *remoteError) Error() string   { return e.msg }
func (e *remoteError) Unwrap() []error { return e.errs }

// fromStatus turns a gRPC error back into a domain error.
func fromStatus(err error) error {
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
		for _, w := range wireErrors {
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
	}
	return err
}

func wireToProto(format engine.WireFormat, version int, payload []byte) *pb.Wire {
	return &pb.Wire{Format: string(format), Version: int32(version), Payload: payload}
}

func documentToProto(d manager.Document) *pb.Document {
	return &pb.Document{Snapshot: wireToProto(d.Snapshot.Format, d.Snapshot.Version, d.Snapshot.Payload), Seq: d.Seq}
}

func documentFromProto(p *pb.Document) manager.Document {
	w := p.GetSnapshot()
	return manager.Document{
		Snapshot: engine.Snapshot{Format: engine.WireFormat(w.GetFormat()), Version: int(w.GetVersion()), Payload: w.GetPayload()},
		Seq:      p.GetSeq(),
	}
}

func refToProto(r engine.AssetRef) *pb.AssetRef {
	return &pb.AssetRef{AssetId: r.ID, Version: int32(r.Version)}
}

func refFromProto(p *pb.AssetRef) engine.AssetRef {
	return engine.AssetRef{ID: p.GetAssetId(), Version: int(p.GetVersion())}
}
