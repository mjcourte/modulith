package grpcapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	pb "github.com/mjcourte/modulith/contracts/gen/go/modulith/documents/v1"
	"github.com/mjcourte/modulith/internal/engine"
	"github.com/mjcourte/modulith/services/documents/internal/manager"
)

// Server serves any Service (normally *manager.Manager) as DocumentsService.
type Server struct {
	pb.UnimplementedDocumentsServiceServer
	svc manager.Service
}

func NewServer(svc manager.Service) *Server { return &Server{svc: svc} }

func (s *Server) GetDocument(ctx context.Context, req *pb.GetDocumentRequest) (*pb.GetDocumentResponse, error) {
	d, err := s.svc.GetDocument(ctx, req.GetActorId(), req.GetProjectId())
	if err != nil {
		return nil, toStatus(err)
	}
	return &pb.GetDocumentResponse{Document: documentToProto(d)}, nil
}

func (s *Server) QueryDocument(ctx context.Context, req *pb.QueryDocumentRequest) (*pb.QueryDocumentResponse, error) {
	q := engine.Query{Format: engine.QueryFormat(req.GetFormat()), Source: req.GetSource()}
	if len(req.GetVariables()) > 0 {
		if err := json.Unmarshal(req.GetVariables(), &q.Variables); err != nil {
			return nil, toStatus(fmt.Errorf("%w: variables aren't a JSON object: %v", engine.ErrUnsupportedFormat, err))
		}
	}
	v, err := s.svc.QueryDocument(ctx, req.GetActorId(), req.GetProjectId(), q)
	if err != nil {
		return nil, toStatus(err)
	}
	return &pb.QueryDocumentResponse{Data: v.Data}, nil
}

func (s *Server) InitDocument(ctx context.Context, req *pb.InitDocumentRequest) (*pb.InitDocumentResponse, error) {
	d, err := s.svc.InitDocument(ctx, req.GetActorId(), req.GetProjectId())
	if err != nil {
		return nil, toStatus(err)
	}
	return &pb.InitDocumentResponse{Document: documentToProto(d)}, nil
}

// UpdateDocument returns an applied edit as a success even when it wasn't
// published or its uses weren't recorded: the caller must still get the
// delta, so those problems travel in the response instead of the status.
func (s *Server) UpdateDocument(ctx context.Context, req *pb.UpdateDocumentRequest) (*pb.UpdateDocumentResponse, error) {
	w := req.GetDelta()
	in := engine.DeltaInput{Format: engine.WireFormat(w.GetFormat()), Version: int(w.GetVersion()), Payload: w.GetPayload()}
	e, err := appliedEdit(s.svc.UpdateDocument(ctx, req.GetActorId(), req.GetProjectId(), in))
	if err != nil {
		return nil, err
	}
	return &pb.UpdateDocumentResponse{Delta: e.delta, NotPublished: e.notPublished, UsesNotRecorded: e.usesNotRecorded}, nil
}

func (s *Server) RevertDocument(ctx context.Context, req *pb.RevertDocumentRequest) (*pb.RevertDocumentResponse, error) {
	e, err := appliedEdit(s.svc.RevertDocument(ctx, req.GetActorId(), req.GetProjectId(), int(req.GetVersion())))
	if err != nil {
		return nil, err
	}
	return &pb.RevertDocumentResponse{Delta: e.delta, NotPublished: e.notPublished, UsesNotRecorded: e.usesNotRecorded}, nil
}

type edit struct {
	delta                         *pb.Wire
	notPublished, usesNotRecorded string
}

// appliedEdit splits an edit's result: a failure before it was applied is a
// status; ErrNotPublished and ErrUsesNotRecorded after it ride along.
func appliedEdit(out engine.Delta, err error) (edit, error) {
	e := edit{delta: wireToProto(out.Format, out.Version, out.Payload)}
	if err == nil {
		return e, nil
	}
	if len(out.Payload) == 0 {
		return edit{}, toStatus(err)
	}
	if errors.Is(err, manager.ErrNotPublished) {
		e.notPublished = err.Error()
	}
	if errors.Is(err, manager.ErrUsesNotRecorded) {
		e.usesNotRecorded = err.Error()
	}
	if e.notPublished == "" && e.usesNotRecorded == "" {
		return edit{}, toStatus(err) // applied, but failed some other way
	}
	return e, nil
}

func (s *Server) CloneDocument(ctx context.Context, req *pb.CloneDocumentRequest) (*pb.CloneDocumentResponse, error) {
	if err := s.svc.CloneDocument(ctx, req.GetActorId(), req.GetFromProjectId(), req.GetToProjectId()); err != nil {
		return nil, toStatus(err)
	}
	return &pb.CloneDocumentResponse{}, nil
}

func (s *Server) ReplaceAssetRef(ctx context.Context, req *pb.ReplaceAssetRefRequest) (*pb.ReplaceAssetRefResponse, error) {
	e, err := appliedEdit(s.svc.ReplaceAssetRef(ctx, req.GetActorId(), req.GetProjectId(), refFromProto(req.GetFrom()), refFromProto(req.GetTo())))
	if err != nil {
		return nil, err
	}
	return &pb.ReplaceAssetRefResponse{Delta: e.delta, NotPublished: e.notPublished, UsesNotRecorded: e.usesNotRecorded}, nil
}
