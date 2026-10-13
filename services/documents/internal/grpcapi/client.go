package grpcapi

import (
	"context"
	"encoding/json"
	"errors"

	"google.golang.org/grpc"

	pb "gyoza/contracts/gen/go/gyoza/documents/v1"
	"gyoza/internal/engine"
	"gyoza/services/documents/internal/manager"
)

// Client is a Service over the wire: the same results and errors as the
// in-process Manager.
type Client struct {
	c pb.DocumentsServiceClient
}

func NewClient(conn grpc.ClientConnInterface) *Client {
	return &Client{c: pb.NewDocumentsServiceClient(conn)}
}

var _ manager.Service = (*Client)(nil)

func (c *Client) GetDocument(ctx context.Context, actorID, projectID string) (manager.Document, error) {
	resp, err := c.c.GetDocument(ctx, &pb.GetDocumentRequest{ActorId: actorID, ProjectId: projectID})
	if err != nil {
		return manager.Document{}, fromStatus(err)
	}
	return documentFromProto(resp.GetDocument()), nil
}

func (c *Client) QueryDocument(ctx context.Context, actorID, projectID string, q engine.Query) (*engine.View, error) {
	req := &pb.QueryDocumentRequest{ActorId: actorID, ProjectId: projectID, Format: string(q.Format), Source: q.Source}
	if len(q.Variables) > 0 {
		vars, err := json.Marshal(q.Variables)
		if err != nil {
			return nil, err
		}
		req.Variables = vars
	}
	resp, err := c.c.QueryDocument(ctx, req)
	if err != nil {
		return nil, fromStatus(err)
	}
	return &engine.View{Data: resp.GetData()}, nil
}

func (c *Client) InitDocument(ctx context.Context, actorID, projectID string) (manager.Document, error) {
	resp, err := c.c.InitDocument(ctx, &pb.InitDocumentRequest{ActorId: actorID, ProjectId: projectID})
	if err != nil {
		return manager.Document{}, fromStatus(err)
	}
	return documentFromProto(resp.GetDocument()), nil
}

func (c *Client) UpdateDocument(ctx context.Context, actorID, projectID string, delta engine.DeltaInput) (engine.Delta, error) {
	resp, err := c.c.UpdateDocument(ctx, &pb.UpdateDocumentRequest{
		ActorId: actorID, ProjectId: projectID,
		Delta: wireToProto(delta.Format, delta.Version, delta.Payload),
	})
	if err != nil {
		return engine.Delta{}, fromStatus(err)
	}
	return editFromProto(resp.GetDelta(), resp.GetNotPublished(), resp.GetUsesNotRecorded())
}

func (c *Client) RevertDocument(ctx context.Context, actorID, projectID string, version int) (engine.Delta, error) {
	resp, err := c.c.RevertDocument(ctx, &pb.RevertDocumentRequest{ActorId: actorID, ProjectId: projectID, Version: int32(version)})
	if err != nil {
		return engine.Delta{}, fromStatus(err)
	}
	return editFromProto(resp.GetDelta(), resp.GetNotPublished(), resp.GetUsesNotRecorded())
}

// editFromProto rebuilds an applied edit and the errors the server reported
// with it in the response.
func editFromProto(w *pb.Wire, notPublished, usesNotRecorded string) (engine.Delta, error) {
	out := engine.Delta{Format: engine.WireFormat(w.GetFormat()), Version: int(w.GetVersion()), Payload: w.GetPayload()}
	var errs []error
	if notPublished != "" {
		errs = append(errs, &remoteError{msg: notPublished, errs: []error{manager.ErrNotPublished}})
	}
	if usesNotRecorded != "" {
		errs = append(errs, &remoteError{msg: usesNotRecorded, errs: []error{manager.ErrUsesNotRecorded}})
	}
	return out, errors.Join(errs...)
}
