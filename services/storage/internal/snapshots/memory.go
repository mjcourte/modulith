package snapshots

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"sync"
	"time"
)

// Memory is the in-memory Store, for tests.
type Memory struct {
	mu   sync.Mutex
	rows map[string]Snapshot // by object ID
}

func NewMemory() *Memory { return &Memory{rows: map[string]Snapshot{}} }

var _ Store = (*Memory)(nil)

func (m *Memory) Begin(ctx context.Context, s Snapshot) (Snapshot, error) {
	if err := checkBegin(s); err != nil {
		return Snapshot{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.rows[s.ObjectID]; ok {
		return Snapshot{}, fmt.Errorf("%w: object %s already recorded", ErrInvalid, s.ObjectID)
	}
	if latest, ok := m.latestLocked(s.ProjectID); ok && s.Seq < latest.Seq {
		return Snapshot{}, stale(s, latest)
	}
	s.Version, s.CompletedAt, s.CreatedAt = 0, nil, now()
	s.EditedAt = s.EditedAt.UTC().Truncate(time.Microsecond)
	s.ExpiresAt = s.ExpiresAt.UTC().Truncate(time.Microsecond)
	m.rows[s.ObjectID] = s
	return s, nil
}

func (m *Memory) Get(ctx context.Context, objectID string) (Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.rows[objectID]
	if !ok {
		return Snapshot{}, fmt.Errorf("%w: %s", ErrNotFound, objectID)
	}
	return s, nil
}

func (m *Memory) Complete(ctx context.Context, objectID string) (Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.rows[objectID]
	if !ok {
		return Snapshot{}, fmt.Errorf("%w: %s", ErrNotFound, objectID)
	}
	if s.Version != 0 {
		return s, nil
	}
	latest, ok := m.latestLocked(s.ProjectID)
	if ok && s.Seq < latest.Seq {
		return Snapshot{}, stale(s, latest)
	}
	s.Version = latest.Version + 1 // 1 when there is none
	t := now()
	s.CompletedAt = &t
	m.rows[objectID] = s
	return s, nil
}

func (m *Memory) Latest(ctx context.Context, projectID string) (Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.latestLocked(projectID)
	if !ok {
		return Snapshot{}, fmt.Errorf("%w: no snapshot of %s", ErrNotFound, projectID)
	}
	return s, nil
}

func (m *Memory) latestLocked(projectID string) (Snapshot, bool) {
	var latest Snapshot
	for _, s := range m.rows {
		if s.ProjectID == projectID && s.Version > latest.Version {
			latest = s
		}
	}
	return latest, latest.Version != 0
}

func (m *Memory) List(ctx context.Context, projectID string, before, limit int) ([]Snapshot, error) {
	if err := checkLimit(limit); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Snapshot
	for _, s := range m.rows {
		if s.ProjectID == projectID && s.Version != 0 && (before == 0 || s.Version < before) {
			out = append(out, s)
		}
	}
	slices.SortFunc(out, func(a, b Snapshot) int { return b.Version - a.Version })
	return out[:min(limit, len(out))], nil
}

func (m *Memory) ListExpiredPending(ctx context.Context, before time.Time, limit int) ([]Snapshot, error) {
	if err := checkLimit(limit); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Snapshot
	for _, s := range m.rows {
		if s.Version == 0 && s.ExpiresAt.Before(before) {
			out = append(out, s)
		}
	}
	slices.SortFunc(out, func(a, b Snapshot) int { return a.ExpiresAt.Compare(b.ExpiresAt) })
	return out[:min(limit, len(out))], nil
}

func (m *Memory) ListBeyondKeep(ctx context.Context, keep, limit int) ([]Snapshot, error) {
	if err := checkLimit(limit); err != nil {
		return nil, err
	}
	if keep < 1 {
		return nil, fmt.Errorf("%w: keep %d", ErrInvalid, keep)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	latest := map[string]int{}
	for _, s := range m.rows {
		latest[s.ProjectID] = max(latest[s.ProjectID], s.Version)
	}
	var out []Snapshot
	for _, s := range m.rows {
		if s.Version != 0 && s.Version <= latest[s.ProjectID]-keep {
			out = append(out, s)
		}
	}
	slices.SortFunc(out, func(a, b Snapshot) int {
		return cmp.Or(cmp.Compare(a.ProjectID, b.ProjectID), cmp.Compare(a.Version, b.Version))
	})
	return out[:min(limit, len(out))], nil
}

func (m *Memory) Delete(ctx context.Context, objectID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.rows, objectID)
	return nil
}

func (m *Memory) DeleteProject(ctx context.Context, projectID string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for id, s := range m.rows {
		if s.ProjectID == projectID {
			delete(m.rows, id)
			n++
		}
	}
	return n, nil
}

func (m *Memory) ClearEditedBy(ctx context.Context, userID string) (int, error) {
	if userID == "" {
		return 0, fmt.Errorf("%w: empty user ID", ErrInvalid)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for id, s := range m.rows {
		if s.EditedBy == userID {
			s.EditedBy = ""
			m.rows[id] = s
			n++
		}
	}
	return n, nil
}
