package grpcapi

import (
	"context"

	"google.golang.org/grpc"

	pb "gyoza/contracts/gen/go/gyoza/storage/v1"
	"gyoza/services/storage/internal/dam"
	"gyoza/services/storage/internal/ledger"
	"gyoza/services/storage/internal/manager"
)

// Client is a Service that calls a remote storage over gRPC.
// Callers can't tell it apart from an in-process *Manager: it returns the
// same domain types and the same errors (errors.Is and errors.As work as
// they would locally).
type Client struct {
	c pb.StorageServiceClient
}

// NewClient uses conn, which the caller creates (grpc.NewClient) and closes.
func NewClient(conn grpc.ClientConnInterface) *Client {
	return &Client{c: pb.NewStorageServiceClient(conn)}
}

// Compile-time check that *Client satisfies Service.
var _ manager.Service = (*Client)(nil)

// ---- Uploads

func (c *Client) BeginUpload(ctx context.Context, actorID string, req manager.UploadRequest) (manager.UploadTicket, error) {
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
		return manager.UploadTicket{}, fromStatus(err)
	}
	return manager.UploadTicket{
		UploadID:  resp.GetUploadId(),
		Method:    resp.GetMethod(),
		URL:       resp.GetUrl(),
		Headers:   resp.GetHeaders(),
		ExpiresAt: timeOf(resp.GetExpiresAt()),
	}, nil
}

func (c *Client) CompleteUpload(ctx context.Context, actorID, uploadID string) (dam.Asset, error) {
	resp, err := c.c.CompleteUpload(ctx, &pb.CompleteUploadRequest{ActorId: actorID, UploadId: uploadID})
	if err != nil {
		return dam.Asset{}, fromStatus(err)
	}
	return assetFromProto(resp.GetAsset()), nil
}

// ---- Downloads

func (c *Client) DownloadURL(ctx context.Context, actorID, assetID string, version int) (manager.Download, error) {
	resp, err := c.c.GetDownloadURL(ctx, &pb.GetDownloadURLRequest{ActorId: actorID, AssetId: assetID, Version: int32(version)})
	if err != nil {
		return manager.Download{}, fromStatus(err)
	}
	return downloadFromProto(resp.GetDownload()), nil
}

func (c *Client) ExportURL(ctx context.Context, actorID, objectID string) (manager.Download, error) {
	resp, err := c.c.GetExportURL(ctx, &pb.GetExportURLRequest{ActorId: actorID, ObjectId: objectID})
	if err != nil {
		return manager.Download{}, fromStatus(err)
	}
	return downloadFromProto(resp.GetDownload()), nil
}

// ---- Assets

func (c *Client) GetAsset(ctx context.Context, actorID, id string) (dam.Asset, error) {
	resp, err := c.c.GetAsset(ctx, &pb.GetAssetRequest{ActorId: actorID, Id: id})
	if err != nil {
		return dam.Asset{}, fromStatus(err)
	}
	return assetFromProto(resp.GetAsset()), nil
}

func (c *Client) ListAssets(ctx context.Context, actorID string, q dam.AssetQuery) (dam.AssetPage, error) {
	resp, err := c.c.ListAssets(ctx, &pb.ListAssetsRequest{
		ActorId:        actorID,
		ProjectId:      q.ProjectID,
		Owner:          ownerToProto(q.Owner),
		Tag:            q.Tag,
		Category:       string(q.Category),
		FolderId:       q.Folder,
		IncludeDeleted: q.IncludeDeleted,
		OnlyDeleted:    q.OnlyDeleted,
		PageSize:       int32(max(min(q.PageSize, dam.MaxPageSize), -1)), // the server caps it anyway; this keeps it within int32
		PageToken:      q.PageToken,
	})
	if err != nil {
		return dam.AssetPage{}, fromStatus(err)
	}
	return assetPageFromProto(resp.GetPage()), nil
}

func (c *Client) UpdateAsset(ctx context.Context, actorID, id string, u dam.AssetUpdate) (dam.Asset, error) {
	req := &pb.UpdateAssetRequest{ActorId: actorID, Id: id, Name: u.Name, Description: u.Description, FolderId: u.FolderID}
	if u.Tags != nil {
		req.Tags = &pb.Tags{Tags: *u.Tags}
	}
	resp, err := c.c.UpdateAsset(ctx, req)
	if err != nil {
		return dam.Asset{}, fromStatus(err)
	}
	return assetFromProto(resp.GetAsset()), nil
}

