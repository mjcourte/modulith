package grpcapi

import (
	"context"

	pb "github.com/mjcourte/modulith/contracts/gen/go/modulith/storage/v1"
	"github.com/mjcourte/modulith/services/storage/internal/dam"
	"github.com/mjcourte/modulith/services/storage/internal/manager"
)

// Server exposes a Service over gRPC. Register it with
// pb.RegisterStorageServiceServer.
type Server struct {
	// Embedded by value, as the generated code requires: an RPC added to the
	// proto but not yet here returns UNIMPLEMENTED instead of breaking the build.
	pb.UnimplementedStorageServiceServer
	svc manager.Service
}

func NewServer(svc manager.Service) *Server {
	return &Server{svc: svc}
}

// ---- Uploads

func (s *Server) BeginUpload(ctx context.Context, req *pb.BeginUploadRequest) (*pb.BeginUploadResponse, error) {
	t, err := s.svc.BeginUpload(ctx, req.GetActorId(), manager.UploadRequest{
		Owner:       ownerFromProto(req.GetOwner()),
		ProjectID:   req.GetProjectId(),
		AssetID:     req.GetAssetId(),
		Name:        req.GetName(),
		Description: req.GetDescription(),
		Tags:        req.GetTags(),
		FolderID:    req.GetFolderId(),
		ContentType: req.GetContentType(),
		SizeBytes:   req.GetSizeBytes(),
	})
	if err != nil {
		return nil, toStatus(err)
	}
	return &pb.BeginUploadResponse{
		UploadId: t.UploadID, Method: t.Method, Url: t.URL, Headers: t.Headers, ExpiresAt: timestamp(t.ExpiresAt),
	}, nil
}

func (s *Server) CompleteUpload(ctx context.Context, req *pb.CompleteUploadRequest) (*pb.CompleteUploadResponse, error) {
	a, err := s.svc.CompleteUpload(ctx, req.GetActorId(), req.GetUploadId())
	if err != nil {
		return nil, toStatus(err)
	}
	return &pb.CompleteUploadResponse{Asset: assetToProto(a)}, nil
}

// ---- Downloads

func (s *Server) GetDownloadURL(ctx context.Context, req *pb.GetDownloadURLRequest) (*pb.GetDownloadURLResponse, error) {
	d, err := s.svc.DownloadURL(ctx, req.GetActorId(), req.GetAssetId(), int(req.GetVersion()))
	if err != nil {
		return nil, toStatus(err)
	}
	return &pb.GetDownloadURLResponse{Download: downloadToProto(d)}, nil
}

func (s *Server) GetExportURL(ctx context.Context, req *pb.GetExportURLRequest) (*pb.GetExportURLResponse, error) {
	d, err := s.svc.ExportURL(ctx, req.GetActorId(), req.GetObjectId())
	if err != nil {
		return nil, toStatus(err)
	}
	return &pb.GetExportURLResponse{Download: downloadToProto(d)}, nil
}

// ---- Assets

func (s *Server) GetAsset(ctx context.Context, req *pb.GetAssetRequest) (*pb.GetAssetResponse, error) {
	a, err := s.svc.GetAsset(ctx, req.GetActorId(), req.GetId())
	if err != nil {
		return nil, toStatus(err)
	}
	return &pb.GetAssetResponse{Asset: assetToProto(a)}, nil
}

func (s *Server) ListAssets(ctx context.Context, req *pb.ListAssetsRequest) (*pb.ListAssetsResponse, error) {
	q := dam.AssetQuery{
		ProjectID:      req.GetProjectId(),
		Owner:          ownerFromProto(req.GetOwner()),
		Tag:            req.GetTag(),
		Category:       dam.Category(req.GetCategory()),
		Folder:         req.FolderId, // optional field: a *string, nil when unset
		IncludeDeleted: req.GetIncludeDeleted(),
		OnlyDeleted:    req.GetOnlyDeleted(),
		PageSize:       int(req.GetPageSize()),
		PageToken:      req.GetPageToken(),
	}
	page, err := s.svc.ListAssets(ctx, req.GetActorId(), q)
	if err != nil {
		return nil, toStatus(err)
	}
	return &pb.ListAssetsResponse{Page: assetPageToProto(page)}, nil
}

