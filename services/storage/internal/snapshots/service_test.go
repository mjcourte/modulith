package snapshots_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"gyoza/services/storage/internal/snapshots"
)

// runStoreTests is the suite every Store runs. newStore returns an empty
// store (or a rolled-back transaction of the real one).
func runStoreTests(t *testing.T, newStore func(t *testing.T) snapshots.Store, concurrent bool) {
	tests := []struct {
		name string
		fn   func(*testing.T, snapshots.Store)
	}{
		{"BeginThenComplete", testSnapshotBeginThenComplete},
		{"CompleteIsIdempotent", testSnapshotCompleteIsIdempotent},
		{"VersionsPerProject", testSnapshotVersionsPerProject},
		{"StaleRefusedAtBegin", testSnapshotStaleRefusedAtBegin},
		{"StaleRefusedAtComplete", testSnapshotStaleRefusedAtComplete},
		{"EqualSeqAllowed", testSnapshotEqualSeqAllowed},
		{"Missing", testSnapshotMissing},
		{"RejectsInvalid", testSnapshotRejectsInvalid},
		{"List", testSnapshotList},
		{"ExpiredPending", testSnapshotExpiredPending},
		{"BeyondKeep", testSnapshotBeyondKeep},
		{"Delete", testSnapshotDelete},
		{"DeleteProject", testSnapshotDeleteProject},
		{"ClearEditedBy", testSnapshotClearEditedBy},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { tc.fn(t, newStore(t)) })
	}
	if concurrent {
		// Needs real concurrency: Postgres runs it on a pool, not one tx.
		t.Run("ConcurrentCompletes", func(t *testing.T) { testSnapshotConcurrentCompletes(t, newStore(t)) })
	}
}

func project() string { return uuid.Must(uuid.NewV7()).String() }

func pending(projectID string, seq uint64) snapshots.Snapshot {
	id := uuid.Must(uuid.NewV7()).String()
	return snapshots.Snapshot{
		ObjectID: id, ProjectID: projectID, Seq: seq,
		EditedBy: "ann", EditedAt: time.Now().Add(-time.Minute),
		Bucket: "b", Key: "projects/" + projectID + "/snapshot/" + id,
		ContentType: "application/vnd.gyoza.app+flatbuffers; version=1", SizeBytes: 100,
		ExpiresAt: time.Now().Add(15 * time.Minute),
	}
}