func (c *Client) DeleteAsset(ctx context.Context, actorID, id string) error {
	if _, err := c.c.DeleteAsset(ctx, &pb.DeleteAssetRequest{ActorId: actorID, Id: id}); err != nil {
		return fromStatus(err)
	}
	return nil
}

func (c *Client) RestoreAsset(ctx context.Context, actorID, id string) (dam.Asset, error) {
	resp, err := c.c.RestoreAsset(ctx, &pb.RestoreAssetRequest{ActorId: actorID, Id: id})
	if err != nil {
		return dam.Asset{}, fromStatus(err)
	}
	return assetFromProto(resp.GetAsset()), nil
}

func (c *Client) ListVersions(ctx context.Context, actorID, id string) ([]dam.Version, error) {
	resp, err := c.c.ListVersions(ctx, &pb.ListVersionsRequest{ActorId: actorID, AssetId: id})
	if err != nil {
		return nil, fromStatus(err)
	}
	vs := make([]dam.Version, len(resp.GetVersions()))
	for i, p := range resp.GetVersions() {
		vs[i] = versionFromProto(p)
	}
	return vs, nil
}

func (c *Client) RevertAsset(ctx context.Context, actorID, id string, version int) (dam.Asset, error) {
	resp, err := c.c.RevertAsset(ctx, &pb.RevertAssetRequest{ActorId: actorID, AssetId: id, Version: int32(version)})
	if err != nil {
		return dam.Asset{}, fromStatus(err)
	}
	return assetFromProto(resp.GetAsset()), nil
}

// ---- Folders and the trash

func (c *Client) CreateFolder(ctx context.Context, actorID string, f dam.Folder) (dam.Folder, error) {
	resp, err := c.c.CreateFolder(ctx, &pb.CreateFolderRequest{
		ActorId: actorID, Owner: ownerToProto(f.Owner), ParentId: f.ParentID, Name: f.Name,
	})
	if err != nil {
		return dam.Folder{}, fromStatus(err)
	}
	return folderFromProto(resp.GetFolder()), nil
}

func (c *Client) RenameFolder(ctx context.Context, actorID, id, name string) (dam.Folder, error) {
	resp, err := c.c.RenameFolder(ctx, &pb.RenameFolderRequest{ActorId: actorID, Id: id, Name: name})
	if err != nil {
		return dam.Folder{}, fromStatus(err)
	}
	return folderFromProto(resp.GetFolder()), nil
}

func (c *Client) MoveFolder(ctx context.Context, actorID, id, newParentID string) (dam.Folder, error) {
	resp, err := c.c.MoveFolder(ctx, &pb.MoveFolderRequest{ActorId: actorID, Id: id, NewParentId: newParentID})
	if err != nil {
		return dam.Folder{}, fromStatus(err)
	}
	return folderFromProto(resp.GetFolder()), nil
}

func (c *Client) DeleteFolder(ctx context.Context, actorID, id string) (dam.FolderDeletion, error) {
	resp, err := c.c.DeleteFolder(ctx, &pb.DeleteFolderRequest{ActorId: actorID, Id: id})
	if err != nil {
		return dam.FolderDeletion{}, fromStatus(err)
	}
	return deletionFromProto(resp.GetDeletion()), nil
}

func (c *Client) DeleteFolderTree(ctx context.Context, actorID, id string) (dam.FolderDeletion, error) {
	resp, err := c.c.DeleteFolderTree(ctx, &pb.DeleteFolderTreeRequest{ActorId: actorID, Id: id})
	if err != nil {
		return dam.FolderDeletion{}, fromStatus(err)
	}
	return deletionFromProto(resp.GetDeletion()), nil
}

func (c *Client) RestoreFolder(ctx context.Context, actorID, id string) (dam.FolderDeletion, error) {
	resp, err := c.c.RestoreFolder(ctx, &pb.RestoreFolderRequest{ActorId: actorID, Id: id})
	if err != nil {
		return dam.FolderDeletion{}, fromStatus(err)
	}
	return deletionFromProto(resp.GetDeletion()), nil
}

