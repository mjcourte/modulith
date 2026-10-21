package manager

import (
	"context"

	"github.com/mjcourte/modulith/services/storage/internal/dam"
	"github.com/mjcourte/modulith/services/storage/internal/ledger"
)

// Service is what other components call: the gateway (on behalf of a signed-in
// user, passing their ID as actorID) and iam (the owner lifecycle).
// *Manager implements it in-process; grpcapi.Client implements it over the
// network, with the same results and errors, so callers can't tell which they
// have. Callers should depend on Service, never on *Manager.
//
// The cleanup job (Manager.Cleanup, RunCleanup) is not part of it: it runs
// inside whichever process hosts the Manager.
type Service interface {
	// Uploads (manager_upload.go, manager_complete.go).
	BeginUpload(ctx context.Context, actorID string, req UploadRequest) (UploadTicket, error)
	CompleteUpload(ctx context.Context, actorID, uploadID string) (dam.Asset, error)

	// Downloads (manager_download.go).
	DownloadURL(ctx context.Context, actorID, assetID string, version int) (Download, error)
	ExportURL(ctx context.Context, actorID, objectID string) (Download, error)

	// Assets (manager_dam.go).
	GetAsset(ctx context.Context, actorID, id string) (dam.Asset, error)
	ListAssets(ctx context.Context, actorID string, q dam.AssetQuery) (dam.AssetPage, error)
	UpdateAsset(ctx context.Context, actorID, id string, u dam.AssetUpdate) (dam.Asset, error)
	DeleteAsset(ctx context.Context, actorID, id string) error
	RestoreAsset(ctx context.Context, actorID, id string) (dam.Asset, error)
	ListVersions(ctx context.Context, actorID, id string) ([]dam.Version, error)
	RevertAsset(ctx context.Context, actorID, id string, version int) (dam.Asset, error)

	// Folders and the trash (manager_dam.go).
	CreateFolder(ctx context.Context, actorID string, f dam.Folder) (dam.Folder, error)
	RenameFolder(ctx context.Context, actorID, id, name string) (dam.Folder, error)
	MoveFolder(ctx context.Context, actorID, id, newParentID string) (dam.Folder, error)
	DeleteFolder(ctx context.Context, actorID, id string) (dam.FolderDeletion, error)
	DeleteFolderTree(ctx context.Context, actorID, id string) (dam.FolderDeletion, error)
	RestoreFolder(ctx context.Context, actorID, id string) (dam.FolderDeletion, error)
	ListFolderContents(ctx context.Context, actorID string, owner ledger.Owner, folderID, pageToken string) (FolderContents, error)
	FolderPath(ctx context.Context, actorID, id string) ([]dam.Folder, error)
	ListTrash(ctx context.Context, actorID string, owner ledger.Owner, pageToken string) (Trash, error)

	// Owner lifecycle, service to service only (manager_owners.go): the
	// gateway must never expose these to browsers.
	DeleteOwned(ctx context.Context, owner ledger.Owner) (OwnerDeletion, error)
	RemoveUser(ctx context.Context, userID, orgID string) (UserRemoval, error)

	// ResolveRef reads a ref a project's document uses (manager_resolve.go).
	ResolveRef(ctx context.Context, actorID, projectID string, ref dam.AssetRef) (ResolvedRef, error)

	// Asset uses, service to service only (manager_uses.go): documents
	// records each ref a project's document uses, for the user editing it.
	RecordAssetUse(ctx context.Context, actorID, projectID string, ref dam.AssetRef) error
	ForgetAssetUse(ctx context.Context, actorID, projectID string, ref dam.AssetRef) error

	// Copies (manager_copy.go). ForkAsset is for a user editing a document;
	// CopyProjectFiles is service to service only (iam's CloneProject).
	ForkAsset(ctx context.Context, actorID, projectID string, ref dam.AssetRef, target ledger.Owner, via string) (dam.Asset, error)
	CopyProjectFiles(ctx context.Context, actorID, from, to string) (CopyReport, error)

	// Document snapshots, service to service only (manager_snapshots.go):
	// the documents service. No actor: saves run on a timer.
	BeginSnapshot(ctx context.Context, req SnapshotRequest) (SnapshotTicket, error)
	CompleteSnapshot(ctx context.Context, projectID, objectID string) (SnapshotRef, error)
	LatestSnapshot(ctx context.Context, projectID string) (SnapshotRef, error)
	ListSnapshots(ctx context.Context, projectID, pageToken string) (SnapshotPage, error)
	SnapshotURL(ctx context.Context, projectID, objectID string) (Download, error)
}

// Compile-time check that *Manager satisfies Service.
var _ Service = (*Manager)(nil)
