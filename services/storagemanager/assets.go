package storagemanager

import (
	"context"
	"errors"
	"mime"
	"strings"
	"time"

	"gyoza/services/storageledger"
)

// Errors returned by every Store. Check them with errors.Is: they are usually
// wrapped with details about what was wrong.
var (
	ErrAssetNotFound   = errors.New("storagemanager: asset not found")
	ErrVersionNotFound = errors.New("storagemanager: version not found")
	ErrInvalidAsset    = errors.New("storagemanager: invalid asset")
	ErrInvalidQuery    = errors.New("storagemanager: invalid query")
)

// Limits enforced by every Store (the checks live in assets_rules.go).
// Lengths count characters, not bytes, so "ñ" is one.
const (
	// DefaultPageSize is used when a query doesn't set PageSize.
	DefaultPageSize = 50
	// MaxPageSize caps PageSize so one request can't pull a whole library.
	MaxPageSize = 500
	// MaxNameLen bounds names, after trimming, to what a file list can display.
	MaxNameLen = 255
	// MaxDescriptionLen keeps descriptions to notes, not documents.
	MaxDescriptionLen = 4096
	// MaxTagLen bounds a tag, after normalising, so tags stay short labels
	// and the tag index stays compact.
	MaxTagLen = 64
	// MaxTags per asset keeps tag filtering and the tag index cheap.
	MaxTags = 50
)

// Asset is a file in the DAM: a named, tagged item whose bytes live in one or
// more versions. Its owner, project and timestamps follow the same rules as
// ledger objects, so cascades and user removal treat both alike.
type Asset struct {
	ID    string
	Owner storageledger.Owner
	// ProjectID is the project the asset is associated with, whoever owns it;
	// empty for library assets. A project-owned asset must have
	// ProjectID == Owner.ID.
	ProjectID      string
	FolderID       string // "" means the root folder
	Name           string
	Description    string
	Tags           []string // normalised (trimmed, lowercased, deduplicated) and sorted
	ContentType    string   // MIME type of the current version, e.g. "image/png"
	Category       Category // derived from the first version's ContentType; never changes
	CurrentVersion int      // Number of the version currently in use
	CreatedBy      string   // user who created the asset; "" once that user is deleted
	CreatedAt      time.Time
	UpdatedAt      time.Time  // last change to the asset's fields or current version
	DeletedAt      *time.Time // nil until soft-deleted
}

// Version is one uploaded file of an asset. Versions are append-only: a new
// upload adds the next number, and reverting only changes which version is
// current. They are removed only together with their asset.
type Version struct {
	AssetID     string
	Number      int    // 1, 2, 3, ... assigned by the Store
	ObjectID    string // the ledger object holding this version's bytes
	ContentType string // MIME type; must map to the asset's Category
	SizeBytes   int64
	CreatedBy   string
	CreatedAt   time.Time
}

// Category is the broad kind of an asset, derived from its MIME type with
// CategoryOf, so it can be listed and filtered without reading any bytes.
type Category string

const (
	CategoryImage Category = "image" // image/*, including image/svg+xml
	CategoryVideo Category = "video" // video/*
	CategoryAudio Category = "audio" // audio/*
	CategoryFont  Category = "font"  // font/*, application/font-*, application/x-font*, application/vnd.ms-fontobject
	CategoryPDF   Category = "pdf"   // application/pdf
	CategoryJSON  Category = "json"  // application/json and *+json
	CategoryText  Category = "text"  // text/*
	CategoryOther Category = "other" // anything else, or an unparseable type
)

// CategoryOf maps a MIME type to its Category. Parameters such as
// "; charset=utf-8" and letter case are ignored (mime.ParseMediaType handles
// both); anything unrecognised or malformed is CategoryOther.
func CategoryOf(contentType string) Category {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return CategoryOther
	}
	major, minor, _ := strings.Cut(mediaType, "/")
	switch {
	case major == "image": // catches image/svg+xml
		return CategoryImage
	case major == "video":
		return CategoryVideo
	case major == "audio":
		return CategoryAudio
	case major == "font", mediaType == "application/vnd.ms-fontobject",
		strings.HasPrefix(mediaType, "application/font-"),
		strings.HasPrefix(mediaType, "application/x-font"):
		return CategoryFont
	case mediaType == "application/pdf":
		return CategoryPDF
	case mediaType == "application/json",
		strings.HasSuffix(minor, "+json"):
		return CategoryJSON
	case major == "text":
		return CategoryText
	}
	return CategoryOther
}

// AssetUpdate is a partial update: only non-nil fields change. Pointers tell
// "leave alone" (nil) apart from "set to empty" (a pointer to "" or to an
// empty slice).
type AssetUpdate struct {
	Name        *string
	Description *string
	Tags        *[]string // replaces the whole tag set; normalised like on create
}

// AssetQuery selects assets within exactly one scope: a project or an owner.
// The other fields narrow the results within that scope.
type AssetQuery struct {
	// Scope: set exactly one.
	ProjectID string
	Owner     storageledger.Owner

	// Optional filters.
	Tag            string   // matched after normalising, so "LOGO" finds "logo"
	Category       Category // "" means any
	IncludeDeleted bool

	// Pagination. PageSize 0 means the default; larger values are capped.
	// PageToken is the NextPageToken from the previous page, or "" for the first.
	PageSize  int
	PageToken string
}

// AssetPage is one page of results, oldest first. NextPageToken is "" when
// there are no more results.
type AssetPage struct {
	Assets        []Asset
	NextPageToken string
}

// Store persists assets and their versions. It is pure data: it validates
// shape (names, tags, owners, categories) but does no authorization; the
// Manager checks permissions before calling it and is trusted. Every
// implementation must pass the shared store test suite.
//
// Soft-deleted assets can still be read (GetAsset, ListVersions, ListAssets
// with IncludeDeleted) but not changed: UpdateAsset, AddVersion and
// SetCurrentVersion treat them as missing and return ErrAssetNotFound.
type Store interface {
	// CreateAsset stores a new asset with first as version 1, atomically.
	// It assigns IDs and timestamps and derives Category from first.
	CreateAsset(ctx context.Context, a Asset, first Version) (Asset, error)
	// GetAsset returns the asset, including soft-deleted ones, or ErrAssetNotFound.
	GetAsset(ctx context.Context, id string) (Asset, error)
	// ListAssets returns one page of assets matching q.
	ListAssets(ctx context.Context, q AssetQuery) (AssetPage, error)
	// UpdateAsset applies the non-nil fields of u and returns the result.
	UpdateAsset(ctx context.Context, id string, u AssetUpdate) (Asset, error)
	// AddVersion appends v as the next version and makes it current. v must
	// have the asset's Category, or ErrInvalidAsset.
	AddVersion(ctx context.Context, assetID string, v Version) (Version, error)
	// SetCurrentVersion makes an existing version current again (revert); the
	// asset's ContentType follows it. ErrVersionNotFound if there is no such
	// number. Setting the version that is already current changes nothing, not
	// even UpdatedAt, so retries are harmless.
	SetCurrentVersion(ctx context.Context, assetID string, number int) (Asset, error)
	// ListVersions returns every version of the asset, oldest first.
	ListVersions(ctx context.Context, assetID string) ([]Version, error)
	// MarkAssetDeleted soft-deletes the asset. Deleting twice keeps the first
	// deletion time; a missing asset is ErrAssetNotFound.
	MarkAssetDeleted(ctx context.Context, id string) error
}
