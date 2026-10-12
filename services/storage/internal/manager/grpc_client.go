package manager

import (
	"context"

	"google.golang.org/grpc"

	pb "gyoza/contracts/gen/go/gyoza/storage/v1"
	"gyoza/services/storage/internal/ledger"
)

// GRPCClient is a Service that calls a remote storage over gRPC.
// Callers can't tell it apart from an in-process *Manager: it returns the
// same domain types and the same errors (errors.Is and errors.As work as
// they would locally).
type GRPCClient struct {
	c pb.StorageServiceClient
}

// NewGRPCClient uses conn, which the caller creates (grpc.NewClient) and closes.
func NewGRPCClient(conn grpc.ClientConnInterface) *GRPCClient {
	return &GRPCClient{c: pb.NewStorageServiceClient(conn)}
}

// Compile-time check that *GRPCClient satisfies Service.
var _ Service = (*GRPCClient)(nil)

// ---- Uploads

func (c *GRPCClient) BeginUpload(ctx context.Context, actorID string, req UploadRequest) (UploadTicket, error) {
	resp, err := c.c.BeginUpload(ctx, &pb.BeginUploadRequest{
		ActorId:     actorID,
		Owner:       ownerToProto(req.Owner),
		ProjectId:   req.ProjectID,
		AssetId:     req.AssetID,
		Name:        req.Name,
		Description: req.Description,
		Tags:        req.Tags,
		FolderId:    req.FolderID,
		ContentType: req.ContentType,
		SizeBytes:   req.SizeBytes,
	})
	if err != nil {
		return UploadTicket{}, fromStatus(err)
	}
	return UploadTicket{
		UploadID:  resp.GetUploadId(),
		Method:    resp.GetMethod(),
		URL:       resp.GetUrl(),
		Headers:   resp.GetHeaders(),
		ExpiresAt: timeOf(resp.GetExpiresAt()),
	}, nil
}

func (c *GRPCClient) CompleteUpload(ctx context.Context, actorID, uploadID string) (Asset, error) {
	resp, err := c.c.CompleteUpload(ctx, &pb.CompleteUploadRequest{ActorId: actorID, UploadId: uploadID})
	if err != nil {
		return Asset{}, fromStatus(err)
	}
	return assetFromProto(resp.GetAsset()), nil
}

// ---- Downloads

func (c *GRPCClient) DownloadURL(ctx context.Context, actorID, assetID string, version int) (Download, error) {
	resp, err := c.c.GetDownloadURL(ctx, &pb.GetDownloadURLRequest{ActorId: actorID, AssetId: assetID, Version: int32(version)})
	if err != nil {
		return Download{}, fromStatus(err)
	}
	return downloadFromProto(resp.GetDownload()), nil
}

func (c *GRPCClient) ExportURL(ctx context.Context, actorID, objectID string) (Download, error) {
	resp, err := c.c.GetExportURL(ctx, &pb.GetExportURLRequest{ActorId: actorID, ObjectId: objectID})
	if err != nil {
		return Download{}, fromStatus(err)
	}
	return downloadFromProto(resp.GetDownload()), nil
}

// ---- Assets

func (c *GRPCClient) GetAsset(ctx context.Context, actorID, id string) (Asset, error) {
	resp, err := c.c.GetAsset(ctx, &pb.GetAssetRequest{ActorId: actorID, Id: id})
	if err != nil {
		return Asset{}, fromStatus(err)
	}
	return assetFromProto(resp.GetAsset()), nil
}

func (c *GRPCClient) ListAssets(ctx context.Context, actorID string, q AssetQuery) (AssetPage, error) {
	resp, err := c.c.ListAssets(ctx, &pb.ListAssetsRequest{
		ActorId:        actorID,
		ProjectId:      q.ProjectID,
		Owner:          ownerToProto(q.Owner),
		Tag:            q.Tag,
		Category:       string(q.Category),
		FolderId:       q.Folder,
		IncludeDeleted: q.IncludeDeleted,
		OnlyDeleted:    q.OnlyDeleted,
		PageSize:       int32(max(min(q.PageSize, MaxPageSize), -1)), // the server caps it anyway; this keeps it within int32
		PageToken:      q.PageToken,
	})
	if err != nil {
		return AssetPage{}, fromStatus(err)
	}
	return assetPageFromProto(resp.GetPage()), nil
}

