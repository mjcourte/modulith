package manager

import (
	"context"

	pb "gyoza/contracts/gen/go/gyoza/storage/v1"
)

// GRPCServer exposes a Service over gRPC. Register it with
// pb.RegisterStorageServiceServer.
type GRPCServer struct {
	// Embedded by value, as the generated code requires: an RPC added to the
	// proto but not yet here returns UNIMPLEMENTED instead of breaking the build.
	pb.UnimplementedStorageServiceServer
	svc Service
}

func NewGRPCServer(svc Service) *GRPCServer {
	return &GRPCServer{svc: svc}
}

// ---- Uploads

func (s *GRPCServer) BeginUpload(ctx context.Context, req *pb.BeginUploadRequest) (*pb.BeginUploadResponse, error) {
	t, err := s.svc.BeginUpload(ctx, req.GetActorId(), UploadRequest{
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

func (s *GRPCServer) CompleteUpload(ctx context.Context, req *pb.CompleteUploadRequest) (*pb.CompleteUploadResponse, error) {
	a, err := s.svc.CompleteUpload(ctx, req.GetActorId(), req.GetUploadId())
	if err != nil {
		return nil, toStatus(err)
	}
	return &pb.CompleteUploadResponse{Asset: assetToProto(a)}, nil
}

// ---- Downloads

func (s *GRPCServer) GetDownloadURL(ctx context.Context, req *pb.GetDownloadURLRequest) (*pb.GetDownloadURLResponse, error) {
	d, err := s.svc.DownloadURL(ctx, req.GetActorId(), req.GetAssetId(), int(req.GetVersion()))
	if err != nil {
		return nil, toStatus(err)
	}
	return &pb.GetDownloadURLResponse{Download: downloadToProto(d)}, nil
}

func (s *GRPCServer) GetExportURL(ctx context.Context, req *pb.GetExportURLRequest) (*pb.GetExportURLResponse, error) {
	d, err := s.svc.ExportURL(ctx, req.GetActorId(), req.GetObjectId())
	if err != nil {
		return nil, toStatus(err)
	}
	return &pb.GetExportURLResponse{Download: downloadToProto(d)}, nil
}

// ---- Assets

func (s *GRPCServer) GetAsset(ctx context.Context, req *pb.GetAssetRequest) (*pb.GetAssetResponse, error) {
	a, err := s.svc.GetAsset(ctx, req.GetActorId(), req.GetId())
	if err != nil {
		return nil, toStatus(err)
	}
	return &pb.GetAssetResponse{Asset: assetToProto(a)}, nil
}

func (s *GRPCServer) ListAssets(ctx context.Context, req *pb.ListAssetsRequest) (*pb.ListAssetsResponse, error) {
	q := AssetQuery{
		ProjectID:      req.GetProjectId(),
		Owner:          ownerFromProto(req.GetOwner()),
		Tag:            req.GetTag(),
		Category:       Category(req.GetCategory()),
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

func (s *GRPCServer) UpdateAsset(ctx context.Context, req *pb.UpdateAssetRequest) (*pb.UpdateAssetResponse, error) {
	u := AssetUpdate{Name: req.Name, Description: req.Description, FolderID: req.FolderId}
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

func (s *GRPCServer) DeleteAsset(ctx context.Context, req *pb.DeleteAssetRequest) (*pb.DeleteAssetResponse, error) {
	if err := s.svc.DeleteAsset(ctx, req.GetActorId(), req.GetId()); err != nil {
		return nil, toStatus(err)
	}
	return &pb.DeleteAssetResponse{}, nil
}

func (s *GRPCServer) RestoreAsset(ctx context.Context, req *pb.RestoreAssetRequest) (*pb.RestoreAssetResponse, error) {
	a, err := s.svc.RestoreAsset(ctx, req.GetActorId(), req.GetId())
	if err != nil {
		return nil, toStatus(err)
	}
	return &pb.RestoreAssetResponse{Asset: assetToProto(a)}, nil
}

func (s *GRPCServer) ListVersions(ctx context.Context, req *pb.ListVersionsRequest) (*pb.ListVersionsResponse, error) {
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

func (s *GRPCServer) RevertAsset(ctx context.Context, req *pb.RevertAssetRequest) (*pb.RevertAssetResponse, error) {
	a, err := s.svc.RevertAsset(ctx, req.GetActorId(), req.GetAssetId(), int(req.GetVersion()))
	if err != nil {
		return nil, toStatus(err)
	}
	return &pb.RevertAssetResponse{Asset: assetToProto(a)}, nil
}

// ---- Folders and the trash

func (s *GRPCServer) CreateFolder(ctx context.Context, req *pb.CreateFolderRequest) (*pb.CreateFolderResponse, error) {
	f, err := s.svc.CreateFolder(ctx, req.GetActorId(), Folder{
		Owner: ownerFromProto(req.GetOwner()), ParentID: req.GetParentId(), Name: req.GetName(),
	})
	if err != nil {
		return nil, toStatus(err)
	}
	return &pb.CreateFolderResponse{Folder: folderToProto(f)}, nil
}

func (s *GRPCServer) RenameFolder(ctx context.Context, req *pb.RenameFolderRequest) (*pb.RenameFolderResponse, error) {
	f, err := s.svc.RenameFolder(ctx, req.GetActorId(), req.GetId(), req.GetName())
	if err != nil {
		return nil, toStatus(err)
	}
	return &pb.RenameFolderResponse{Folder: folderToProto(f)}, nil
}

func (s *GRPCServer) MoveFolder(ctx context.Context, req *pb.MoveFolderRequest) (*pb.MoveFolderResponse, error) {
	f, err := s.svc.MoveFolder(ctx, req.GetActorId(), req.GetId(), req.GetNewParentId())
	if err != nil {
		return nil, toStatus(err)
	}
	return &pb.MoveFolderResponse{Folder: folderToProto(f)}, nil
}

func (s *GRPCServer) DeleteFolder(ctx context.Context, req *pb.DeleteFolderRequest) (*pb.DeleteFolderResponse, error) {
	d, err := s.svc.DeleteFolder(ctx, req.GetActorId(), req.GetId())
	if err != nil {
		return nil, toStatus(err)
	}
	return &pb.DeleteFolderResponse{Deletion: deletionToProto(d)}, nil
}

func (s *GRPCServer) DeleteFolderTree(ctx context.Context, req *pb.DeleteFolderTreeRequest) (*pb.DeleteFolderTreeResponse, error) {
	d, err := s.svc.DeleteFolderTree(ctx, req.GetActorId(), req.GetId())
	if err != nil {
		return nil, toStatus(err)
	}
	return &pb.DeleteFolderTreeResponse{Deletion: deletionToProto(d)}, nil
}

func (s *GRPCServer) RestoreFolder(ctx context.Context, req *pb.RestoreFolderRequest) (*pb.RestoreFolderResponse, error) {
	d, err := s.svc.RestoreFolder(ctx, req.GetActorId(), req.GetId())
	if err != nil {
		return nil, toStatus(err)
	}
	return &pb.RestoreFolderResponse{Deletion: deletionToProto(d)}, nil
}

func (s *GRPCServer) ListFolderContents(ctx context.Context, req *pb.ListFolderContentsRequest) (*pb.ListFolderContentsResponse, error) {
	c, err := s.svc.ListFolderContents(ctx, req.GetActorId(), ownerFromProto(req.GetOwner()), req.GetFolderId(), req.GetPageToken())
	if err != nil {
		return nil, toStatus(err)
	}
	return &pb.ListFolderContentsResponse{
		Path: foldersToProto(c.Path), Folders: foldersToProto(c.Folders), Assets: assetPageToProto(c.Assets),
	}, nil
}

func (s *GRPCServer) GetFolderPath(ctx context.Context, req *pb.GetFolderPathRequest) (*pb.GetFolderPathResponse, error) {
	path, err := s.svc.FolderPath(ctx, req.GetActorId(), req.GetId())
	if err != nil {
		return nil, toStatus(err)
	}
	return &pb.GetFolderPathResponse{Path: foldersToProto(path)}, nil
}

func (s *GRPCServer) ListTrash(ctx context.Context, req *pb.ListTrashRequest) (*pb.ListTrashResponse, error) {
	t, err := s.svc.ListTrash(ctx, req.GetActorId(), ownerFromProto(req.GetOwner()), req.GetPageToken())
	if err != nil {
		return nil, toStatus(err)
	}
	return &pb.ListTrashResponse{Folders: foldersToProto(t.Folders), Assets: assetPageToProto(t.Assets)}, nil
}

// ---- Owner lifecycle

func (s *GRPCServer) DeleteOwned(ctx context.Context, req *pb.DeleteOwnedRequest) (*pb.DeleteOwnedResponse, error) {
	d, err := s.svc.DeleteOwned(ctx, ownerFromProto(req.GetOwner()))
	if err != nil {
		return nil, toStatus(err)
	}
	return &pb.DeleteOwnedResponse{LedgerObjects: int32(d.LedgerObjects), Trash: deletionToProto(d.Trash)}, nil
}

func (s *GRPCServer) RemoveUser(ctx context.Context, req *pb.RemoveUserRequest) (*pb.RemoveUserResponse, error) {
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
	}
	if !r.TransferredTo.IsZero() {
		resp.TransferredTo = ownerToProto(r.TransferredTo)
	}
	return resp, nil
}
