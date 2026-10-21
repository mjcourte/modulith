// Package storage keeps every file Modulith stores in S3: uploads, downloads, the
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

	pb "github.com/mjcourte/modulith/contracts/gen/go/modulith/storage/v1"
	"github.com/mjcourte/modulith/services/storage/internal/blob"
	"github.com/mjcourte/modulith/services/storage/internal/dam"
	"github.com/mjcourte/modulith/services/storage/internal/db"
	"github.com/mjcourte/modulith/services/storage/internal/grpcapi"
	"github.com/mjcourte/modulith/services/storage/internal/ledger"
	"github.com/mjcourte/modulith/services/storage/internal/manager"
	"github.com/mjcourte/modulith/services/storage/internal/snapshots"
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
	OwnerModulith   = ledger.OwnerModulith
)

type AssetRef = dam.AssetRef

// CopyAssetID is the ID of the copy of src that a clone (projectID, owned
// by target) or a fork gets; the copy's ref is {CopyAssetID(...), 1}.
func CopyAssetID(projectID string, target Owner, src AssetRef) string {
	return dam.CopyAssetID(projectID, target, src)
}

// Manager API.
type (
	Service       = manager.Service
	Manager       = manager.Manager
	Config        = manager.Config
	Authorizer    = manager.Authorizer
	AllowAll      = manager.AllowAll
	UploadRequest = manager.UploadRequest

	ResolvedRef = manager.ResolvedRef
	CopyReport  = manager.CopyReport

	// What the owner lifecycle calls (iam's cascades) report.
	OwnerDeletion = manager.OwnerDeletion
	UserRemoval   = manager.UserRemoval

	// Document snapshots (the documents service).
	SnapshotRequest = manager.SnapshotRequest
	SnapshotTicket  = manager.SnapshotTicket
	SnapshotRef     = manager.SnapshotRef
	SnapshotPage    = manager.SnapshotPage
	Download        = manager.Download
)

const (
	DefaultUploadURLTTL     = manager.DefaultUploadURLTTL
	DefaultDownloadURLTTL   = manager.DefaultDownloadURLTTL
	DefaultTrashRetention   = manager.DefaultTrashRetention
	DefaultSnapshotKeep     = manager.DefaultSnapshotKeep
	DefaultMaxSnapshotBytes = manager.DefaultMaxSnapshotBytes
)

// ErrForbidden is what an Authorizer's refusals must wrap.
var ErrForbidden = manager.ErrForbidden

// ErrForkRequired: a project can only use its own (or its owner's) assets in
// place; anything else must be forked first.
var ErrForkRequired = manager.ErrForkRequired

// S3Config is how storage reaches S3 (Settings.S3), for tests elsewhere that
// open a real storage against RustFS.
type S3Config = blob.S3Config

// The DAM: assets, versions, folders and uploads, as the Service takes and
// returns them (the gateway encodes them for browsers).
type (
	Asset               = dam.Asset
	AssetPage           = dam.AssetPage
	AssetQuery          = dam.AssetQuery
	AssetUpdate         = dam.AssetUpdate
	Version             = dam.Version
	Category            = dam.Category
	Folder              = dam.Folder
	FolderDeletion      = dam.FolderDeletion
	FolderNotEmptyError = dam.FolderNotEmptyError
	FolderContents      = manager.FolderContents
	Trash               = manager.Trash
	UploadTicket        = manager.UploadTicket
)

// DAM errors. Over gRPC the client rebuilds them, so errors.Is works on
// either transport.
var (
	ErrAssetNotFound   = dam.ErrAssetNotFound
	ErrVersionNotFound = dam.ErrVersionNotFound
	ErrInvalidAsset    = dam.ErrInvalidAsset
	ErrInvalidQuery    = dam.ErrInvalidQuery
	ErrFolderNotFound  = dam.ErrFolderNotFound
	ErrInvalidFolder   = dam.ErrInvalidFolder
	ErrFolderNotEmpty  = dam.ErrFolderNotEmpty // also as *FolderNotEmptyError, with counts
	ErrNameTaken       = dam.ErrNameTaken
	ErrUploadNotFound  = dam.ErrUploadNotFound
	ErrUploadExpired   = dam.ErrUploadExpired
	ErrInvalidUpload   = dam.ErrInvalidUpload
	ErrUploadTooLarge  = manager.ErrUploadTooLarge
)

// Snapshot errors.
var (
	ErrSnapshotStale    = manager.ErrSnapshotStale
	ErrSnapshotNotFound = manager.ErrSnapshotNotFound
	ErrSnapshotTooLarge = manager.ErrSnapshotTooLarge
	ErrSnapshotExpired  = manager.ErrSnapshotExpired
	ErrInvalidSnapshot  = manager.ErrInvalidSnapshot
	ErrNotUploaded      = manager.ErrNotUploaded
)

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
	m, err := manager.NewManager(ledger.NewMemory(), dam.NewMemoryStore(), snapshots.NewMemory(), blobs, authz, cfg)
	if err != nil {
		return nil, err
	}
	return &Memory{Manager: m, blobs: blobs}, nil
}

// Upload plays the browser: it PUTs data to an upload URL from BeginUpload.
func (m *Memory) Upload(url, contentType string, data []byte) error {
	return m.blobs.PutSigned(url, contentType, data)
}

// Download plays the browser: it GETs a download URL (DownloadURL,
// SnapshotURL, ...).
func (m *Memory) Download(url string) ([]byte, error) {
	data, _, err := m.blobs.GetSigned(url)
	return data, err
}

// RegisterGRPC serves s as StorageService on r.
func RegisterGRPC(r grpc.ServiceRegistrar, s Service) {
	pb.RegisterStorageServiceServer(r, grpcapi.NewServer(s))
}

// NewGRPCClient is a Service calling a remote storage over conn.
func NewGRPCClient(conn grpc.ClientConnInterface) Service { return grpcapi.NewClient(conn) }