func (c *GRPCClient) UpdateAsset(ctx context.Context, actorID, id string, u AssetUpdate) (Asset, error) {
	req := &pb.UpdateAssetRequest{ActorId: actorID, Id: id, Name: u.Name, Description: u.Description, FolderId: u.FolderID}
	if u.Tags != nil {
		req.Tags = &pb.Tags{Tags: *u.Tags}
	}
	resp, err := c.c.UpdateAsset(ctx, req)
	if err != nil {
		return Asset{}, fromStatus(err)
	}
	return assetFromProto(resp.GetAsset()), nil
}

func (c *GRPCClient) DeleteAsset(ctx context.Context, actorID, id string) error {
	if _, err := c.c.DeleteAsset(ctx, &pb.DeleteAssetRequest{ActorId: actorID, Id: id}); err != nil {
		return fromStatus(err)
	}
	return nil
}

func (c *GRPCClient) RestoreAsset(ctx context.Context, actorID, id string) (Asset, error) {
	resp, err := c.c.RestoreAsset(ctx, &pb.RestoreAssetRequest{ActorId: actorID, Id: id})
	if err != nil {
		return Asset{}, fromStatus(err)
	}
	return assetFromProto(resp.GetAsset()), nil
}

func (c *GRPCClient) ListVersions(ctx context.Context, actorID, id string) ([]Version, error) {
	resp, err := c.c.ListVersions(ctx, &pb.ListVersionsRequest{ActorId: actorID, AssetId: id})
	if err != nil {
		return nil, fromStatus(err)
	}
	vs := make([]Version, len(resp.GetVersions()))
	for i, p := range resp.GetVersions() {
		vs[i] = versionFromProto(p)
	}
	return vs, nil
}

func (c *GRPCClient) RevertAsset(ctx context.Context, actorID, id string, version int) (Asset, error) {
	resp, err := c.c.RevertAsset(ctx, &pb.RevertAssetRequest{ActorId: actorID, AssetId: id, Version: int32(version)})
	if err != nil {
		return Asset{}, fromStatus(err)
	}
	return assetFromProto(resp.GetAsset()), nil
}

// ---- Folders and the trash

func (c *GRPCClient) CreateFolder(ctx context.Context, actorID string, f Folder) (Folder, error) {
	resp, err := c.c.CreateFolder(ctx, &pb.CreateFolderRequest{
		ActorId: actorID, Owner: ownerToProto(f.Owner), ParentId: f.ParentID, Name: f.Name,
	})
	if err != nil {
		return Folder{}, fromStatus(err)
	}
	return folderFromProto(resp.GetFolder()), nil
}

func (c *GRPCClient) RenameFolder(ctx context.Context, actorID, id, name string) (Folder, error) {
	resp, err := c.c.RenameFolder(ctx, &pb.RenameFolderRequest{ActorId: actorID, Id: id, Name: name})
	if err != nil {
		return Folder{}, fromStatus(err)
	}
	return folderFromProto(resp.GetFolder()), nil
}

func (c *GRPCClient) MoveFolder(ctx context.Context, actorID, id, newParentID string) (Folder, error) {
	resp, err := c.c.MoveFolder(ctx, &pb.MoveFolderRequest{ActorId: actorID, Id: id, NewParentId: newParentID})
	if err != nil {
		return Folder{}, fromStatus(err)
	}
	return folderFromProto(resp.GetFolder()), nil
}

func (c *GRPCClient) DeleteFolder(ctx context.Context, actorID, id string) (FolderDeletion, error) {
	resp, err := c.c.DeleteFolder(ctx, &pb.DeleteFolderRequest{ActorId: actorID, Id: id})
	if err != nil {
		return FolderDeletion{}, fromStatus(err)
	}
	return deletionFromProto(resp.GetDeletion()), nil
}

