package dam

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"
)

// MemoryStore is an in-memory Store for tests and local runs.
type MemoryStore struct {
	mu     sync.Mutex
	assets map[string]Asset // by asset ID
	// versions holds each asset's versions, oldest first. Versions are only
	// ever appended, so version n is always at index n-1.
	versions map[string][]Version
	folders  map[string]Folder // by folder ID; methods in folders_memory.go
	uploads  map[string]Upload // by upload ID; methods in uploads_memory.go
}

// NewMemoryStore returns an empty MemoryStore.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		assets:   make(map[string]Asset),
		versions: make(map[string][]Version),
		folders:  make(map[string]Folder),
		uploads:  make(map[string]Upload),
	}
}

// Compile-time check that *MemoryStore satisfies Store.
var _ Store = (*MemoryStore)(nil)

// now matches what Postgres stores: UTC, microsecond precision.
func now() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }

// cloneAsset copies a, including its Tags slice, so callers and the store
// never share memory: changing a returned asset's tags can't change what's stored.
func cloneAsset(a Asset) Asset {
	a.Tags = slices.Clone(a.Tags)
	return a
}

// liveAsset returns the asset for a write, treating soft-deleted assets as
// missing. The caller must hold m.mu.
func (m *MemoryStore) liveAsset(id string) (Asset, error) {
	a, ok := m.assets[id]
	if !ok || a.DeletedAt != nil {
		return Asset{}, ErrAssetNotFound
	}
	return a, nil
}