func (s *Server) UpdateAsset(ctx context.Context, req *pb.UpdateAssetRequest) (*pb.UpdateAssetResponse, error) {
	u := dam.AssetUpdate{Name: req.Name, Description: req.Description, FolderID: req.FolderId}
	if req.Tags != nil { // set, possibly to an empty list
		tags := req.GetTags().GetTags()
		u.Tags = &tags
	}
	a, err := s.svc.UpdateAsset(ctx, req.GetActorId(), req.GetId(), u)
	if err != nil {
		return nil, toStatus(err)
	}
	return &pb.UpdateAssetResponse{Asset: assetToProto(a)}, nil
}

func (s *Server) DeleteAsset(ctx context.Context, req *pb.DeleteAssetRequest) (*pb.DeleteAssetResponse, error) {
	if err := s.svc.DeleteAsset(ctx, req.GetActorId(), req.GetId()); err != nil {
		return nil, toStatus(err)
	}
	return &pb.DeleteAssetResponse{}, nil
}

func (s *Server) RestoreAsset(ctx context.Context, req *pb.RestoreAssetRequest) (*pb.RestoreAssetResponse, error) {
	a, err := s.svc.RestoreAsset(ctx, req.GetActorId(), req.GetId())
	if err != nil {
		return nil, toStatus(err)
	}
	return &pb.RestoreAssetResponse{Asset: assetToProto(a)}, nil
}

func (s *Server) ListVersions(ctx context.Context, req *pb.ListVersionsRequest) (*pb.ListVersionsResponse, error) {
	vs, err := s.svc.ListVersions(ctx, req.GetActorId(), req.GetAssetId())
	if err != nil {
		return nil, toStatus(err)
	}
	resp := &pb.ListVersionsResponse{Versions: make([]*pb.Version, len(vs))}
	for i, v := range vs {
		resp.Versions[i] = versionToProto(v)
	}
	return resp, nil
}

func (s *Server) RevertAsset(ctx context.Context, req *pb.RevertAssetRequest) (*pb.RevertAssetResponse, error) {
	a, err := s.svc.RevertAsset(ctx, req.GetActorId(), req.GetAssetId(), int(req.GetVersion()))
	if err != nil {
		return nil, toStatus(err)
	}
	return &pb.RevertAssetResponse{Asset: assetToProto(a)}, nil
}

// ---- Folders and the trash

func (s *Server) CreateFolder(ctx context.Context, req *pb.CreateFolderRequest) (*pb.CreateFolderResponse, error) {
	f, err := s.svc.CreateFolder(ctx, req.GetActorId(), dam.Folder{
		Owner: ownerFromProto(req.GetOwner()), ParentID: req.GetParentId(), Name: req.GetName(),
	})
	if err != nil {
		return nil, toStatus(err)
	}
	return &pb.CreateFolderResponse{Folder: folderToProto(f)}, nil
}

func (s *Server) RenameFolder(ctx context.Context, req *pb.RenameFolderRequest) (*pb.RenameFolderResponse, error) {
	f, err := s.svc.RenameFolder(ctx, req.GetActorId(), req.GetId(), req.GetName())
	if err != nil {
		return nil, toStatus(err)
	}
	return &pb.RenameFolderResponse{Folder: folderToProto(f)}, nil
}

func (s *Server) MoveFolder(ctx context.Context, req *pb.MoveFolderRequest) (*pb.MoveFolderResponse, error) {
	f, err := s.svc.MoveFolder(ctx, req.GetActorId(), req.GetId(), req.GetNewParentId())
	if err != nil {
		return nil, toStatus(err)
	}
	return &pb.MoveFolderResponse{Folder: folderToProto(f)}, nil
}

func (s *Server) DeleteFolder(ctx context.Context, req *pb.DeleteFolderRequest) (*pb.DeleteFolderResponse, error) {
	d, err := s.svc.DeleteFolder(ctx, req.GetActorId(), req.GetId())
	if err != nil {
		return nil, toStatus(err)
	}
	return &pb.DeleteFolderResponse{Deletion: deletionToProto(d)}, nil
}

func (s *Server) DeleteFolderTree(ctx context.Context, req *pb.DeleteFolderTreeRequest) (*pb.DeleteFolderTreeResponse, error) {
	d, err := s.svc.DeleteFolderTree(ctx, req.GetActorId(), req.GetId())
	if err != nil {
		return nil, toStatus(err)
	}
	return &pb.DeleteFolderTreeResponse{Deletion: deletionToProto(d)}, nil
}

