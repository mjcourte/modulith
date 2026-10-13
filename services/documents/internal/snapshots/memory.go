package snapshots

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"sync"

	"gyoza/internal/engine"
)

// Memory is an in-memory Store. It isn't durable, so a Manager over it never
// compacts the stream (NewDurableMemory pretends otherwise, for tests).
type Memory struct {
	durable bool
	mu      sync.Mutex
	byID    map[string]stored // by object ID
	refs    map[string][]Ref  // by project, oldest first
	next    int               // object ID counter
}

type stored struct {
	ref  Ref
	snap engine.Snapshot
}

func NewMemory() *Memory {
	return &Memory{byID: map[string]stored{}, refs: map[string][]Ref{}}
}

// NewDurableMemory is a Memory that claims to be durable, for tests of what
// happens after a durable save (compaction). Never use it outside tests.
func NewDurableMemory() *Memory {
	m := NewMemory()
	m.durable = true
	return m
}

func (m *Memory) Durable() bool { return m.durable }

var _ Store = (*Memory)(nil)

func (m *Memory) Put(ctx context.Context, projectID string, snap engine.Snapshot, seq uint64, last Edit) (Ref, error) {
	if err := checkPut(projectID, snap); err != nil {
		return Ref{}, err
	}
	ct, _ := ContentType(snap.Format, snap.Version) // checked by checkPut
	m.mu.Lock()
	defer m.mu.Unlock()
	if refs := m.refs[projectID]; len(refs) > 0 && seq < refs[len(refs)-1].Seq {
		return Ref{}, fmt.Errorf("%w: seq %d is before the latest's %d", ErrStale, seq, refs[len(refs)-1].Seq)
	}
	m.next++
	ref := Ref{
		ProjectID:   projectID,
		ObjectID:    "snap-" + strconv.Itoa(m.next),
		Version:     len(m.refs[projectID]) + 1,
		Seq:         seq,
		EditedBy:    last.By,
		EditedAt:    last.At,
		ContentType: ct,
	}
	snap.Payload = slices.Clone(snap.Payload) // the caller may reuse its buffer
	m.byID[ref.ObjectID] = stored{ref, snap}
	m.refs[projectID] = append(m.refs[projectID], ref)
	return ref, nil
}

func (m *Memory) Latest(ctx context.Context, projectID string) (Ref, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	refs := m.refs[projectID]
	if len(refs) == 0 {
		return Ref{}, fmt.Errorf("%w: no snapshot of %q", ErrNotFound, projectID)
	}
	return refs[len(refs)-1], nil
}

func (m *Memory) Get(ctx context.Context, ref Ref) (engine.Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.byID[ref.ObjectID]
	if !ok || s.ref.ProjectID != ref.ProjectID {
		return engine.Snapshot{}, fmt.Errorf("%w: snapshot %q of %q", ErrNotFound, ref.ObjectID, ref.ProjectID)
	}
	snap := s.snap
	snap.Payload = slices.Clone(snap.Payload)
	return snap, nil
}

func (m *Memory) Version(ctx context.Context, projectID string, version int) (Ref, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, ref := range m.refs[projectID] {
		if ref.Version == version {
			return ref, nil
		}
	}
	return Ref{}, fmt.Errorf("%w: version %d of %q", ErrNotFound, version, projectID)
}
