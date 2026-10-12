// Package storage keeps every file Gyoza stores in S3: uploads, downloads, the
// DAM, cleanup, and the ledger recording each object. The logic is under
// internal/; this file is the public surface.
//
// The names below are aliases (type X = Y), not new types, so values flow
// between this package and internal/ without conversion.
package storage

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc"

	pb "gyoza/contracts/gen/go/gyoza/storage/v1"
	"gyoza/platform/blob"
	"gyoza/services/storage/internal/db"
	"gyoza/services/storage/internal/ledger"
	"gyoza/services/storage/internal/manager"
)

// Who owns a file; shared with services that own files (iam).
type (
	Owner     = ledger.Owner
	OwnerKind = ledger.OwnerKind
)

const (
	OwnerProject = ledger.OwnerProject
	OwnerUser    = ledger.OwnerUser
	OwnerOrg     = ledger.OwnerOrg
	OwnerGyoza   = ledger.OwnerGyoza
)

// Manager API.
type (
	Service       = manager.Service
	Manager       = manager.Manager
	Config        = manager.Config
	Authorizer    = manager.Authorizer
	AllowAll      = manager.AllowAll
	UploadRequest = manager.UploadRequest
)

const (
	DefaultUploadURLTTL   = manager.DefaultUploadURLTTL
	DefaultDownloadURLTTL = manager.DefaultDownloadURLTTL
	DefaultTrashRetention = manager.DefaultTrashRetention
)

// ErrForbidden is what an Authorizer's refusals must wrap.
var ErrForbidden = manager.ErrForbidden

// Migrate brings the storage schema up to date.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error { return db.Migrate(ctx, pool) }

// NewMemoryManager builds a Manager on in-memory ledger and store, for other
// services' tests.
func NewMemoryManager(blobs blob.Store, authz Authorizer, cfg Config) (*Manager, error) {
	return manager.NewManager(ledger.NewMemory(), manager.NewMemoryStore(), blobs, authz, cfg)
}

// RegisterGRPC serves s as StorageService on r.
func RegisterGRPC(r grpc.ServiceRegistrar, s Service) {
	pb.RegisterStorageServiceServer(r, manager.NewGRPCServer(s))
}

// NewGRPCClient is a Service calling a remote storage over conn.
func NewGRPCClient(conn grpc.ClientConnInterface) Service { return manager.NewGRPCClient(conn) }