func (s *Server) RestoreFolder(ctx context.Context, req *pb.RestoreFolderRequest) (*pb.RestoreFolderResponse, error) {
	d, err := s.svc.RestoreFolder(ctx, req.GetActorId(), req.GetId())
	if err != nil {
		return nil, toStatus(err)
	}
	return &pb.RestoreFolderResponse{Deletion: deletionToProto(d)}, nil
}

func (s *Server) ListFolderContents(ctx context.Context, req *pb.ListFolderContentsRequest) (*pb.ListFolderContentsResponse, error) {
	c, err := s.svc.ListFolderContents(ctx, req.GetActorId(), ownerFromProto(req.GetOwner()), req.GetFolderId(), req.GetPageToken())
	if err != nil {
		return nil, toStatus(err)
	}
	return &pb.ListFolderContentsResponse{
		Path: foldersToProto(c.Path), Folders: foldersToProto(c.Folders), Assets: assetPageToProto(c.Assets),
	}, nil
}

func (s *Server) GetFolderPath(ctx context.Context, req *pb.GetFolderPathRequest) (*pb.GetFolderPathResponse, error) {
	path, err := s.svc.FolderPath(ctx, req.GetActorId(), req.GetId())
	if err != nil {
		return nil, toStatus(err)
	}
	return &pb.GetFolderPathResponse{Path: foldersToProto(path)}, nil
}

func (s *Server) ListTrash(ctx context.Context, req *pb.ListTrashRequest) (*pb.ListTrashResponse, error) {
	t, err := s.svc.ListTrash(ctx, req.GetActorId(), ownerFromProto(req.GetOwner()), req.GetPageToken())
	if err != nil {
		return nil, toStatus(err)
	}
	uses := make(map[string]int32, len(t.Uses))
	for id, n := range t.Uses {
		uses[id] = int32(n)
	}
	return &pb.ListTrashResponse{Folders: foldersToProto(t.Folders), Assets: assetPageToProto(t.Assets), Uses: uses}, nil
}

// ---- Owner lifecycle

func (s *Server) DeleteOwned(ctx context.Context, req *pb.DeleteOwnedRequest) (*pb.DeleteOwnedResponse, error) {
	d, err := s.svc.DeleteOwned(ctx, ownerFromProto(req.GetOwner()))
	if err != nil {
		return nil, toStatus(err)
	}
	return &pb.DeleteOwnedResponse{LedgerObjects: int32(d.LedgerObjects), Trash: deletionToProto(d.Trash), Uses: int32(d.Uses), Snapshots: int32(d.Snapshots)}, nil
}

func (s *Server) RemoveUser(ctx context.Context, req *pb.RemoveUserRequest) (*pb.RemoveUserResponse, error) {
	r, err := s.svc.RemoveUser(ctx, req.GetUserId(), req.GetOrgId())
	if err != nil {
		return nil, toStatus(err)
	}
	resp := &pb.RemoveUserResponse{
		ExpiredUploads:       int32(r.ExpiredUploads),
		TransferredAssets:    int32(r.Transfer.Assets),
		TransferredFolders:   int32(r.Transfer.Folders),
		RenamedFolders:       int32(r.Transfer.Renamed),
		LedgerObjects:        int32(r.LedgerObjects),
		DeletedLedgerObjects: int32(r.Deleted.LedgerObjects),
		Deleted:              deletionToProto(r.Deleted.Trash),
		ClearedLedger:        int32(r.ClearedLedger),
		ClearedDam:           int32(r.ClearedDAM),
		ClearedSnapshots:     int32(r.ClearedSnapshots),
	}
	if !r.TransferredTo.IsZero() {
		resp.TransferredTo = ownerToProto(r.TransferredTo)
	}
	return resp, nil
}

// ---- Asset refs, uses and copies

func (s *Server) ResolveRef(ctx context.Context, req *pb.ResolveRefRequest) (*pb.ResolveRefResponse, error) {
	r, err := s.svc.ResolveRef(ctx, req.GetActorId(), req.GetProjectId(), refFromProto(req.GetRef()))
	if err != nil {
		return nil, toStatus(err)
	}
	return &pb.ResolveRefResponse{Asset: assetToProto(r.Asset), Version: versionToProto(r.Version), Download: downloadToProto(r.Download)}, nil
}