func (c *Client) ListFolderContents(ctx context.Context, actorID string, owner ledger.Owner, folderID, pageToken string) (manager.FolderContents, error) {
	resp, err := c.c.ListFolderContents(ctx, &pb.ListFolderContentsRequest{
		ActorId: actorID, Owner: ownerToProto(owner), FolderId: folderID, PageToken: pageToken,
	})
	if err != nil {
		return manager.FolderContents{}, fromStatus(err)
	}
	return manager.FolderContents{
		Path:    foldersFromProto(resp.GetPath()),
		Folders: foldersFromProto(resp.GetFolders()),
		Assets:  assetPageFromProto(resp.GetAssets()),
	}, nil
}

func (c *Client) FolderPath(ctx context.Context, actorID, id string) ([]dam.Folder, error) {
	resp, err := c.c.GetFolderPath(ctx, &pb.GetFolderPathRequest{ActorId: actorID, Id: id})
	if err != nil {
		return nil, fromStatus(err)
	}
	return foldersFromProto(resp.GetPath()), nil
}

func (c *Client) ListTrash(ctx context.Context, actorID string, owner ledger.Owner, pageToken string) (manager.Trash, error) {
	resp, err := c.c.ListTrash(ctx, &pb.ListTrashRequest{ActorId: actorID, Owner: ownerToProto(owner), PageToken: pageToken})
	if err != nil {
		return manager.Trash{}, fromStatus(err)
	}
	uses := make(map[string]int, len(resp.GetUses())) // never nil, as from the Manager
	for id, n := range resp.GetUses() {
		uses[id] = int(n)
	}
	return manager.Trash{Folders: foldersFromProto(resp.GetFolders()), Assets: assetPageFromProto(resp.GetAssets()), Uses: uses}, nil
}

// ---- Owner lifecycle

func (c *Client) DeleteOwned(ctx context.Context, owner ledger.Owner) (manager.OwnerDeletion, error) {
	resp, err := c.c.DeleteOwned(ctx, &pb.DeleteOwnedRequest{Owner: ownerToProto(owner)})
	if err != nil {
		return manager.OwnerDeletion{}, fromStatus(err)
	}
	return manager.OwnerDeletion{LedgerObjects: int(resp.GetLedgerObjects()), Trash: deletionFromProto(resp.GetTrash()), Uses: int(resp.GetUses()), Snapshots: int(resp.GetSnapshots())}, nil
}

func (c *Client) RemoveUser(ctx context.Context, userID, orgID string) (manager.UserRemoval, error) {
	resp, err := c.c.RemoveUser(ctx, &pb.RemoveUserRequest{UserId: userID, OrgId: orgID})
	if err != nil {
		return manager.UserRemoval{}, fromStatus(err)
	}
	r := manager.UserRemoval{
		ExpiredUploads: int(resp.GetExpiredUploads()),
		Transfer: dam.OwnerTransfer{
			Assets:  int(resp.GetTransferredAssets()),
			Folders: int(resp.GetTransferredFolders()),
			Renamed: int(resp.GetRenamedFolders()),
		},
		LedgerObjects: int(resp.GetLedgerObjects()),
		Deleted: manager.OwnerDeletion{
			LedgerObjects: int(resp.GetDeletedLedgerObjects()),
			Trash:         deletionFromProto(resp.GetDeleted()),
		},
		ClearedLedger:    int(resp.GetClearedLedger()),
		ClearedDAM:       int(resp.GetClearedDam()),
		ClearedSnapshots: int(resp.GetClearedSnapshots()),
	}
	if resp.GetTransferredTo() != nil {
		r.TransferredTo = ownerFromProto(resp.GetTransferredTo())
	}
	return r, nil
}

// ---- Asset refs, uses and copies

func (c *Client) ResolveRef(ctx context.Context, actorID, projectID string, ref dam.AssetRef) (manager.ResolvedRef, error) {
	resp, err := c.c.ResolveRef(ctx, &pb.ResolveRefRequest{ActorId: actorID, ProjectId: projectID, Ref: refToProto(ref)})
	if err != nil {
		return manager.ResolvedRef{}, fromStatus(err)
	}
	return manager.ResolvedRef{
		Asset:    assetFromProto(resp.GetAsset()),
		Version:  versionFromProto(resp.GetVersion()),
		Download: downloadFromProto(resp.GetDownload()),
	}, nil
}

