// Package snapshotstest is the shared test suite for snapshots.Store
// implementations. It's its own package, like net/http/httptest, so the
// testing package is never compiled into binaries that only use snapshots.
package snapshotstest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"gyoza/internal/engine"
	"gyoza/services/documents/internal/snapshots"
)

// RunSuite checks the behaviour every Store must share. newStore must return
// an empty store, or one where the projects the suite uses (unique per call)
// have no snapshots.
func RunSuite(t *testing.T, newStore func(t *testing.T) snapshots.Store) {
	tests := []struct {
		name string
		fn   func(*testing.T, snapshots.Store, func(string) string)
	}{
		{"LatestMissing", testSnapshotsLatestMissing},
		{"PutGetRoundTrip", testSnapshotsPutGetRoundTrip},
		{"LatestIsNewest", testSnapshotsLatestIsNewest},
		{"ProjectsAreSeparate", testSnapshotsProjectsAreSeparate},
		{"GetMissing", testSnapshotsGetMissing},
		{"RejectsInvalid", testSnapshotsRejectsInvalid},
		{"RejectsStale", testSnapshotsRejectsStale},
		{"Version", testSnapshotsVersion},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Unique project IDs, so stores shared between runs (storage's
			// dev database) don't see earlier tests' snapshots.
			suffix := fmt.Sprintf("-%d", time.Now().UnixNano())
			tc.fn(t, newStore(t), func(p string) string { return p + suffix })
		})
	}
}

// editedAt is a fixed edit time, at microseconds as storage keeps it.
var editedAt = time.Date(2026, 10, 12, 9, 30, 0, 123456000, time.UTC)

func snap(payload string) engine.Snapshot {
	return engine.Snapshot{Format: engine.WireNative, Version: 1, Payload: []byte(payload)}
}

func put(t *testing.T, s snapshots.Store, projectID, payload string, seq uint64) snapshots.Ref {
	t.Helper()
	ref, err := s.Put(context.Background(), projectID, snap(payload), seq, snapshots.Edit{By: "ann", At: editedAt})
	if err != nil {
		t.Fatal(err)
	}
	return ref
}

func testSnapshotsLatestMissing(t *testing.T, s snapshots.Store, id func(string) string) {
	if _, err := s.Latest(context.Background(), id("p")); !errors.Is(err, snapshots.ErrNotFound) {
		t.Fatalf("Latest of a project without snapshots: got %v, want ErrNotFound", err)
	}
}

