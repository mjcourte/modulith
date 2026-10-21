package manager

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mjcourte/modulith/services/storage/internal/blob"
	"github.com/mjcourte/modulith/services/storage/internal/dam"
	"github.com/mjcourte/modulith/services/storage/internal/ledger"
)

// CompleteUpload finishes an upload the browser has PUT to S3: it checks the
// file really arrived and is what was declared, records it in the ledger, and
// creates the asset (or its new version). Video and audio are queued for the
// media probe job (ProbeMedia).
//
// It's safe to call again, from a retry, a timeout or a double click, even
// overlapping: every path ends with one asset (or version) and one ledger
// object, because the ledger object's ID was chosen when the upload began.
func (m *Manager) CompleteUpload(ctx context.Context, actorID, uploadID string) (dam.Asset, error) {
	u, err := m.store.GetUpload(ctx, uploadID)
	if err != nil {
		return dam.Asset{}, err
	}
	// Only the user who began the upload may finish it, and only while they
	// still may write there (permissions can change mid-upload).
	if actorID == "" || actorID != u.CreatedBy {
		return dam.Asset{}, fmt.Errorf("%w: only the user who began an upload can complete it", ErrForbidden)
	}
	check := m.canCreateIn // the same check BeginUpload made
	if u.AssetID != "" {
		check = m.canWrite
	}
	if err := check(ctx, actorID, u.Owner, u.ProjectID); err != nil {
		return dam.Asset{}, err
	}

	if u.CompletedAt != nil {
		return m.store.GetAsset(ctx, u.ResultAssetID) // already done: same result again
	}
	if u.Expired(time.Now()) {
		return dam.Asset{}, fmt.Errorf("%w: expired at %s", dam.ErrUploadExpired, u.ExpiresAt.Format(time.RFC3339))
	}

	prefix, err := m.checkUploadedFile(ctx, u)
	if err != nil {
		return dam.Asset{}, err
	}

	if err := m.recordInLedger(ctx, u); err != nil {
		return dam.Asset{}, err
	}
	// Media info, recorded before the session completes so that a retry after
	// a failure here records it again (both calls are safe to repeat). Raster
	// images are measured now from the bytes already read; video and audio
	// are queued for the probe job (ffprobe). Vector images have no pixel size.
	switch dam.CategoryOf(u.ContentType) {
	case dam.CategoryImage:
		if err := m.recordImageInfo(ctx, u.ObjectID, prefix); err != nil {
			return dam.Asset{}, err
		}
	case dam.CategoryVideo, dam.CategoryAudio:
		if err := m.ledger.MarkMediaPending(ctx, u.ObjectID); err != nil {
			return dam.Asset{}, fmt.Errorf("queue media probe: %w", err)
		}
	}
	// Overlapping calls all get here; the Store lets one complete the
	// session and hands the others the same asset.
	return m.store.CompleteUpload(ctx, u.ID, dam.Version{
		ObjectID:    u.ObjectID,
		ContentType: u.ContentType,
		SizeBytes:   u.SizeBytes,
		CreatedBy:   actorID,
	})
}

// checkUploadedFile confirms the file is in S3 with the signed size and type,
// and that its first bytes match the declared type. It returns those first
// bytes: enough to measure an image (imageHeaderLen) for raster images,
// SniffLen otherwise. A rejected file is left in place: the cleanup job
// deletes it when the session expires.
func (m *Manager) checkUploadedFile(ctx context.Context, u dam.Upload) ([]byte, error) {
	info, err := m.blobs.Stat(ctx, u.Bucket, u.Key)
	if errors.Is(err, blob.ErrNotFound) {
		return nil, fmt.Errorf("%w: upload the file to the ticket's URL first", ErrNotUploaded)
	}
	if err != nil {
		return nil, fmt.Errorf("check uploaded file: %w", err)
	}
	// The presigned URL signs both, so S3 shouldn't let these differ; check anyway.
	if info.Size != u.SizeBytes || info.ContentType != u.ContentType {
		return nil, fmt.Errorf("%w: stored %s, %d bytes; expected %s, %d bytes",
			dam.ErrInvalidUpload, info.ContentType, info.Size, u.ContentType, u.SizeBytes)
	}
	// One read serves both: sniffing looks at the first SniffLen bytes only.
	n := SniffLen
	if dam.CategoryOf(u.ContentType) == dam.CategoryImage {
		n = imageHeaderLen
	}
	prefix, err := m.blobs.ReadPrefix(ctx, u.Bucket, u.Key, n)
	if err != nil {
		return nil, fmt.Errorf("read uploaded file: %w", err)
	}
	if err := checkSniffed(u.ContentType, prefix); err != nil {
		return nil, err
	}
	return prefix, nil
}

// recordInLedger records the session's file in the ledger under the ID the
// session chose up front. Doing it again (a retry, an overlapping call, or a
// resumed attempt that crashed after this step) finds the object already
// there, so there is never more than one ledger object per upload.
func (m *Manager) recordInLedger(ctx context.Context, u dam.Upload) error {
	_, err := m.ledger.Create(ctx, ledger.Object{
		ID:          u.ObjectID,
		Owner:       u.Owner,
		CreatedBy:   u.CreatedBy,
		ProjectID:   u.ProjectID,
		Kind:        ledger.KindAsset,
		Bucket:      u.Bucket,
		Key:         u.Key,
		SizeBytes:   u.SizeBytes,
		ContentType: u.ContentType,
	})
	if err != nil {
		return fmt.Errorf("record in ledger: %w", err)
	}
	return nil
}
