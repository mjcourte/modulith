package grpcapi

// Conversions between the domain types and the protobuf messages, and between
// domain errors and gRPC statuses. Shared by the gRPC server and client.

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	pb "github.com/mjcourte/modulith/contracts/gen/go/modulith/storage/v1"
	"github.com/mjcourte/modulith/services/storage/internal/dam"
	"github.com/mjcourte/modulith/services/storage/internal/ledger"
	"github.com/mjcourte/modulith/services/storage/internal/manager"
)

// ---- Errors

// errorDomain identifies this service in google.rpc.ErrorInfo details.
const errorDomain = "storage.modulith"

// wireErrors lists every sentinel that may cross the API, with its status
// code and its stable name on the wire. Several share a code, so the name
// (ErrorInfo's "reason") is what tells the client which one it was. Both
// directions use this one table, so they can't disagree.
var wireErrors = []struct {
	err    error
	code   codes.Code
	reason string
}{
	{dam.ErrAssetNotFound, codes.NotFound, "ASSET_NOT_FOUND"},
	{dam.ErrVersionNotFound, codes.NotFound, "VERSION_NOT_FOUND"},
	{dam.ErrFolderNotFound, codes.NotFound, "FOLDER_NOT_FOUND"},
	{dam.ErrUploadNotFound, codes.NotFound, "UPLOAD_NOT_FOUND"},
	{manager.ErrExportNotFound, codes.NotFound, "EXPORT_NOT_FOUND"},
	{dam.ErrInvalidAsset, codes.InvalidArgument, "INVALID_ASSET"},
	{dam.ErrInvalidQuery, codes.InvalidArgument, "INVALID_QUERY"},
	{dam.ErrInvalidFolder, codes.InvalidArgument, "INVALID_FOLDER"},
	{dam.ErrInvalidUpload, codes.InvalidArgument, "INVALID_UPLOAD"},
	{manager.ErrContentMismatch, codes.InvalidArgument, "CONTENT_MISMATCH"},
	{manager.ErrUploadTooLarge, codes.InvalidArgument, "UPLOAD_TOO_LARGE"},
	{manager.ErrForbidden, codes.PermissionDenied, "FORBIDDEN"},
	{manager.ErrForkRequired, codes.PermissionDenied, "FORK_REQUIRED"}, // always with FORBIDDEN
	{dam.ErrNameTaken, codes.AlreadyExists, "NAME_TAKEN"},
	{dam.ErrFolderNotEmpty, codes.FailedPrecondition, "FOLDER_NOT_EMPTY"},
	{dam.ErrUploadExpired, codes.FailedPrecondition, "UPLOAD_EXPIRED"},
	{manager.ErrNotUploaded, codes.FailedPrecondition, "NOT_UPLOADED"},
	{manager.ErrSnapshotNotFound, codes.NotFound, "SNAPSHOT_NOT_FOUND"},
	{manager.ErrInvalidSnapshot, codes.InvalidArgument, "INVALID_SNAPSHOT"},
	{manager.ErrSnapshotTooLarge, codes.InvalidArgument, "SNAPSHOT_TOO_LARGE"},
	{manager.ErrSnapshotStale, codes.FailedPrecondition, "STALE"},
	{manager.ErrSnapshotExpired, codes.FailedPrecondition, "SNAPSHOT_EXPIRED"},
}