func (s *Server) ForkAsset(ctx context.Context, req *pb.ForkAssetRequest) (*pb.ForkAssetResponse, error) {
	a, err := s.svc.ForkAsset(ctx, req.GetActorId(), req.GetProjectId(), refFromProto(req.GetRef()), ownerFromProto(req.GetTarget()), req.GetViaProjectId())
	if err != nil {
		return nil, toStatus(err)
	}
	return &pb.ForkAssetResponse{Asset: assetToProto(a)}, nil
}

func (s *Server) RecordAssetUse(ctx context.Context, req *pb.RecordAssetUseRequest) (*pb.RecordAssetUseResponse, error) {
	if err := s.svc.RecordAssetUse(ctx, req.GetActorId(), req.GetProjectId(), refFromProto(req.GetRef())); err != nil {
		return nil, toStatus(err)
	}
	return &pb.RecordAssetUseResponse{}, nil
}

func (s *Server) ForgetAssetUse(ctx context.Context, req *pb.ForgetAssetUseRequest) (*pb.ForgetAssetUseResponse, error) {
	if err := s.svc.ForgetAssetUse(ctx, req.GetActorId(), req.GetProjectId(), refFromProto(req.GetRef())); err != nil {
		return nil, toStatus(err)
	}
	return &pb.ForgetAssetUseResponse{}, nil
}

func (s *Server) CopyProjectFiles(ctx context.Context, req *pb.CopyProjectFilesRequest) (*pb.CopyProjectFilesResponse, error) {
	r, err := s.svc.CopyProjectFiles(ctx, req.GetActorId(), req.GetFromProjectId(), req.GetToProjectId())
	if err != nil {
		return nil, toStatus(err)
	}
	return &pb.CopyProjectFilesResponse{Folders: int32(r.Folders), Assets: int32(r.Assets), Uses: int32(r.Uses)}, nil
}

// ---- Document snapshots

func (s *Server) BeginSnapshot(ctx context.Context, req *pb.BeginSnapshotRequest) (*pb.BeginSnapshotResponse, error) {
	t, err := s.svc.BeginSnapshot(ctx, manager.SnapshotRequest{
		ProjectID: req.GetProjectId(), Seq: req.GetSeq(),
		EditedBy: req.GetEditedBy(), EditedAt: timeOf(req.GetEditedAt()),
		ContentType: req.GetContentType(), SizeBytes: req.GetSizeBytes(),
	})
	if err != nil {
		return nil, toStatus(err)
	}
	return &pb.BeginSnapshotResponse{ObjectId: t.ObjectID, Url: t.URL, Headers: t.Headers, ExpiresAt: timestamp(t.ExpiresAt)}, nil
}

func (s *Server) CompleteSnapshot(ctx context.Context, req *pb.CompleteSnapshotRequest) (*pb.CompleteSnapshotResponse, error) {
	r, err := s.svc.CompleteSnapshot(ctx, req.GetProjectId(), req.GetObjectId())
	if err != nil {
		return nil, toStatus(err)
	}
	return &pb.CompleteSnapshotResponse{Snapshot: snapshotToProto(r)}, nil
}

func (s *Server) GetLatestSnapshot(ctx context.Context, req *pb.GetLatestSnapshotRequest) (*pb.GetLatestSnapshotResponse, error) {
	r, err := s.svc.LatestSnapshot(ctx, req.GetProjectId())
	if err != nil {
		return nil, toStatus(err)
	}
	return &pb.GetLatestSnapshotResponse{Snapshot: snapshotToProto(r)}, nil
}

func (s *Server) ListSnapshots(ctx context.Context, req *pb.ListSnapshotsRequest) (*pb.ListSnapshotsResponse, error) {
	page, err := s.svc.ListSnapshots(ctx, req.GetProjectId(), req.GetPageToken())
	if err != nil {
		return nil, toStatus(err)
	}
	resp := &pb.ListSnapshotsResponse{NextPageToken: page.NextPageToken}
	for _, r := range page.Snapshots {
		resp.Snapshots = append(resp.Snapshots, snapshotToProto(r))
	}
	return resp, nil
}

func (s *Server) GetSnapshotURL(ctx context.Context, req *pb.GetSnapshotURLRequest) (*pb.GetSnapshotURLResponse, error) {
	d, err := s.svc.SnapshotURL(ctx, req.GetProjectId(), req.GetObjectId())
	if err != nil {
		return nil, toStatus(err)
	}
	return &pb.GetSnapshotURLResponse{Download: downloadToProto(d)}, nil
}
