package rest

import (
	"errors"
	"log/slog"
	"net/http"

	"gyoza/internal/engine"
	"gyoza/services/documents"
	"gyoza/services/iam"
	"gyoza/services/storage"
)

// errorCode is one row of a table: a sentinel (matched with errors.Is, so
// wrapped errors and gRPC clients' rebuilt ones match too), its HTTP status
// and the code browsers branch on.
type errorCode struct {
	err    error
	status int
	code   string
}

// iamErrors maps iam's sentinels. Not mapped: ErrNotDeleting (internal to the
// deletion jobs), which is a 500 if it ever escapes.
var iamErrors = []errorCode{
	{iam.ErrForbidden, http.StatusForbidden, "FORBIDDEN"},
	{iam.ErrUserNotFound, http.StatusNotFound, "USER_NOT_FOUND"},
	{iam.ErrOrgNotFound, http.StatusNotFound, "ORG_NOT_FOUND"},
	{iam.ErrProjectNotFound, http.StatusNotFound, "PROJECT_NOT_FOUND"},
	{iam.ErrInviteNotFound, http.StatusNotFound, "INVITE_NOT_FOUND"},
	{iam.ErrShareNotFound, http.StatusNotFound, "SHARE_NOT_FOUND"},
	{iam.ErrInvalidUser, http.StatusBadRequest, "INVALID_USER"},
	{iam.ErrInvalidOrg, http.StatusBadRequest, "INVALID_ORG"},
	{iam.ErrInvalidProject, http.StatusBadRequest, "INVALID_PROJECT"},
	{iam.ErrInvalidInvite, http.StatusBadRequest, "INVALID_INVITE"},
	{iam.ErrInvalidShare, http.StatusBadRequest, "INVALID_SHARE"},
	{iam.ErrInvalidQuery, http.StatusBadRequest, "INVALID_QUERY"},
	{iam.ErrAlreadyMember, http.StatusConflict, "ALREADY_MEMBER"},
	{iam.ErrNotMember, http.StatusConflict, "NOT_MEMBER"},
	{iam.ErrLastAdmin, http.StatusConflict, "LAST_ADMIN"},
	{iam.ErrEmailTaken, http.StatusConflict, "EMAIL_TAKEN"},
	{iam.ErrInviteExpired, http.StatusGone, "INVITE_EXPIRED"},
}

// storageErrors maps storage's sentinels. *FolderNotEmptyError also adds
// its counts to the body (writeError).
var storageErrors = []errorCode{
	{storage.ErrForbidden, http.StatusForbidden, "FORBIDDEN"},
	{storage.ErrAssetNotFound, http.StatusNotFound, "ASSET_NOT_FOUND"},
	{storage.ErrVersionNotFound, http.StatusNotFound, "VERSION_NOT_FOUND"},
	{storage.ErrFolderNotFound, http.StatusNotFound, "FOLDER_NOT_FOUND"},
	{storage.ErrUploadNotFound, http.StatusNotFound, "UPLOAD_NOT_FOUND"},
	{storage.ErrSnapshotNotFound, http.StatusNotFound, "SNAPSHOT_NOT_FOUND"},
	{storage.ErrInvalidAsset, http.StatusBadRequest, "INVALID_ASSET"},
	{storage.ErrInvalidQuery, http.StatusBadRequest, "INVALID_QUERY"},
	{storage.ErrInvalidFolder, http.StatusBadRequest, "INVALID_FOLDER"},
	{storage.ErrInvalidUpload, http.StatusBadRequest, "INVALID_UPLOAD"},
	{storage.ErrUploadTooLarge, http.StatusBadRequest, "UPLOAD_TOO_LARGE"},
	{storage.ErrNameTaken, http.StatusConflict, "NAME_TAKEN"},
	{storage.ErrFolderNotEmpty, http.StatusConflict, "FOLDER_NOT_EMPTY"},
	{storage.ErrForkRequired, http.StatusConflict, "FORK_REQUIRED"},
	{storage.ErrNotUploaded, http.StatusConflict, "NOT_UPLOADED"},
	{storage.ErrUploadExpired, http.StatusGone, "UPLOAD_EXPIRED"},
}

// documentsErrors maps documents' sentinels and the engine's, which
// documents passes through.
var documentsErrors = []errorCode{
	{documents.ErrForbidden, http.StatusForbidden, "FORBIDDEN"},
	{documents.ErrExists, http.StatusConflict, "DOCUMENT_EXISTS"},
	{engine.ErrUnsupportedFormat, http.StatusBadRequest, "UNSUPPORTED_FORMAT"},
	{engine.ErrBadStamp, http.StatusBadRequest, "BAD_STAMP"},
}

// gatewayErrors are the gateway's own.
var gatewayErrors = []errorCode{
	{errBadRequest, http.StatusBadRequest, "BAD_REQUEST"},
	{errNotJSON, http.StatusUnsupportedMediaType, "NOT_JSON"},
}

var errorTables = [][]errorCode{gatewayErrors, iamErrors, storageErrors, documentsErrors}

// errorBody is every error response: {"error": {...}}.
type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	// FolderNotEmptyError's counts, so the UI can say what a tree delete
	// would trash.
	Folders *int `json:"folders,omitempty"`
	Assets  *int `json:"assets,omitempty"`
}

// writeError answers with err's status and code. An error in no table is a
// 500 with a generic message: its text may hold internals, so it's logged,
// not sent.
func writeError(w http.ResponseWriter, r *http.Request, err error) {
	status, code := Code(err)
	d := errorDetail{Code: code, Message: err.Error()}
	if status == http.StatusInternalServerError {
		slog.ErrorContext(r.Context(), "gateway: request failed", "method", r.Method, "path", r.URL.Path, "err", err)
		d.Message = "internal error"
	}
	var notEmpty *storage.FolderNotEmptyError
	if errors.As(err, &notEmpty) {
		d.Folders, d.Assets = &notEmpty.Folders, &notEmpty.Assets
	}
	writeJSON(w, status, errorBody{Error: d})
}

// Code returns err's HTTP status and error code, from the tables above: the
// WebSocket reports errors with the same codes. An error in no table is 500
// INTERNAL, whose message must not be shown (writeError).
func Code(err error) (int, string) {
	for _, table := range errorTables {
		for _, e := range table {
			if errors.Is(err, e.err) {
				return e.status, e.code
			}
		}
	}
	return http.StatusInternalServerError, "INTERNAL"
}