// toStatus turns a Service error into a gRPC status for the server to return.
// An error can match several sentinels (e.g. an invalid upload's name is both
// ErrInvalidUpload and ErrInvalidAsset): each gets an ErrorInfo detail, and the
// first decides the code. Unexpected errors are logged here and reach the
// caller only as "internal error", so database details never leak.
func toStatus(err error) error {
	var details []*errdetails.ErrorInfo
	code := codes.Unknown
	for _, w := range wireErrors {
		if !errors.Is(err, w.err) {
			continue
		}
		info := &errdetails.ErrorInfo{Domain: errorDomain, Reason: w.reason}
		var notEmpty *dam.FolderNotEmptyError
		if w.err == dam.ErrFolderNotEmpty && errors.As(err, &notEmpty) {
			info.Metadata = map[string]string{
				"folders": strconv.Itoa(notEmpty.Folders),
				"assets":  strconv.Itoa(notEmpty.Assets),
			}
		}
		if len(details) == 0 {
			code = w.code
		}
		details = append(details, info)
	}
	switch {
	case len(details) > 0:
		st := status.New(code, err.Error())
		for _, d := range details {
			if withDetail, err := st.WithDetails(d); err == nil {
				st = withDetail
			}
		}
		return st.Err()
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, err.Error())
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, err.Error())
	default:
		slog.Error("storage: internal error", "err", err)
		return status.Error(codes.Internal, "internal error")
	}
}

// remoteError is a server error on the client side. It keeps the server's
// message and matches the same sentinels with errors.Is (and a
// *FolderNotEmptyError with errors.As) as the original did, so callers handle
// errors exactly as they would with an in-process Service.
type remoteError struct {
	msg  string
	errs []error
}

func (e *remoteError) Error() string { return e.msg }

// Unwrap returning a slice (Go 1.20+) lets errors.Is and errors.As check every
// error in it, as with errors.Join or fmt.Errorf with several %w.
func (e *remoteError) Unwrap() []error { return e.errs }

// fromStatus turns a gRPC error back into a domain error.
func fromStatus(err error) error {
	st, ok := status.FromError(err)
	if !ok {
		return err // not from gRPC
	}
	var errs []error
	for _, d := range st.Details() {
		info, ok := d.(*errdetails.ErrorInfo)
		if !ok || info.GetDomain() != errorDomain {
			continue
		}
		for _, w := range wireErrors {
			if w.reason != info.GetReason() {
				continue
			}
			if w.err == dam.ErrFolderNotEmpty {
				// Atoi failing leaves 0: the counts are only for display.
				folders, _ := strconv.Atoi(info.GetMetadata()["folders"])
				assets, _ := strconv.Atoi(info.GetMetadata()["assets"])
				errs = append(errs, &dam.FolderNotEmptyError{Folders: folders, Assets: assets})
			} else {
				errs = append(errs, w.err)
			}
		}
	}
	switch {
	case len(errs) > 0:
		return &remoteError{msg: st.Message(), errs: errs}
	case st.Code() == codes.Canceled:
		return &remoteError{msg: st.Message(), errs: []error{context.Canceled, err}}
	case st.Code() == codes.DeadlineExceeded:
		return &remoteError{msg: st.Message(), errs: []error{context.DeadlineExceeded, err}}
	default:
		return err
	}
}

// ---- Times

// timestamp converts t, leaving the zero time unset.
func timestamp(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() {
		return nil
	}
	return timestamppb.New(t)
}

func optTimestamp(t *time.Time) *timestamppb.Timestamp {
	if t == nil {
		return nil
	}
	return timestamppb.New(*t)
}

// timeOf converts p, with unset meaning the zero time. AsTime returns UTC.
func timeOf(p *timestamppb.Timestamp) time.Time {
	if p == nil {
		return time.Time{}
	}
	return p.AsTime()
}

func optTimeOf(p *timestamppb.Timestamp) *time.Time {
	if p == nil {
		return nil
	}
	t := p.AsTime()
	return &t
}

// ---- Assets and versions

func assetToProto(a dam.Asset) *pb.Asset {
	return &pb.Asset{
		Id:             a.ID,
		Owner:          ownerToProto(a.Owner),
		ProjectId:      a.ProjectID,
		FolderId:       a.FolderID,
		Name:           a.Name,
		Description:    a.Description,
		Tags:           a.Tags,
		ContentType:    a.ContentType,
		Category:       string(a.Category),
		CurrentVersion: int32(a.CurrentVersion),
		CreatedBy:      a.CreatedBy,
		CreatedAt:      timestamp(a.CreatedAt),
		UpdatedAt:      timestamp(a.UpdatedAt),
		DeletedAt:      optTimestamp(a.DeletedAt),
		DeletionId:     a.DeletionID,
	}
}

