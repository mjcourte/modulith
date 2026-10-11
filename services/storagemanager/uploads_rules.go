package storagemanager

// Upload session rules shared by every UploadStore implementation.

import (
	"fmt"
	"time"
)

// normalize validates a new session for CreateUpload. Checks that need stored
// data (the folder, the existing asset) are left to the Store.
func (u Upload) normalize() (Upload, error) {
	if err := u.Owner.Validate(); err != nil {
		return u, fmt.Errorf("%w: %w", ErrInvalidUpload, err)
	}
	switch {
	case u.Bucket == "" || u.Key == "":
		return u, fmt.Errorf("%w: bucket and key are required", ErrInvalidUpload)
	case u.ContentType == "":
		return u, fmt.Errorf("%w: content type is required", ErrInvalidUpload)
	case u.SizeBytes < 0:
		return u, fmt.Errorf("%w: negative size %d", ErrInvalidUpload, u.SizeBytes)
	case u.ExpiresAt.IsZero():
		return u, fmt.Errorf("%w: expiry is required", ErrInvalidUpload)
	}

	if u.AssetID != "" {
		// A new version takes its details from the existing asset.
		if err := checkOptionalID(u.AssetID, "asset", ErrInvalidUpload); err != nil {
			return u, err
		}
		if u.Name != "" || u.Description != "" || len(u.Tags) > 0 || u.FolderID != "" {
			return u, fmt.Errorf("%w: a new version can't set name, description, tags or folder", ErrInvalidUpload)
		}
		return u, nil
	}

	// A new asset: the same rules as CreateAsset, checked now rather than
	// after the browser has uploaded the file. Errors match both
	// ErrInvalidUpload and ErrInvalidAsset.
	a, err := u.newAsset().normalizeFields()
	if err != nil {
		return u, fmt.Errorf("%w: %w", ErrInvalidUpload, err)
	}
	u.ProjectID, u.Name, u.Tags = a.ProjectID, a.Name, a.Tags
	return u, nil
}

// expired reports whether a pending session can no longer be completed.
func (u Upload) expired(at time.Time) bool {
	return !at.Before(u.ExpiresAt)
}

// matches checks that the uploaded file is the one the session signed for.
func (u Upload) matches(v Version) error {
	if v.ContentType != u.ContentType || v.SizeBytes != u.SizeBytes {
		return fmt.Errorf("%w: uploaded %s, %d bytes; session is for %s, %d bytes",
			ErrInvalidUpload, v.ContentType, v.SizeBytes, u.ContentType, u.SizeBytes)
	}
	if v.ObjectID != u.ObjectID {
		return fmt.Errorf("%w: ledger object %s, but the session's is %s", ErrInvalidUpload, v.ObjectID, u.ObjectID)
	}
	return nil
}
