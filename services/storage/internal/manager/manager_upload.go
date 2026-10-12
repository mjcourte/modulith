package manager

import (
	"context"
	"fmt"
	"net/http"
	"path"
	"strings"
	"time"

	"gyoza/services/storage/internal/dam"
	"gyoza/services/storage/internal/ledger"
)

// UploadRequest is what a client asks for when it wants to upload a file:
// either a new asset (AssetID empty) or a new version of an existing one.
type UploadRequest struct {
	// For a new asset: who will own it and the project it's for (optional).
	// For a new version these come from the asset; leave them empty or equal.
	Owner     ledger.Owner
	ProjectID string

	AssetID string // set to upload a new version of this asset

	// The new asset's details (new assets only).
	Name        string
	Description string
	Tags        []string
	FolderID    string

	// The file the browser will send. Both are signed into the upload URL,
	// so S3 rejects anything else.
	ContentType string
	SizeBytes   int64
}

// UploadTicket tells the browser how to upload: PUT the file to URL, with
// exactly these headers, before ExpiresAt; then call CompleteUpload with
// UploadID.
type UploadTicket struct {
	UploadID  string
	Method    string            // always "PUT"
	URL       string            // presigned; contains its own credentials
	Headers   map[string]string // must be sent as-is (Content-Type is signed)
	ExpiresAt time.Time
}

// BeginUpload starts a two-phase upload: it checks the request and the
// user's permission, picks where the file will live, records a pending
// session, and returns a presigned URL for the browser to PUT the file to.
// Nothing is stored in the DAM or the ledger until CompleteUpload.
func (m *Manager) BeginUpload(ctx context.Context, actorID string, req UploadRequest) (UploadTicket, error) {
	switch {
	case req.SizeBytes < 1:
		return UploadTicket{}, fmt.Errorf("%w: empty file", dam.ErrInvalidUpload)
	case req.SizeBytes > m.cfg.MaxUploadBytes:
		return UploadTicket{}, fmt.Errorf("%w: %d bytes, max %d", ErrUploadTooLarge, req.SizeBytes, m.cfg.MaxUploadBytes)
	}

	owner, projectID := req.Owner, req.ProjectID
	if req.AssetID != "" {
		// A new version belongs where its asset belongs, whatever the request
		// says. (A trashed asset is refused by Store.CreateUpload below.)
		a, err := m.store.GetAsset(ctx, req.AssetID)
		if err != nil {
			return UploadTicket{}, err
		}
		if (!owner.IsZero() && owner != a.Owner) || (projectID != "" && projectID != a.ProjectID) {
			return UploadTicket{}, fmt.Errorf("%w: owner or project doesn't match the asset's", dam.ErrInvalidUpload)
		}
		owner, projectID = a.Owner, a.ProjectID
	}

	// Permission before anything else is revealed: a user who may not write
	// here learns nothing about whether the rest of the request was valid.
	if err := m.canWrite(ctx, actorID, owner, projectID); err != nil {
		return UploadTicket{}, err
	}

	key, err := objectKey(projectID, ledger.KindAsset)
	if err != nil {
		return UploadTicket{}, err
	}
	u, err := m.store.CreateUpload(ctx, dam.Upload{
		Owner: owner, ProjectID: projectID, CreatedBy: actorID,
		AssetID: req.AssetID,
		Name:    req.Name, Description: req.Description, Tags: req.Tags, FolderID: req.FolderID,
		Bucket: m.cfg.Bucket, Key: key,
		ContentType: req.ContentType, SizeBytes: req.SizeBytes,
		ExpiresAt: time.Now().Add(m.cfg.UploadURLTTL),
	})
	if err != nil {
		return UploadTicket{}, err
	}
	// Presign after the session exists: if presigning fails, the session just
	// expires unused and the cleanup job removes it.
	url, err := m.blobs.PresignPut(ctx, u.Bucket, u.Key, u.ContentType, u.SizeBytes, m.cfg.UploadURLTTL)
	if err != nil {
		return UploadTicket{}, fmt.Errorf("presign upload: %w", err)
	}
	return UploadTicket{
		UploadID:  u.ID,
		Method:    http.MethodPut,
		URL:       url,
		Headers:   map[string]string{"Content-Type": u.ContentType},
		ExpiresAt: u.ExpiresAt,
	}, nil
}

// objectKey builds the S3 key for a new object, following the layout in the
// README: projects/{projectID}/{kind}/{id}, or library/{kind}/{id} with no
// project. It's the only place keys are made; callers never pass one in.
//
// The project ID becomes a path segment, so it must be exactly one plain
// segment: a "/" or ".." in it could otherwise produce a key outside the
// layout (path.Join would turn "projects/../library" into "library").
func objectKey(projectID string, kind ledger.Kind) (string, error) {
	id := dam.NewID()
	if projectID == "" {
		return path.Join("library", string(kind), id), nil
	}
	if strings.ContainsAny(projectID, `/\`) || projectID == "." || projectID == ".." {
		return "", fmt.Errorf("%w: project ID %q can't be used in a storage key", dam.ErrInvalidUpload, projectID)
	}
	return path.Join("projects", projectID, string(kind), id), nil
}
