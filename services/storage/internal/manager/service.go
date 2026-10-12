package manager

import (
	"context"

	"gyoza/services/storage/internal/ledger"
)

// Service is what other components call: the gateway (on behalf of a signed-in
// user, passing their ID as actorID) and iam (the owner lifecycle).
// *Manager implements it in-process; GRPCClient implements it over the
// network, with the same results and errors, so callers can't tell which they
// have. Callers should depend on Service, never on *Manager.
//
// The cleanup job (Manager.Cleanup, RunCleanup) is not part of it: it runs
// inside whichever process hosts the Manager.
type Service interface {
	// Uploads (manager_upload.go, manager_complete.go).
	BeginUpload(ctx context.Context, actorID string, req UploadRequest) (UploadTicket, error)
	CompleteUpload(ctx context.Context, actorID, uploadID string) (Asset, error)

	// Downloads (manager_download.go).
	DownloadURL(ctx context.Context, actorID, assetID string, version int) (Download, error)
	ExportURL(ctx context.Context, actorID, objectID string) (Download, error)

	// Assets (manager_dam.go).
	GetAsset(ctx context.Context, actorID, id string) (Asset, error)
	ListAssets(ctx context.Context, actorID string, q AssetQuery) (AssetPage, error)
	UpdateAsset(ctx context.Context, actorID, id string, u AssetUpdate) (Asset, error)
	DeleteAsset(ctx context.Context, actorID, id string) error
	RestoreAsset(ctx context.Context, actorID, id string) (Asset, error)
	ListVersions(ctx context.Context, actorID, id string) ([]Version, error)
	RevertAsset(ctx context.Context, actorID, id string, version int) (Asset, error)

	// Folders and the trash (manager_dam.go).
	CreateFolder(ctx context.Context, actorID string, f Folder) (Folder, error)
	RenameFolder(ctx context.Context, actorID, id, name string) (Folder, error)
	MoveFolder(ctx context.Context, actorID, id, newParentID string) (Folder, error)
	DeleteFolder(ctx context.Context, actorID, id string) (FolderDeletion, error)
	DeleteFolderTree(ctx context.Context, actorID, id string) (FolderDeletion, error)
	RestoreFolder(ctx context.Context, actorID, id string) (FolderDeletion, error)
	ListFolderContents(ctx context.Context, actorID string, owner ledger.Owner, folderID, pageToken string) (FolderContents, error)
	FolderPath(ctx context.Context, actorID, id string) ([]Folder, error)
	ListTrash(ctx context.Context, actorID string, owner ledger.Owner, pageToken string) (Trash, error)

	// Owner lifecycle, service to service only (manager_owners.go): the
	// gateway must never expose these to browsers.
	DeleteOwned(ctx context.Context, owner ledger.Owner) (OwnerDeletion, error)
	RemoveUser(ctx context.Context, userID, orgID string) (UserRemoval, error)
}

// Compile-time check that *Manager satisfies Service.
var _ Service = (*Manager)(nil)