func (m *MemoryStore) CreateAsset(ctx context.Context, a Asset, first Version) (Asset, error) {
	// Validation needs no shared state, so it runs before taking the lock.
	a, err := a.normalize(first)
	if err != nil {
		return Asset{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.insertAsset(a, first)
}

// insertAsset stores a normalised asset with first as version 1. The caller
// must hold m.mu; CompleteUpload calls it too, inside its own locked section.
func (m *MemoryStore) insertAsset(a Asset, first Version) (Asset, error) {
	if err := m.checkAssetFolder(a.FolderID, a.Owner); err != nil {
		return Asset{}, err
	}
	t := now()
	a.ID = NewID()
	a.CreatedAt, a.UpdatedAt, a.DeletedAt, a.DeletionID = t, t, nil, ""
	first.AssetID, first.Number, first.CreatedAt = a.ID, 1, t
	m.assets[a.ID] = cloneAsset(a)
	m.versions[a.ID] = []Version{first}
	return cloneAsset(a), nil
}

func (m *MemoryStore) GetAsset(ctx context.Context, id string) (Asset, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.assets[id]
	if !ok {
		return Asset{}, ErrAssetNotFound
	}
	return cloneAsset(a), nil
}

func (m *MemoryStore) ListAssets(ctx context.Context, q AssetQuery) (AssetPage, error) {
	q, err := q.normalize()
	if err != nil {
		return AssetPage{}, err
	}
	after, err := q.afterID()
	if err != nil {
		return AssetPage{}, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	var matches []Asset
	for _, a := range m.assets {
		if a.ID > after && matchesAssetQuery(a, q) {
			matches = append(matches, a)
		}
	}
	// Map iteration order is random; IDs are UUIDv7, so sorting by ID is
	// creation order, the same order Postgres will use.
	slices.SortFunc(matches, func(x, y Asset) int { return strings.Compare(x.ID, y.ID) })
	if len(matches) > q.PageSize+1 {
		matches = matches[:q.PageSize+1] // one extra tells assetPageOf another page exists
	}
	for i := range matches {
		matches[i] = cloneAsset(matches[i])
	}
	return assetPageOf(matches, q.PageSize), nil
}

// matchesAssetQuery reports whether a is in q's scope and passes its filters.
// q must already be normalised.
func matchesAssetQuery(a Asset, q AssetQuery) bool {
	switch {
	case q.ProjectID != "" && a.ProjectID != q.ProjectID:
		return false
	case !q.Owner.IsZero() && a.Owner != q.Owner:
		return false
	case q.Tag != "" && !slices.Contains(a.Tags, q.Tag):
		return false
	case q.Category != "" && a.Category != q.Category:
		return false
	case q.Folder != nil && a.FolderID != *q.Folder:
		return false
	case !q.IncludeDeleted && a.DeletedAt != nil:
		return false
	case q.OnlyDeleted && a.DeletedAt == nil:
		return false
	}
	return true
}

func (m *MemoryStore) GetVersion(ctx context.Context, assetID string, number int) (Version, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.assets[assetID]; !ok {
		return Version{}, ErrAssetNotFound
	}
	versions := m.versions[assetID]
	if number < 1 || number > len(versions) {
		return Version{}, ErrVersionNotFound
	}
	return versions[number-1], nil
}

func (m *MemoryStore) RestoreAsset(ctx context.Context, id string) (Asset, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.assets[id]
	if !ok {
		return Asset{}, ErrAssetNotFound
	}
	if a.DeletedAt == nil {
		return cloneAsset(a), nil // not in the trash: nothing to do
	}
	m.restoreAssetLocked(&a)
	m.assets[id] = a
	return cloneAsset(a), nil
}

// restoreAssetLocked takes a out of the trash, moving it to the top level if
// its folder isn't live. The caller must hold m.mu and write a back.
func (m *MemoryStore) restoreAssetLocked(a *Asset) {
	if a.FolderID != "" {
		if _, err := m.ownedFolder(a.FolderID, a.Owner); err != nil {
			a.FolderID = ""
		}
	}
	a.DeletedAt, a.DeletionID = nil, ""
	a.UpdatedAt = now()
}

func (m *MemoryStore) UpdateAsset(ctx context.Context, id string, u AssetUpdate) (Asset, error) {
	u, err := u.normalize()
	if err != nil {
		return Asset{}, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	a, err := m.liveAsset(id)
	if err != nil {
		return Asset{}, err
	}
	if u.Name != nil {
		a.Name = *u.Name
	}
	if u.Description != nil {
		a.Description = *u.Description
	}
	if u.Tags != nil {
		a.Tags = slices.Clone(*u.Tags)
	}
	if u.FolderID != nil {
		// Checked before anything is written back, so a bad move changes nothing.
		if err := m.checkAssetFolder(*u.FolderID, a.Owner); err != nil {
			return Asset{}, err
		}
		a.FolderID = *u.FolderID
	}
	a.UpdatedAt = now()
	m.assets[id] = a // a is a copy; write it back
	return cloneAsset(a), nil
}

func (m *MemoryStore) AddVersion(ctx context.Context, assetID string, v Version) (Version, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.appendVersion(assetID, v)
}

// appendVersion adds v as the asset's next, current version. The caller must
// hold m.mu; CompleteUpload calls it too.
func (m *MemoryStore) appendVersion(assetID string, v Version) (Version, error) {
	a, err := m.liveAsset(assetID)
	if err != nil {
		return Version{}, err
	}
	// Needs the asset's Category, so this check runs under the lock.
	if err := a.acceptsVersion(v); err != nil {
		return Version{}, err
	}
	t := now()
	v.AssetID = assetID
	v.Number = len(m.versions[assetID]) + 1
	v.CreatedAt = t
	m.versions[assetID] = append(m.versions[assetID], v)

	a.CurrentVersion = v.Number
	a.ContentType = v.ContentType
	a.UpdatedAt = t
	m.assets[assetID] = a
	return v, nil
}

func (m *MemoryStore) SetCurrentVersion(ctx context.Context, assetID string, number int) (Asset, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, err := m.liveAsset(assetID)
	if err != nil {
		return Asset{}, err
	}
	versions := m.versions[assetID]
	if number < 1 || number > len(versions) {
		return Asset{}, ErrVersionNotFound
	}
	if number == a.CurrentVersion {
		return cloneAsset(a), nil // already current: a no-op, so retries change nothing
	}
	a.CurrentVersion = number
	a.ContentType = versions[number-1].ContentType
	a.UpdatedAt = now()
	m.assets[assetID] = a
	return cloneAsset(a), nil
}

func (m *MemoryStore) ListVersions(ctx context.Context, assetID string) ([]Version, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Deleted assets still list their versions, like GetAsset still returns them.
	if _, ok := m.assets[assetID]; !ok {
		return nil, ErrAssetNotFound
	}
	// Version has no slices or pointers, so cloning the outer slice is a full copy.
	return slices.Clone(m.versions[assetID]), nil
}

func (m *MemoryStore) MarkAssetDeleted(ctx context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.assets[id]
	if !ok {
		return ErrAssetNotFound
	}
	if a.DeletedAt == nil { // deleting again keeps the first deletion time
		t := now()
		a.DeletedAt = &t
		m.assets[id] = a
	}
	return nil
}
