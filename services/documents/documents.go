// Package documents keeps live documents on the engine: it applies edits,
// distributes them through the NATS write-ahead log, and stores snapshots
// through storage. The logic is under internal/; this file is the public
// surface.
//
// The names below are aliases (type X = Y), not new types, so values flow
// between this package and internal/ without conversion.
package documents

import (
	"google.golang.org/grpc"

	pb "github.com/mjcourte/modulith/contracts/gen/go/modulith/documents/v1"
	"github.com/mjcourte/modulith/internal/engine"
	"github.com/mjcourte/modulith/services/documents/internal/grpcapi"
	"github.com/mjcourte/modulith/services/documents/internal/manager"
	"github.com/mjcourte/modulith/services/documents/internal/snapshots"
)

// Manager API.
type (
	Service    = manager.Service
	Manager    = manager.Manager
	Config     = manager.Config
	Document   = manager.Document
	Authorizer = manager.Authorizer
	AssetUses  = manager.AssetUses
	WAL        = manager.WAL
	// AllowAll lets every identified user do anything: tests and local
	// experiments only. No cmd/ binary may wire it (PLAN.md, "Authorization").
	AllowAll = manager.AllowAll

	SnapshotStore = snapshots.Store
)

// Errors callers can test for with errors.Is.
var (
	ErrForbidden       = manager.ErrForbidden
	ErrExists          = manager.ErrExists
	ErrNotPublished    = manager.ErrNotPublished
	ErrUsesNotRecorded = manager.ErrUsesNotRecorded
)

// New builds the documents service. Close it to save open documents.
func New(eng engine.Engine, store SnapshotStore, authz Authorizer, uses AssetUses, cfg Config) *Manager {
	return manager.New(eng, store, authz, uses, cfg)
}

// RegisterGRPC serves s as DocumentsService on r.
func RegisterGRPC(r grpc.ServiceRegistrar, s Service) {
	pb.RegisterDocumentsServiceServer(r, grpcapi.NewServer(s))
}

// NewGRPCClient is a Service calling a remote documents over conn.
func NewGRPCClient(conn grpc.ClientConnInterface) Service { return grpcapi.NewClient(conn) }
