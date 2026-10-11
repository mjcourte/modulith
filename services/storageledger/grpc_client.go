package storageledger

import (
	"context"

	"google.golang.org/grpc"

	pb "gyoza/contracts/gen/go/gyoza/storageledger/v1"
)

// GRPCClient is a Service that calls a remote ledger over gRPC. Callers
// can't tell it apart from an in-process Service: it returns the same domain
// types and the same sentinel errors.
type GRPCClient struct {
	c pb.LedgerServiceClient
}

// NewGRPCClient uses conn, which the caller creates (grpc.NewClient) and closes.
func NewGRPCClient(conn grpc.ClientConnInterface) *GRPCClient {
	return &GRPCClient{c: pb.NewLedgerServiceClient(conn)}
}

// Compile-time check that *GRPCClient satisfies Service.
var _ Service = (*GRPCClient)(nil)

func (c *GRPCClient) Create(ctx context.Context, obj Object) (Object, error) {
	resp, err := c.c.CreateObject(ctx, &pb.CreateObjectRequest{Object: objectToProto(obj)})
	if err != nil {
		return Object{}, fromStatus(err, ErrInvalidObject)
	}
	return objectFromProto(resp.GetObject()), nil
}

func (c *GRPCClient) Get(ctx context.Context, id string) (Object, error) {
	resp, err := c.c.GetObject(ctx, &pb.GetObjectRequest{Id: id})
	if err != nil {
		return Object{}, fromStatus(err, ErrInvalidQuery)
	}
	return objectFromProto(resp.GetObject()), nil
}

func (c *GRPCClient) List(ctx context.Context, q ListQuery) (ListPage, error) {
	// Validate before sending: the request's oneof can only carry one scope,
	// so a query with both would otherwise be silently trimmed to one.
	n, err := q.normalize()
	if err != nil {
		return ListPage{}, err
	}
	req := &pb.ListObjectsRequest{
		CreatedBy:      n.CreatedBy,
		Kind:           string(n.Kind),
		IncludeDeleted: n.IncludeDeleted,
		PageSize:       int32(n.PageSize), // normalize caps it, so it fits
		PageToken:      n.PageToken,
	}
	if n.ProjectID != "" {
		req.Scope = &pb.ListObjectsRequest_ProjectId{ProjectId: n.ProjectID}
	} else {
		req.Scope = &pb.ListObjectsRequest_Owner{Owner: ownerToProto(n.Owner)}
	}

	resp, err := c.c.ListObjects(ctx, req)
	if err != nil {
		return ListPage{}, fromStatus(err, ErrInvalidQuery)
	}
	page := ListPage{
		Objects:       make([]Object, len(resp.GetObjects())),
		NextPageToken: resp.GetNextPageToken(),
	}
	for i, p := range resp.GetObjects() {
		page.Objects[i] = objectFromProto(p)
	}
	return page, nil
}

func (c *GRPCClient) MarkDeleted(ctx context.Context, id string) error {
	if _, err := c.c.MarkObjectDeleted(ctx, &pb.MarkObjectDeletedRequest{Id: id}); err != nil {
		return fromStatus(err, ErrInvalidQuery)
	}
	return nil
}

func (c *GRPCClient) ClearCreatedBy(ctx context.Context, userID string) (int, error) {
	resp, err := c.c.ClearCreatedBy(ctx, &pb.ClearCreatedByRequest{UserId: userID})
	if err != nil {
		return 0, fromStatus(err, ErrInvalidQuery)
	}
	return int(resp.GetCount()), nil
}