func (c *Client) ForkAsset(ctx context.Context, actorID, projectID string, ref dam.AssetRef, target ledger.Owner, via string) (dam.Asset, error) {
	req := &pb.ForkAssetRequest{ActorId: actorID, ProjectId: projectID, Ref: refToProto(ref), ViaProjectId: via}
	if !target.IsZero() {
		req.Target = ownerToProto(target)
	}
	resp, err := c.c.ForkAsset(ctx, req)
	if err != nil {
		return dam.Asset{}, fromStatus(err)
	}
	return assetFromProto(resp.GetAsset()), nil
}

func (c *Client) RecordAssetUse(ctx context.Context, actorID, projectID string, ref dam.AssetRef) error {
	if _, err := c.c.RecordAssetUse(ctx, &pb.RecordAssetUseRequest{ActorId: actorID, ProjectId: projectID, Ref: refToProto(ref)}); err != nil {
		return fromStatus(err)
	}
	return nil
}

func (c *Client) ForgetAssetUse(ctx context.Context, actorID, projectID string, ref dam.AssetRef) error {
	if _, err := c.c.ForgetAssetUse(ctx, &pb.ForgetAssetUseRequest{ActorId: actorID, ProjectId: projectID, Ref: refToProto(ref)}); err != nil {
		return fromStatus(err)
	}
	return nil
}

func (c *Client) CopyProjectFiles(ctx context.Context, actorID, from, to string) (manager.CopyReport, error) {
	resp, err := c.c.CopyProjectFiles(ctx, &pb.CopyProjectFilesRequest{ActorId: actorID, FromProjectId: from, ToProjectId: to})
	if err != nil {
		return manager.CopyReport{}, fromStatus(err)
	}
	return manager.CopyReport{Folders: int(resp.GetFolders()), Assets: int(resp.GetAssets()), Uses: int(resp.GetUses())}, nil
}

// ---- Document snapshots

func (c *Client) BeginSnapshot(ctx context.Context, req manager.SnapshotRequest) (manager.SnapshotTicket, error) {
	resp, err := c.c.BeginSnapshot(ctx, &pb.BeginSnapshotRequest{
		ProjectId: req.ProjectID, Seq: req.Seq,
		EditedBy: req.EditedBy, EditedAt: timestamp(req.EditedAt),
		ContentType: req.ContentType, SizeBytes: req.SizeBytes,
	})
	if err != nil {
		return manager.SnapshotTicket{}, fromStatus(err)
	}
	return manager.SnapshotTicket{ObjectID: resp.GetObjectId(), URL: resp.GetUrl(), Headers: resp.GetHeaders(), ExpiresAt: timeOf(resp.GetExpiresAt())}, nil
}

func (c *Client) CompleteSnapshot(ctx context.Context, projectID, objectID string) (manager.SnapshotRef, error) {
	resp, err := c.c.CompleteSnapshot(ctx, &pb.CompleteSnapshotRequest{ProjectId: projectID, ObjectId: objectID})
	if err != nil {
		return manager.SnapshotRef{}, fromStatus(err)
	}
	return snapshotFromProto(resp.GetSnapshot()), nil
}

func (c *Client) LatestSnapshot(ctx context.Context, projectID string) (manager.SnapshotRef, error) {
	resp, err := c.c.GetLatestSnapshot(ctx, &pb.GetLatestSnapshotRequest{ProjectId: projectID})
	if err != nil {
		return manager.SnapshotRef{}, fromStatus(err)
	}
	return snapshotFromProto(resp.GetSnapshot()), nil
}

func (c *Client) ListSnapshots(ctx context.Context, projectID, pageToken string) (manager.SnapshotPage, error) {
	resp, err := c.c.ListSnapshots(ctx, &pb.ListSnapshotsRequest{ProjectId: projectID, PageToken: pageToken})
	if err != nil {
		return manager.SnapshotPage{}, fromStatus(err)
	}
	page := manager.SnapshotPage{NextPageToken: resp.GetNextPageToken()}
	for _, s := range resp.GetSnapshots() {
		page.Snapshots = append(page.Snapshots, snapshotFromProto(s))
	}
	return page, nil
}

func (c *Client) SnapshotURL(ctx context.Context, projectID, objectID string) (manager.Download, error) {
	resp, err := c.c.GetSnapshotURL(ctx, &pb.GetSnapshotURLRequest{ProjectId: projectID, ObjectId: objectID})
	if err != nil {
		return manager.Download{}, fromStatus(err)
	}
	return downloadFromProto(resp.GetDownload()), nil
}
