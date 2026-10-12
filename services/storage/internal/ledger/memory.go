// Memory implements the Go Service interface
// that for now "mocks" what we will do with some external Postgres.
package ledger

import (
	"cmp"
	"context"
	"crypto/rand"
	"fmt"
	"slices"
	"sync"
	"time"
)

type Memory struct {
	mu      sync.Mutex
	objects map[string]Object
}

func NewMemory() *Memory {
	return &Memory{objects: make(map[string]Object)}
}

// Compile-time check that *Memory satisfies Service.
var _ Service = (*Memory)(nil)

// now matches what Postgres stores: UTC, microsecond precision.
func now() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }

func (m *Memory) Create(ctx context.Context, obj Object) (Object, error) {
	obj, err := obj.normalize()
	if err != nil {
		return Object{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if obj.ID == "" {
		obj.ID = rand.Text()
	} else if existing, ok := m.objects[obj.ID]; ok {
		return existingOrTaken(existing, obj)
	}
	obj.CreatedAt = now()
	m.objects[obj.ID] = obj
	return obj, nil
}

func (m *Memory) Get(ctx context.Context, id string) (Object, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	obj, ok := m.objects[id]
	if !ok {
		return Object{}, ErrNotFound
	}
	return obj, nil
}

func (m *Memory) List(ctx context.Context, q ListQuery) (ListPage, error) {
	q, err := q.normalize()
	if err != nil {
		return ListPage{}, err
	}
	after, err := q.cursor()
	if err != nil {
		return ListPage{}, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	var matches []Object
	for _, obj := range m.objects {
		if matchesQuery(obj, q) && after.before(obj) {
			matches = append(matches, obj)
		}
	}
	// Map iteration order is random in Go; sort into the paging order.
	slices.SortFunc(matches, func(a, b Object) int {
		if c := a.CreatedAt.Compare(b.CreatedAt); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})

	return pageOf(matches, q.PageSize), nil
}

func matchesQuery(obj Object, q ListQuery) bool {
	switch {
	case q.ProjectID != "" && obj.ProjectID != q.ProjectID:
		return false
	case !q.Owner.IsZero() && obj.Owner != q.Owner:
		return false
	case q.CreatedBy != "" && obj.CreatedBy != q.CreatedBy:
		return false
	case q.Kind != "" && obj.Kind != q.Kind:
		return false
	case !q.IncludeDeleted && obj.DeletedAt != nil:
		return false
	}
	return true
}

func (m *Memory) MarkDeleted(ctx context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	obj, ok := m.objects[id]
	if !ok {
		return ErrNotFound
	}
	if obj.DeletedAt == nil {
		t := now()
		obj.DeletedAt = &t
		m.objects[id] = obj // obj is a copy; write it back
	}
	return nil
}

func (m *Memory) ClearCreatedBy(ctx context.Context, userID string) (int, error) {
	if userID == "" {
		return 0, fmt.Errorf("%w: empty userID", ErrInvalidQuery)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for id, obj := range m.objects {
		if obj.CreatedBy == userID {
			obj.CreatedBy = ""
			m.objects[id] = obj
			n++
		}
	}
	return n, nil
}

func (m *Memory) TransferOwner(ctx context.Context, from, to Owner) (int, error) {
	if err := checkTransfer(from, to); err != nil {
		return 0, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for id, obj := range m.objects {
		if obj.Owner == from {
			obj.Owner = to
			m.objects[id] = obj
			n++
		}
	}
	return n, nil
}

func (m *Memory) MarkDeletedByOwner(ctx context.Context, owner Owner) (int, error) {
	if err := checkOwnerArg(owner); err != nil {
		return 0, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	t := now()
	n := 0
	for id, obj := range m.objects {
		if obj.Owner == owner && obj.DeletedAt == nil {
			obj.DeletedAt = &t
			m.objects[id] = obj
			n++
		}
	}
	return n, nil
}

func (m *Memory) ListDeleted(ctx context.Context, before time.Time, limit int) ([]Object, error) {
	if err := checkLimit(limit); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var matches []Object
	for _, obj := range m.objects {
		if obj.DeletedAt != nil && obj.DeletedAt.Before(before) {
			matches = append(matches, obj)
		}
	}
	slices.SortFunc(matches, func(a, b Object) int {
		if c := a.DeletedAt.Compare(*b.DeletedAt); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
	return matches[:min(limit, len(matches))], nil
}

func (m *Memory) Purge(ctx context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	obj, ok := m.objects[id]
	switch {
	case !ok:
		return nil // already gone
	case obj.DeletedAt == nil:
		return fmt.Errorf("%w: %s", ErrNotDeleted, id)
	}
	delete(m.objects, id)
	return nil
}
