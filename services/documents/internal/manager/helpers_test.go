package manager_test

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"

	"gyoza/internal/engine"
	"gyoza/services/documents/internal/manager"
	"gyoza/services/documents/internal/snapshots"
)

// fakeAuthz allows reads to readers and writers, writes to writers.
type fakeAuthz struct {
	readers, writers map[string]bool
}

func (a fakeAuthz) CanRead(ctx context.Context, actorID, projectID string) error {
	if a.readers[actorID] || a.writers[actorID] {
		return nil
	}
	return fmt.Errorf("%w: %s can't read %s", manager.ErrForbidden, actorID, projectID)
}

func (a fakeAuthz) CanWrite(ctx context.Context, actorID, projectID string) error {
	if a.writers[actorID] {
		return nil
	}
	return fmt.Errorf("%w: %s can't write %s", manager.ErrForbidden, actorID, projectID)
}

// fakeUses is storage's usage table: a set of rows per project.
type fakeUses struct {
	mu    sync.Mutex
	rows  map[string][]engine.AssetRef
	calls int
	fail  bool
}

func (u *fakeUses) RecordAssetUse(ctx context.Context, actorID, projectID string, ref engine.AssetRef) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.calls++
	if u.fail {
		return errors.New("storage unavailable")
	}
	if !slices.Contains(u.rows[projectID], ref) {
		u.rows[projectID] = append(u.rows[projectID], ref)
	}
	return nil
}

func (u *fakeUses) ForgetAssetUse(ctx context.Context, actorID, projectID string, ref engine.AssetRef) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.calls++
	if u.fail {
		return errors.New("storage unavailable")
	}
	u.rows[projectID] = slices.DeleteFunc(u.rows[projectID], func(r engine.AssetRef) bool { return r == ref })
	return nil
}

// of returns a project's rows, sorted.
func (u *fakeUses) of(projectID string) []engine.AssetRef {
	u.mu.Lock()
	defer u.mu.Unlock()
	rows := slices.Clone(u.rows[projectID])
	slices.SortFunc(rows, func(a, b engine.AssetRef) int {
		return cmp.Or(cmp.Compare(a.ID, b.ID), cmp.Compare(a.Version, b.Version))
	})
	return rows
}

type world struct {
	t     *testing.T
	store *snapshots.Memory
	uses  *fakeUses
	authz fakeAuthz
	m     *manager.Manager
}

// newWorld wires a Manager over the mock engine and in-memory fakes. "ann"
// can write and "rita" can only read.
func newWorld(t *testing.T) *world {
	w := &world{
		t:     t,
		store: snapshots.NewMemory(),
		uses:  &fakeUses{rows: map[string][]engine.AssetRef{}},
		authz: fakeAuthz{readers: map[string]bool{"rita": true}, writers: map[string]bool{"ann": true}},
	}
	w.restart()
	return w
}

// restart replaces the Manager with a fresh one over the same store, as if
// the process restarted. The old one is closed first, saving its documents.
func (w *world) restart() {
	if w.m != nil {
		if err := w.m.Close(context.Background()); err != nil {
			w.t.Fatal(err)
		}
	}
	m := manager.New(engine.Mock{}, w.store, w.authz, w.uses, manager.Config{Replica: "server"})
	w.m = m
	w.t.Cleanup(func() { m.Close(context.Background()) })
}

func (w *world) edit(actor, project string, sets ...engine.MockSet) (engine.Delta, error) {
	w.t.Helper()
	in, err := engine.MockDelta("", 0, sets...)
	if err != nil {
		w.t.Fatal(err)
	}
	return w.m.UpdateDocument(context.Background(), actor, project, in)
}

func (w *world) mustEdit(project string, sets ...engine.MockSet) engine.Delta {
	w.t.Helper()
	out, err := w.edit("ann", project, sets...)
	if err != nil {
		w.t.Fatal(err)
	}
	return out
}

// registers decodes a Document's snapshot into its live registers.
func registers(t *testing.T, d manager.Document) map[string]any {
	t.Helper()
	ctx := context.Background()
	doc, _ := engine.Mock{}.New(ctx, engine.Settings{ID: "check"})
	if err := doc.Merge(ctx, d.Snapshot); err != nil {
		t.Fatal(err)
	}
	v, err := doc.Query(ctx, engine.Query{Format: engine.QueryNative})
	if err != nil {
		t.Fatal(err)
	}
	var view struct{ Registers map[string]any }
	if err := json.Unmarshal(v.Data, &view); err != nil {
		t.Fatal(err)
	}
	return view.Registers
}