func (c *GRPCClient) DeleteFolderTree(ctx context.Context, actorID, id string) (FolderDeletion, error) {
	resp, err := c.c.DeleteFolderTree(ctx, &pb.DeleteFolderTreeRequest{ActorId: actorID, Id: id})
	if err != nil {
		return FolderDeletion{}, fromStatus(err)
	}
	return deletionFromProto(resp.GetDeletion()), nil
}

func (c *GRPCClient) RestoreFolder(ctx context.Context, actorID, id string) (FolderDeletion, error) {
	resp, err := c.c.RestoreFolder(ctx, &pb.RestoreFolderRequest{ActorId: actorID, Id: id})
	if err != nil {
		return FolderDeletion{}, fromStatus(err)
	}
	return deletionFromProto(resp.GetDeletion()), nil
}

func (c *GRPCClient) ListFolderContents(ctx context.Context, actorID string, owner ledger.Owner, folderID, pageToken string) (FolderContents, error) {
	resp, err := c.c.ListFolderContents(ctx, &pb.ListFolderContentsRequest{
		ActorId: actorID, Owner: ownerToProto(owner), FolderId: folderID, PageToken: pageToken,
	})
	if err != nil {
		return FolderContents{}, fromStatus(err)
	}
	return FolderContents{
		Path:    foldersFromProto(resp.GetPath()),
		Folders: foldersFromProto(resp.GetFolders()),
		Assets:  assetPageFromProto(resp.GetAssets()),
	}, nil
}

func (c *GRPCClient) FolderPath(ctx context.Context, actorID, id string) ([]Folder, error) {
	resp, err := c.c.GetFolderPath(ctx, &pb.GetFolderPathRequest{ActorId: actorID, Id: id})
	if err != nil {
		return nil, fromStatus(err)
	}
	return foldersFromProto(resp.GetPath()), nil
}

func (c *GRPCClient) ListTrash(ctx context.Context, actorID string, owner ledger.Owner, pageToken string) (Trash, error) {
	resp, err := c.c.ListTrash(ctx, &pb.ListTrashRequest{ActorId: actorID, Owner: ownerToProto(owner), PageToken: pageToken})
	if err != nil {
		return Trash{}, fromStatus(err)
	}
	return Trash{Folders: foldersFromProto(resp.GetFolders()), Assets: assetPageFromProto(resp.GetAssets())}, nil
}

// ---- Owner lifecycle

func (c *GRPCClient) DeleteOwned(ctx context.Context, owner ledger.Owner) (OwnerDeletion, error) {
	resp, err := c.c.DeleteOwned(ctx, &pb.DeleteOwnedRequest{Owner: ownerToProto(owner)})
	if err != nil {
		return OwnerDeletion{}, fromStatus(err)
	}
	return OwnerDeletion{LedgerObjects: int(resp.GetLedgerObjects()), Trash: deletionFromProto(resp.GetTrash())}, nil
}

func (c *GRPCClient) RemoveUser(ctx context.Context, userID, orgID string) (UserRemoval, error) {
	resp, err := c.c.RemoveUser(ctx, &pb.RemoveUserRequest{UserId: userID, OrgId: orgID})
	if err != nil {
		return UserRemoval{}, fromStatus(err)
	}
	r := UserRemoval{
		ExpiredUploads: int(resp.GetExpiredUploads()),
		Transfer: OwnerTransfer{
			Assets:  int(resp.GetTransferredAssets()),
			Folders: int(resp.GetTransferredFolders()),
			Renamed: int(resp.GetRenamedFolders()),
		},
		LedgerObjects: int(resp.GetLedgerObjects()),
		Deleted: OwnerDeletion{
			LedgerObjects: int(resp.GetDeletedLedgerObjects()),
			Trash:         deletionFromProto(resp.GetDeleted()),
		},
		ClearedLedger: int(resp.GetClearedLedger()),
		ClearedDAM:    int(resp.GetClearedDam()),
	}
	if resp.GetTransferredTo() != nil {
		r.TransferredTo = ownerFromProto(resp.GetTransferredTo())
	}
	return r, nil
}
