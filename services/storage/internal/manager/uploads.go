package manager

import (
	"context"
	"errors"
	"time"

	"gyoza/services/storage/internal/ledger"
)

// Upload session errors returned by every Store (see UploadStore).
var (
	ErrUploadNotFound = errors.New("storage: upload not found")
	ErrUploadExpired  = errors.New("storage: upload expired")
	ErrInvalidUpload  = errors.New("storage: invalid upload")
)

// Upload is a pending upload session: the Store's half of the two-phase
// upload. The Manager's BeginUpload creates it (choosing the S3 key and how
// long the presigned URL lives); CompleteUpload finishes it once the browser
// has put the file in S3.
//
// A session either creates a new asset (AssetID empty; Name, Description,
// Tags and FolderID describe it) or adds a new version to an existing asset
// (AssetID set; those fields must be empty).
type Upload struct {
	ID        string // UUIDv7
	Owner     ledger.Owner
	ProjectID string
	CreatedBy string

	AssetID string // set: a new version of this asset; empty: a new asset

	// The new asset's details, when AssetID is empty.
	Name        string
	Description string
	Tags        []string
	FolderID    string

	// Where and what the browser will upload, as signed into the presigned URL.
	Bucket      string
	Key         string
	ContentType string
	SizeBytes   int64

	// ObjectID is the ID the uploaded file is recorded under in the ledger,
	// assigned by CreateUpload. Choosing it up front makes recording
	// idempotent: a retried or overlapping CompleteUpload creates the same
	// ledger object again, which the ledger turns into "already there".
	ObjectID string

	ExpiresAt   time.Time // after this, CompleteUpload refuses and the cleanup job may delete it
	CreatedAt   time.Time
	CompletedAt *time.Time // nil while pending

	// What completing produced, so a repeated CompleteUpload returns the same result.
	ResultAssetID string
	ResultVersion int
}

// newAsset is the asset this session would create, before its first version.
func (u Upload) newAsset() Asset {
	return Asset{
		Owner: u.Owner, ProjectID: u.ProjectID, CreatedBy: u.CreatedBy,
		Name: u.Name, Description: u.Description, Tags: u.Tags, FolderID: u.FolderID,
	}
}

// UploadStore persists upload sessions. Like the rest of Store it is pure
// data: shape and consistency only, no authorization.
type UploadStore interface {
	// CreateUpload stores a pending session, assigning its ID, ObjectID and
	// CreatedAt.
	// Everything that can be checked before the file exists is checked now, so
	// the browser isn't sent to upload something that could never complete:
	// a new asset's name, tags, project and folder (ErrFolderNotFound if the
	// folder doesn't belong to the owner); for a new version, that the asset
	// exists, is live, has the same owner (else ErrAssetNotFound) and that the
	// content type keeps its category (else ErrInvalidAsset).
	CreateUpload(ctx context.Context, u Upload) (Upload, error)
	// GetUpload returns the session, pending or completed, or ErrUploadNotFound.
	GetUpload(ctx context.Context, id string) (Upload, error)
	// CompleteUpload finishes a session in one atomic step: it records v (the
	// uploaded file, already in the ledger) as version 1 of a new asset or as
	// the next version of the existing one, and marks the session complete.
	// v's ObjectID, ContentType and SizeBytes must match the session's
	// (ErrInvalidUpload).
	//
	// It is idempotent: completing a completed session returns the same asset
	// and creates nothing, so a browser retrying after a timeout is harmless.
	// A pending session past ExpiresAt is ErrUploadExpired. The asset checks of
	// CreateUpload are repeated, since things may have changed meanwhile.
	CompleteUpload(ctx context.Context, id string, v Version) (Asset, error)
	// ListExpiredUploads returns up to limit sessions that are still pending
	// and expired before the given time, soonest-expired first, for the
	// cleanup job to delete their blobs and then the sessions.
	ListExpiredUploads(ctx context.Context, before time.Time, limit int) ([]Upload, error)
	// DeleteUpload removes a session for good. Deleting a missing session is
	// not an error, so the cleanup job can safely retry.
	DeleteUpload(ctx context.Context, id string) error
}