// assetFromProto accepts nil, since getters on a missing message return nil.
func assetFromProto(p *pb.Asset) dam.Asset {
	return dam.Asset{
		ID:             p.GetId(),
		Owner:          ownerFromProto(p.GetOwner()),
		ProjectID:      p.GetProjectId(),
		FolderID:       p.GetFolderId(),
		Name:           p.GetName(),
		Description:    p.GetDescription(),
		Tags:           p.GetTags(),
		ContentType:    p.GetContentType(),
		Category:       dam.Category(p.GetCategory()),
		CurrentVersion: int(p.GetCurrentVersion()),
		CreatedBy:      p.GetCreatedBy(),
		CreatedAt:      timeOf(p.GetCreatedAt()),
		UpdatedAt:      timeOf(p.GetUpdatedAt()),
		DeletedAt:      optTimeOf(p.GetDeletedAt()),
		DeletionID:     p.GetDeletionId(),
	}
}

func assetPageToProto(page dam.AssetPage) *pb.AssetPage {
	p := &pb.AssetPage{Assets: make([]*pb.Asset, len(page.Assets)), NextPageToken: page.NextPageToken}
	for i, a := range page.Assets {
		p.Assets[i] = assetToProto(a)
	}
	return p
}

func assetPageFromProto(p *pb.AssetPage) dam.AssetPage {
	page := dam.AssetPage{NextPageToken: p.GetNextPageToken()}
	for _, a := range p.GetAssets() {
		page.Assets = append(page.Assets, assetFromProto(a))
	}
	return page
}

func versionToProto(v dam.Version) *pb.Version {
	return &pb.Version{
		AssetId:     v.AssetID,
		Number:      int32(v.Number),
		ObjectId:    v.ObjectID,
		ContentType: v.ContentType,
		SizeBytes:   v.SizeBytes,
		CreatedBy:   v.CreatedBy,
		CreatedAt:   timestamp(v.CreatedAt),
	}
}

func versionFromProto(p *pb.Version) dam.Version {
	return dam.Version{
		AssetID:     p.GetAssetId(),
		Number:      int(p.GetNumber()),
		ObjectID:    p.GetObjectId(),
		ContentType: p.GetContentType(),
		SizeBytes:   p.GetSizeBytes(),
		CreatedBy:   p.GetCreatedBy(),
		CreatedAt:   timeOf(p.GetCreatedAt()),
	}
}

// ---- Folders

func folderToProto(f dam.Folder) *pb.Folder {
	return &pb.Folder{
		Id:         f.ID,
		Owner:      ownerToProto(f.Owner),
		ParentId:   f.ParentID,
		Name:       f.Name,
		CreatedBy:  f.CreatedBy,
		CreatedAt:  timestamp(f.CreatedAt),
		UpdatedAt:  timestamp(f.UpdatedAt),
		DeletedAt:  optTimestamp(f.DeletedAt),
		DeletionId: f.DeletionID,
	}
}

func folderFromProto(p *pb.Folder) dam.Folder {
	return dam.Folder{
		ID:         p.GetId(),
		Owner:      ownerFromProto(p.GetOwner()),
		ParentID:   p.GetParentId(),
		Name:       p.GetName(),
		CreatedBy:  p.GetCreatedBy(),
		CreatedAt:  timeOf(p.GetCreatedAt()),
		UpdatedAt:  timeOf(p.GetUpdatedAt()),
		DeletedAt:  optTimeOf(p.GetDeletedAt()),
		DeletionID: p.GetDeletionId(),
	}
}

func foldersToProto(fs []dam.Folder) []*pb.Folder {
	out := make([]*pb.Folder, len(fs))
	for i, f := range fs {
		out[i] = folderToProto(f)
	}
	return out
}

