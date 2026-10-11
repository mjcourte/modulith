package storageledger

import (
	"context"

	pb "gyoza/contracts/gen/go/gyoza/storageledger/v1"
)

// GRPCServer exposes a Service over gRPC. Register it with
// pb.RegisterLedgerServiceServer.
type GRPCServer struct {
	// Embedding the generated Unimplemented type (by value, as the generated
	// code requires) means an RPC added to the proto but not yet here returns
	// UNIMPLEMENTED instead of breaking the build.
	pb.UnimplementedLedgerServiceServer
	svc Service
}

func NewGRPCServer(svc Service) *GRPCServer {
	return &GRPCServer{svc: svc}
}

func (s *GRPCServer) CreateObject(ctx context.Context, req *pb.CreateObjectRequest) (*pb.CreateObjectResponse, error) {
	obj, err := s.svc.Create(ctx, objectFromProto(req.GetObject()))
	if err != nil {
		return nil, toStatus(err)
	}
	return &pb.CreateObjectResponse{Object: objectToProto(obj)}, nil
}

func (s *GRPCServer) GetObject(ctx context.Context, req *pb.GetObjectRequest) (*pb.GetObjectResponse, error) {
	obj, err := s.svc.Get(ctx, req.GetId())
	if err != nil {
		return nil, toStatus(err)
	}
	return &pb.GetObjectResponse{Object: objectToProto(obj)}, nil
}

func (s *GRPCServer) ListObjects(ctx context.Context, req *pb.ListObjectsRequest) (*pb.ListObjectsResponse, error) {
	q := ListQuery{
		CreatedBy:      req.GetCreatedBy(),
		Kind:           Kind(req.GetKind()),
		IncludeDeleted: req.GetIncludeDeleted(),
		PageSize:       int(req.GetPageSize()),
		PageToken:      req.GetPageToken(),
	}
	// A oneof arrives as an interface holding one wrapper type per option;
	// a type switch finds out which one was set.
	switch scope := req.GetScope().(type) {
	case *pb.ListObjectsRequest_ProjectId:
		q.ProjectID = scope.ProjectId
	case *pb.ListObjectsRequest_Owner:
		q.Owner = ownerFromProto(scope.Owner)
	}
	page, err := s.svc.List(ctx, q)
	if err != nil {
		return nil, toStatus(err)
	}
	resp := &pb.ListObjectsResponse{
		Objects:       make([]*pb.Object, len(page.Objects)),
		NextPageToken: page.NextPageToken,
	}
	for i, obj := range page.Objects {
		resp.Objects[i] = objectToProto(obj)
	}
	return resp, nil
}

func (s *GRPCServer) MarkObjectDeleted(ctx context.Context, req *pb.MarkObjectDeletedRequest) (*pb.MarkObjectDeletedResponse, error) {
	if err := s.svc.MarkDeleted(ctx, req.GetId()); err != nil {
		return nil, toStatus(err)
	}
	return &pb.MarkObjectDeletedResponse{}, nil
}

func (s *GRPCServer) ClearCreatedBy(ctx context.Context, req *pb.ClearCreatedByRequest) (*pb.ClearCreatedByResponse, error) {
	n, err := s.svc.ClearCreatedBy(ctx, req.GetUserId())
	if err != nil {
		return nil, toStatus(err)
	}
	return &pb.ClearCreatedByResponse{Count: int64(n)}, nil
}