func begin(t *testing.T, s snapshots.Store, p snapshots.Snapshot) snapshots.Snapshot {
	t.Helper()
	got, err := s.Begin(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func complete(t *testing.T, s snapshots.Store, objectID string) snapshots.Snapshot {
	t.Helper()
	got, err := s.Complete(context.Background(), objectID)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// store puts a completed snapshot at seq.
func store(t *testing.T, s snapshots.Store, projectID string, seq uint64) snapshots.Snapshot {
	t.Helper()
	return complete(t, s, begin(t, s, pending(projectID, seq)).ObjectID)
}

func testSnapshotBeginThenComplete(t *testing.T, s snapshots.Store) {
	ctx := context.Background()
	p := project()
	in := pending(p, 42)
	got := begin(t, s, in)
	if got.Version != 0 || got.CompletedAt != nil {
		t.Fatalf("begun snapshot %+v is not pending", got)
	}
	if _, err := s.Latest(ctx, p); !errors.Is(err, snapshots.ErrNotFound) {
		t.Errorf("a pending snapshot is Latest: got %v, want ErrNotFound", err)
	}
	done := complete(t, s, in.ObjectID)
	if done.Version != 1 || done.CompletedAt == nil || done.Seq != 42 || done.EditedBy != "ann" ||
		!done.EditedAt.Equal(in.EditedAt.Truncate(time.Microsecond)) || done.Key != in.Key || done.SizeBytes != 100 {
		t.Errorf("completed %+v, want version 1 with the begun fields", done)
	}
	if latest, err := s.Latest(ctx, p); err != nil || latest.ObjectID != in.ObjectID {
		t.Errorf("Latest = %+v, %v; want the completed one", latest, err)
	}
}

func testSnapshotCompleteIsIdempotent(t *testing.T, s snapshots.Store) {
	p := project()
	first := store(t, s, p, 1)
	again := complete(t, s, first.ObjectID)
	if again.Version != first.Version || !again.CompletedAt.Equal(*first.CompletedAt) {
		t.Errorf("completing again gave %+v, want the same %+v", again, first)
	}
	if next := store(t, s, p, 2); next.Version != 2 {
		t.Errorf("next version = %d, want 2 (a repeat must not use one up)", next.Version)
	}
}

func testSnapshotVersionsPerProject(t *testing.T, s snapshots.Store) {
	a, b := project(), project()
	store(t, s, a, 1)
	store(t, s, a, 2)
	if got := store(t, s, b, 1).Version; got != 1 {
		t.Errorf("b's first version = %d, want 1", got)
	}
	if got := store(t, s, a, 3).Version; got != 3 {
		t.Errorf("a's third version = %d, want 3", got)
	}
}

func testSnapshotStaleRefusedAtBegin(t *testing.T, s snapshots.Store) {
	p := project()
	store(t, s, p, 20)
	if _, err := s.Begin(context.Background(), pending(p, 10)); !errors.Is(err, snapshots.ErrStale) {
		t.Errorf("begin at seq 10 after 20: got %v, want ErrStale", err)
	}
}

// Begun before a newer snapshot completed, so Begin couldn't refuse it.
func testSnapshotStaleRefusedAtComplete(t *testing.T, s snapshots.Store) {
	ctx := context.Background()
	p := project()
	old := begin(t, s, pending(p, 10))
	newer := store(t, s, p, 20)
	if _, err := s.Complete(ctx, old.ObjectID); !errors.Is(err, snapshots.ErrStale) {
		t.Fatalf("completing seq 10 after 20: got %v, want ErrStale", err)
	}
	if latest, _ := s.Latest(ctx, p); latest.ObjectID != newer.ObjectID {
		t.Errorf("Latest = %s after a stale complete, want %s", latest.ObjectID, newer.ObjectID)
	}
}

func testSnapshotEqualSeqAllowed(t *testing.T, s snapshots.Store) {
	p := project()
	store(t, s, p, 20)
	if got := store(t, s, p, 20); got.Version != 2 {
		t.Errorf("equal seq: version %d, want 2 (a later save at the same seq holds more local edits)", got.Version)
	}
}

func testSnapshotMissing(t *testing.T, s snapshots.Store) {
	ctx := context.Background()
	if _, err := s.Get(ctx, "nope"); !errors.Is(err, snapshots.ErrNotFound) {
		t.Errorf("Get: got %v, want ErrNotFound", err)
	}
	if _, err := s.Complete(ctx, "nope"); !errors.Is(err, snapshots.ErrNotFound) {
		t.Errorf("Complete: got %v, want ErrNotFound", err)
	}
	if err := s.Delete(ctx, "nope"); err != nil {
		t.Errorf("Delete of a missing row: %v, want nil", err)
	}
}

func testSnapshotRejectsInvalid(t *testing.T, s snapshots.Store) {
	ctx := context.Background()
	p := project()
	for name, mutate := range map[string]func(*snapshots.Snapshot){
		"no project":   func(x *snapshots.Snapshot) { x.ProjectID = "" },
		"no object":    func(x *snapshots.Snapshot) { x.ObjectID = "" },
		"no key":       func(x *snapshots.Snapshot) { x.Key = "" },
		"no type":      func(x *snapshots.Snapshot) { x.ContentType = "" },
		"empty":        func(x *snapshots.Snapshot) { x.SizeBytes = 0 },
		"no expiry":    func(x *snapshots.Snapshot) { x.ExpiresAt = time.Time{} },
		"seq too high": func(x *snapshots.Snapshot) { x.Seq = 1 << 63 },
	} {
		in := pending(p, 1)
		mutate(&in)
		if _, err := s.Begin(ctx, in); !errors.Is(err, snapshots.ErrInvalid) {
			t.Errorf("%s: got %v, want ErrInvalid", name, err)
		}
	}
	dup := begin(t, s, pending(p, 1))
	if _, err := s.Begin(ctx, dup); !errors.Is(err, snapshots.ErrInvalid) {
		t.Errorf("same object twice: got %v, want ErrInvalid", err)
	}
	if _, err := s.List(ctx, p, 0, 0); !errors.Is(err, snapshots.ErrInvalid) {
		t.Errorf("List limit 0: got %v, want ErrInvalid", err)
	}
}

func testSnapshotList(t *testing.T, s snapshots.Store) {
	ctx := context.Background()
	p := project()
	for seq := range uint64(5) {
		store(t, s, p, seq)
	}
	begin(t, s, pending(p, 9)) // pending: never listed
	store(t, s, project(), 1)  // another project
	versions := func(list []snapshots.Snapshot) []int {
		var v []int
		for _, x := range list {
			v = append(v, x.Version)
		}
		return v
	}
	page, err := s.List(ctx, p, 0, 2)
	if err != nil || fmt.Sprint(versions(page)) != "[5 4]" {
		t.Fatalf("first page = %v, %v; want [5 4]", versions(page), err)
	}
	page, err = s.List(ctx, p, 4, 10)
	if err != nil || fmt.Sprint(versions(page)) != "[3 2 1]" {
		t.Errorf("before 4 = %v, %v; want [3 2 1]", versions(page), err)
	}
}

func testSnapshotExpiredPending(t *testing.T, s snapshots.Store) {
	ctx := context.Background()
	p := project()
	expired := pending(p, 1)
	expired.ExpiresAt = time.Now().Add(-time.Hour)
	begin(t, s, expired)
	begin(t, s, pending(p, 1)) // not expired yet
	done := pending(p, 1)
	done.ExpiresAt = time.Now().Add(-time.Hour)
	complete(t, s, begin(t, s, done).ObjectID) // completed: not pending

	got, err := s.ListExpiredPending(ctx, time.Now(), 100)
	if err != nil {
		t.Fatal(err)
	}
	var ours []string
	for _, x := range got {
		if x.ProjectID == p {
			ours = append(ours, x.ObjectID)
		}
	}
	if len(ours) != 1 || ours[0] != expired.ObjectID {
		t.Errorf("expired pending = %v, want only %s", ours, expired.ObjectID)
	}
}

func testSnapshotBeyondKeep(t *testing.T, s snapshots.Store) {
	ctx := context.Background()
	p, small := project(), project()
	for seq := range uint64(5) {
		store(t, s, p, seq)
	}
	store(t, s, small, 1) // fewer than keep: nothing to drop
	got, err := s.ListBeyondKeep(ctx, 3, 100)
	if err != nil {
		t.Fatal(err)
	}
	var ours []int
	for _, x := range got {
		switch x.ProjectID {
		case p:
			ours = append(ours, x.Version)
		case small:
			t.Errorf("listed %s's only snapshot", small)
		}
	}
	if fmt.Sprint(ours) != "[1 2]" {
		t.Errorf("beyond keep 3 = %v, want [1 2] (oldest first)", ours)
	}
	if _, err := s.ListBeyondKeep(ctx, 0, 100); !errors.Is(err, snapshots.ErrInvalid) {
		t.Errorf("keep 0: got %v, want ErrInvalid", err)
	}
}

func testSnapshotDelete(t *testing.T, s snapshots.Store) {
	ctx := context.Background()
	p := project()
	first := store(t, s, p, 1)
	second := store(t, s, p, 2)
	if err := s.Delete(ctx, first.ObjectID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, first.ObjectID); !errors.Is(err, snapshots.ErrNotFound) {
		t.Errorf("Get after Delete: got %v, want ErrNotFound", err)
	}
	if latest, _ := s.Latest(ctx, p); latest.ObjectID != second.ObjectID {
		t.Error("deleting an old snapshot changed Latest")
	}
	if next := store(t, s, p, 3); next.Version != 3 {
		t.Errorf("version after deleting an old one = %d, want 3", next.Version)
	}
}

// Many completions at once for one project: versions are distinct and
// consecutive, and none at an earlier seq than the latest gets through. Half
// share one seq (all must succeed, so they contend for versions), half are
// spread out (the stale check races the completions).
//
// Removing Postgres's per-project lock doesn't fail this locally: completions
// are too quick to overlap here. UNIQUE (project_id, version) still makes
// duplicate versions impossible; the lock is what turns a lost race into a
// clean ErrStale rather than a unique-violation error.
func testSnapshotConcurrentCompletes(t *testing.T, s snapshots.Store) {
	ctx := context.Background()
	p := project()
	var ids []string
	for seq := range uint64(100) {
		seqs := []uint64{5, seq % 10}
		ids = append(ids, begin(t, s, pending(p, seqs[seq%2])).ObjectID)
	}
	t.Cleanup(func() { // on the pool these rows are committed: don't leave them behind
		for _, id := range ids {
			s.Delete(context.Background(), id)
		}
	})
	start := make(chan struct{}) // released together, to overlap as much as possible
	var wg sync.WaitGroup
	var mu sync.Mutex
	done := map[int]uint64{} // version -> seq
	for _, id := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			got, err := s.Complete(ctx, id)
			if errors.Is(err, snapshots.ErrStale) {
				return
			}
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			if _, dup := done[got.Version]; dup {
				t.Errorf("version %d given twice", got.Version)
			}
			done[got.Version] = got.Seq
		}()
	}
	close(start)
	wg.Wait()
	for v := 1; v <= len(done); v++ {
		seq, ok := done[v]
		if !ok {
			t.Fatalf("versions %v aren't consecutive from 1", done)
		}
		if v > 1 && seq < done[v-1] {
			t.Errorf("version %d covers seq %d, before version %d's %d", v, seq, v-1, done[v-1])
		}
	}
}

func testSnapshotDeleteProject(t *testing.T, s snapshots.Store) {
	ctx := context.Background()
	p, other := project(), project()
	store(t, s, p, 1)
	begin(t, s, pending(p, 2))
	kept := store(t, s, other, 1)
	if n, err := s.DeleteProject(ctx, p); err != nil || n != 2 {
		t.Fatalf("DeleteProject = %d, %v; want 2 (completed and pending)", n, err)
	}
	if _, err := s.Latest(ctx, p); !errors.Is(err, snapshots.ErrNotFound) {
		t.Errorf("Latest after DeleteProject: got %v, want ErrNotFound", err)
	}
	if latest, _ := s.Latest(ctx, other); latest.ObjectID != kept.ObjectID {
		t.Error("DeleteProject touched another project")
	}
}

func testSnapshotClearEditedBy(t *testing.T, s snapshots.Store) {
	ctx := context.Background()
	p := project()
	mine := store(t, s, p, 1) // edited by ann
	other := pending(p, 2)
	other.EditedBy = "bob"
	theirs := complete(t, s, begin(t, s, other).ObjectID)
	if n, err := s.ClearEditedBy(ctx, "ann"); err != nil || n < 1 {
		t.Fatalf("ClearEditedBy = %d, %v", n, err)
	}
	if got, _ := s.Get(ctx, mine.ObjectID); got.EditedBy != "" {
		t.Errorf("ann still edited %s", got.ObjectID)
	}
	if got, _ := s.Get(ctx, theirs.ObjectID); got.EditedBy != "bob" {
		t.Errorf("bob's edit lost its editor: %q", got.EditedBy)
	}
	if _, err := s.ClearEditedBy(ctx, ""); !errors.Is(err, snapshots.ErrInvalid) {
		t.Errorf("empty user: got %v, want ErrInvalid", err)
	}
}