func foldersFromProto(ps []*pb.Folder) []dam.Folder {
	var out []dam.Folder
	for _, p := range ps {
		out = append(out, folderFromProto(p))
	}
	return out
}

func deletionToProto(d dam.FolderDeletion) *pb.FolderDeletion {
	return &pb.FolderDeletion{DeletionId: d.DeletionID, Folders: int32(d.Folders), Assets: int32(d.Assets)}
}

func deletionFromProto(p *pb.FolderDeletion) dam.FolderDeletion {
	return dam.FolderDeletion{DeletionID: p.GetDeletionId(), Folders: int(p.GetFolders()), Assets: int(p.GetAssets())}
}

// ---- Downloads

func downloadToProto(d manager.Download) *pb.Download {
	return &pb.Download{Url: d.URL, Filename: d.Filename, ExpiresAt: timestamp(d.ExpiresAt)}
}

func downloadFromProto(p *pb.Download) manager.Download {
	return manager.Download{URL: p.GetUrl(), Filename: p.GetFilename(), ExpiresAt: timeOf(p.GetExpiresAt())}
}

var ownerKindToProto = map[ledger.OwnerKind]pb.OwnerKind{
	ledger.OwnerProject: pb.OwnerKind_OWNER_KIND_PROJECT,
	ledger.OwnerUser:    pb.OwnerKind_OWNER_KIND_USER,
	ledger.OwnerOrg:     pb.OwnerKind_OWNER_KIND_ORG,
	ledger.OwnerModulith:   pb.OwnerKind_OWNER_KIND_MODULITH,
}

var ownerKindFromProto = map[pb.OwnerKind]ledger.OwnerKind{
	pb.OwnerKind_OWNER_KIND_PROJECT: ledger.OwnerProject,
	pb.OwnerKind_OWNER_KIND_USER:    ledger.OwnerUser,
	pb.OwnerKind_OWNER_KIND_ORG:     ledger.OwnerOrg,
	pb.OwnerKind_OWNER_KIND_MODULITH:   ledger.OwnerModulith,
}

// Unknown kinds map to the zero value on both sides (missing map keys return
// zero), and the server's validation rejects them.

// ownerToProto and ownerFromProto convert the ledger's owners.
func ownerToProto(o ledger.Owner) *pb.Owner {
	return &pb.Owner{Kind: ownerKindToProto[o.Kind], Id: o.ID}
}

func ownerFromProto(o *pb.Owner) ledger.Owner {
	if o == nil {
		return ledger.Owner{}
	}
	return ledger.Owner{Kind: ownerKindFromProto[o.GetKind()], ID: o.GetId()}
}

// ---- Asset refs

func refToProto(r dam.AssetRef) *pb.AssetRef {
	return &pb.AssetRef{AssetId: r.AssetID, Version: int32(r.Version)}
}

func refFromProto(p *pb.AssetRef) dam.AssetRef {
	return dam.AssetRef{AssetID: p.GetAssetId(), Version: int(p.GetVersion())}
}

// ---- Snapshots

func snapshotToProto(r manager.SnapshotRef) *pb.Snapshot {
	return &pb.Snapshot{
		ProjectId: r.ProjectID, ObjectId: r.ObjectID, Version: int32(r.Version), Seq: r.Seq,
		EditedBy: r.EditedBy, EditedAt: timestamp(r.EditedAt),
		ContentType: r.ContentType, SizeBytes: r.SizeBytes, CreatedAt: timestamp(r.CreatedAt),
	}
}

func snapshotFromProto(p *pb.Snapshot) manager.SnapshotRef {
	return manager.SnapshotRef{
		ProjectID: p.GetProjectId(), ObjectID: p.GetObjectId(), Version: int(p.GetVersion()), Seq: p.GetSeq(),
		EditedBy: p.GetEditedBy(), EditedAt: timeOf(p.GetEditedAt()),
		ContentType: p.GetContentType(), SizeBytes: p.GetSizeBytes(), CreatedAt: timeOf(p.GetCreatedAt()),
	}
}