func testSnapshotsPutGetRoundTrip(t *testing.T, s snapshots.Store, id func(string) string) {
	ctx := context.Background()
	p := id("p")
	in := engine.Snapshot{Format: engine.WireJSON, Version: 7, Payload: []byte("state")}
	ref, err := s.Put(ctx, p, in, 42, snapshots.Edit{By: "bob", At: editedAt})
	if err != nil {
		t.Fatal(err)
	}
	if ref.ProjectID != p || ref.ObjectID == "" || ref.Version != 1 || ref.Seq != 42 || ref.EditedBy != "bob" || !ref.EditedAt.Equal(editedAt) {
		t.Errorf("ref = %+v, want project %s, an object ID, version 1, seq 42, edited by bob", ref, p)
	}
	in.Payload[0] = 'X' // the store must not keep the caller's buffer
	got, err := s.Get(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	if got.Format != engine.WireJSON || got.Version != 7 || !bytes.Equal(got.Payload, []byte("state")) {
		t.Errorf("Get = %s v%d %q, want json v7 \"state\"", got.Format, got.Version, got.Payload)
	}
}

func testSnapshotsLatestIsNewest(t *testing.T, s snapshots.Store, id func(string) string) {
	ctx := context.Background()
	p := id("p")
	put(t, s, p, "v1", 10)
	put(t, s, p, "v2", 20)
	third := put(t, s, p, "v3", 30)

	latest, err := s.Latest(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if latest != third || latest.Version != 3 || latest.Seq != 30 {
		t.Fatalf("Latest = %+v, want the third put %+v (version 3, seq 30)", latest, third)
	}
	got, err := s.Get(ctx, latest)
	if err != nil || string(got.Payload) != "v3" {
		t.Errorf("Get(latest) = %q, %v; want v3", got.Payload, err)
	}
}

func testSnapshotsProjectsAreSeparate(t *testing.T, s snapshots.Store, id func(string) string) {
	ctx := context.Background()
	a, b := id("a"), id("b")
	put(t, s, a, "a1", 1)
	put(t, s, a, "a2", 2)
	refB := put(t, s, b, "b1", 3)
	if refB.Version != 1 {
		t.Errorf("b's first snapshot has version %d, want 1 (versions are per project)", refB.Version)
	}
	latest, err := s.Latest(ctx, b)
	if err != nil || latest != refB {
		t.Errorf("Latest(b) = %+v, %v; want %+v", latest, err, refB)
	}
	// A ref can't be used to read another project's snapshot.
	stolen := refB
	stolen.ProjectID = a
	if _, err := s.Get(ctx, stolen); !errors.Is(err, snapshots.ErrNotFound) {
		t.Errorf("Get of b's object as a's: got %v, want ErrNotFound", err)
	}
}

func testSnapshotsGetMissing(t *testing.T, s snapshots.Store, id func(string) string) {
	ref := snapshots.Ref{ProjectID: id("p"), ObjectID: "no-such-object", Version: 1}
	if _, err := s.Get(context.Background(), ref); !errors.Is(err, snapshots.ErrNotFound) {
		t.Fatalf("Get of a missing object: got %v, want ErrNotFound", err)
	}
}

func testSnapshotsRejectsInvalid(t *testing.T, s snapshots.Store, id func(string) string) {
	ctx := context.Background()
	cases := map[string]struct {
		project string
		snap    engine.Snapshot
	}{
		"no project":     {"", snap("x")},
		"unknown format": {id("p"), engine.Snapshot{Format: "xml", Version: 1, Payload: []byte("x")}},
		"empty payload":  {id("p"), engine.Snapshot{Format: engine.WireNative, Version: 1}},
	}
	for name, c := range cases {
		if _, err := s.Put(ctx, c.project, c.snap, 1, snapshots.Edit{}); !errors.Is(err, snapshots.ErrInvalid) {
			t.Errorf("%s: got %v, want ErrInvalid", name, err)
		}
	}
	if _, err := s.Latest(ctx, id("p")); !errors.Is(err, snapshots.ErrNotFound) {
		t.Errorf("a rejected Put left a snapshot: Latest got %v", err)
	}
}

func testSnapshotsRejectsStale(t *testing.T, s snapshots.Store, id func(string) string) {
	ctx := context.Background()
	p := id("p")
	newer := put(t, s, p, "covers 20", 20)
	if _, err := s.Put(ctx, p, snap("covers 10"), 10, snapshots.Edit{}); !errors.Is(err, snapshots.ErrStale) {
		t.Fatalf("Put at seq 10 after seq 20: got %v, want ErrStale", err)
	}
	if latest, _ := s.Latest(ctx, p); latest != newer {
		t.Fatalf("a stale Put changed Latest to %+v, want %+v", latest, newer)
	}
	same := put(t, s, p, "covers 20, more local edits", 20) // equal is allowed
	if latest, _ := s.Latest(ctx, p); latest != same || same.Version != 2 {
		t.Errorf("Latest = %+v after a Put at the same seq, want %+v (version 2)", latest, same)
	}
	put(t, s, id("other"), "another project at seq 1", 1) // per project
}

func testSnapshotsVersion(t *testing.T, s snapshots.Store, id func(string) string) {
	ctx := context.Background()
	p := id("p")
	put(t, s, p, "v1", 1)
	second := put(t, s, p, "v2", 2)
	put(t, s, p, "v3", 3)
	got, err := s.Version(ctx, p, 2)
	if err != nil || got != second {
		t.Fatalf("Version(2) = %+v, %v; want %+v", got, err, second)
	}
	snap, err := s.Get(ctx, got)
	if err != nil || string(snap.Payload) != "v2" {
		t.Errorf("its bytes = %q, %v; want v2", snap.Payload, err)
	}
	for _, v := range []int{0, 4} {
		if _, err := s.Version(ctx, p, v); !errors.Is(err, snapshots.ErrNotFound) {
			t.Errorf("Version(%d): got %v, want ErrNotFound", v, err)
		}
	}
	if _, err := s.Version(ctx, id("other"), 1); !errors.Is(err, snapshots.ErrNotFound) {
		t.Errorf("another project's version: got %v, want ErrNotFound", err)
	}
}
