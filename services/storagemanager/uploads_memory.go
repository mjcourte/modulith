package storagemanager

// MemoryStore's UploadStore methods (struct and lock in assets_memory.go).

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"time"
)

// cloneUpload copies u, including its Tags slice.
func cloneUpload(u Upload) Upload {
	u.Tags = slices.Clone(u.Tags)
	return u
}

// checkUploadTarget repeats, against stored data, the checks CreateUpload
// promises: the folder for a new asset, or the existing asset for a new
// version. The caller must hold m.mu.
func (m *MemoryStore) checkUploadTarget(u Upload) error {
	if u.AssetID == "" {
		return m.checkAssetFolder(u.FolderID, u.Owner)
	}
	a, err := m.liveAsset(u.AssetID)
	if err != nil {
		return err
	}
	if a.Owner != u.Owner {
		return ErrAssetNotFound // another owner's asset: not found, as with folders
	}
	return a.acceptsVersion(Version{ObjectID: "pending", ContentType: u.ContentType, SizeBytes: u.SizeBytes})
}

func (m *MemoryStore) CreateUpload(ctx context.Context, u Upload) (Upload, error) {
	u, err := u.normalize()
	if err != nil {
		return Upload{}, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.checkUploadTarget(u); err != nil {
		return Upload{}, err
	}
	u.ID, u.ObjectID = newID(), newID()
	u.CreatedAt = now()
	u.CompletedAt, u.ResultAssetID, u.ResultVersion = nil, "", 0
	m.uploads[u.ID] = cloneUpload(u)
	return cloneUpload(u), nil
}

func (m *MemoryStore) GetUpload(ctx context.Context, id string) (Upload, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.uploads[id]
	if !ok {
		return Upload{}, ErrUploadNotFound
	}
	return cloneUpload(u), nil
}

func (m *MemoryStore) CompleteUpload(ctx context.Context, id string, v Version) (Asset, error) {
	// One locked section for the whole operation: the session can't be
	// completed twice, and the asset and the session change together or not at all.
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.uploads[id]
	if !ok {
		return Asset{}, ErrUploadNotFound
	}
	if u.CompletedAt != nil {
		// Already done: return what it produced (idempotent retry).
		return cloneAsset(m.assets[u.ResultAssetID]), nil
	}
	t := now()
	if u.expired(t) {
		return Asset{}, fmt.Errorf("%w: expired at %s", ErrUploadExpired, u.ExpiresAt.Format(time.RFC3339))
	}
	if err := u.matches(v); err != nil {
		return Asset{}, err
	}
	if v.CreatedBy == "" {
		v.CreatedBy = u.CreatedBy
	}

	var asset Asset
	if u.AssetID == "" {
		a, err := u.newAsset().normalize(v)
		if err != nil {
			return Asset{}, err
		}
		if asset, err = m.insertAsset(a, v); err != nil {
			return Asset{}, err
		}
		u.ResultVersion = 1
	} else {
		if err := m.checkUploadTarget(u); err != nil { // e.g. the asset was trashed meanwhile
			return Asset{}, err
		}
		added, err := m.appendVersion(u.AssetID, v)
		if err != nil {
			return Asset{}, err
		}
		asset, u.ResultVersion = cloneAsset(m.assets[u.AssetID]), added.Number
	}

	u.CompletedAt, u.ResultAssetID = &t, asset.ID
	m.uploads[id] = u
	return asset, nil
}

func (m *MemoryStore) ListExpiredUploads(ctx context.Context, before time.Time, limit int) ([]Upload, error) {
	if err := checkLimit(limit); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var expired []Upload
	for _, u := range m.uploads {
		if u.CompletedAt == nil && u.ExpiresAt.Before(before) {
			expired = append(expired, cloneUpload(u))
		}
	}
	slices.SortFunc(expired, func(a, b Upload) int {
		if c := a.ExpiresAt.Compare(b.ExpiresAt); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
	if len(expired) > limit {
		expired = expired[:limit]
	}
	return expired, nil
}

func (m *MemoryStore) DeleteUpload(ctx context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.uploads, id) // missing is fine: the cleanup job may retry
	return nil
}
