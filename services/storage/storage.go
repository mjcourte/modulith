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
	"gyoza/services/storage/internal/blob"
	"gyoza/services/storage/internal/dam"
	"gyoza/services/storage/internal/db"
	"gyoza/services/storage/internal/grpcapi"
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

// Memory is a Manager on in-memory fakes (ledger, store, S3), for other
// services' tests.
type Memory struct {
	*Manager
	blobs *blob.Memory
}

// NewMemory builds a Memory that asks authz for permission.
func NewMemory(authz Authorizer, cfg Config) (*Memory, error) {
	blobs := blob.NewMemory()
	m, err := manager.NewManager(ledger.NewMemory(), dam.NewMemoryStore(), blobs, authz, cfg)
	if err != nil {
		return nil, err
	}
	return &Memory{Manager: m, blobs: blobs}, nil
}

// Upload plays the browser: it PUTs data to an upload URL from BeginUpload.
func (m *Memory) Upload(url, contentType string, data []byte) error {
	return m.blobs.PutSigned(url, contentType, data)
}

// RegisterGRPC serves s as StorageService on r.
func RegisterGRPC(r grpc.ServiceRegistrar, s Service) {
	pb.RegisterStorageServiceServer(r, grpcapi.NewServer(s))
}

// NewGRPCClient is a Service calling a remote storage over conn.
func NewGRPCClient(conn grpc.ClientConnInterface) Service { return grpcapi.NewClient(conn) }
