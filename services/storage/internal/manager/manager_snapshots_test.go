package manager_test

import (
	"bytes"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/mjcourte/modulith/services/storage/internal/blob"
	"github.com/mjcourte/modulith/services/storage/internal/ledger"
	sm "github.com/mjcourte/modulith/services/storage/internal/manager"
)

const (
	snapProject = "proj-snap"
	snapType    = "application/vnd.modulith.app+flatbuffers; version=1"
)

// newSnapshotManager is a test Manager that knows snapProject, owned by acme.
func newSnapshotManager(t *testing.T, cfg sm.Config) testManager {
	t.Helper()
	cfg.Bucket = testBucket
	tm := newTestManagerWith(t, cfg, nil)
	tm.authz.SetProjectOwner(snapProject, acme)
	return tm
}

func snapRequest(seq uint64, data []byte) sm.SnapshotRequest {
	return sm.SnapshotRequest{
		ProjectID: snapProject, Seq: seq, EditedBy: "ann", EditedAt: time.Now().Add(-time.Minute).UTC().Truncate(time.Microsecond),
		ContentType: snapType, SizeBytes: int64(len(data)),
	}
}

// beginAndUpload begins a snapshot and PUTs data like documents does.
func beginAndUpload(t *testing.T, tm testManager, seq uint64, data []byte) sm.SnapshotTicket {
	t.Helper()
	ticket, err := tm.BeginSnapshot(t.Context(), snapRequest(seq, data))
	if err != nil {
		t.Fatalf("BeginSnapshot: %v", err)
	}
	if err := tm.blobs.PutSigned(ticket.URL, ticket.Headers["Content-Type"], data); err != nil {
		t.Fatalf("PUT: %v", err)
	}
	return ticket
}

func storeSnapshot(t *testing.T, tm testManager, seq uint64, data []byte) sm.SnapshotRef {
	t.Helper()
	ticket := beginAndUpload(t, tm, seq, data)
	ref, err := tm.CompleteSnapshot(t.Context(), snapProject, ticket.ObjectID)
	if err != nil {
		t.Fatalf("CompleteSnapshot: %v", err)
	}
	return ref
}

func TestSnapshotRoundTrip(t *testing.T) {
	tm := newSnapshotManager(t, sm.Config{})
	ctx := t.Context()
	data := []byte("ecs tables")
	req := snapRequest(42, data)
	ref := storeSnapshot(t, tm, 42, data)
	if ref.Version != 1 || ref.Seq != 42 || ref.EditedBy != "ann" || !ref.EditedAt.Equal(req.EditedAt) || ref.SizeBytes != int64(len(data)) {
		t.Errorf("ref = %+v, want version 1 at seq 42, edited by ann", ref)
	}
	obj, err := tm.ledger.Get(ctx, ref.ObjectID)
	if err != nil {
		t.Fatal(err)
	}
	if obj.Kind != ledger.KindSnapshot || obj.Owner != (ledger.Owner{Kind: ledger.OwnerProject, ID: snapProject}) || obj.CreatedBy != "ann" || obj.ProjectID != snapProject {
		t.Errorf("ledger object = %+v, want a snapshot owned by the project, created by ann", obj)
	}
	if latest, err := tm.LatestSnapshot(ctx, snapProject); err != nil || latest != ref {
		t.Errorf("LatestSnapshot = %+v, %v; want %+v", latest, err, ref)
	}
	dl, err := tm.SnapshotURL(ctx, snapProject, ref.ObjectID)
	if err != nil {
		t.Fatal(err)
	}
	got, _, err := tm.blobs.GetSigned(dl.URL)
	if err != nil || !bytes.Equal(got, data) {
		t.Errorf("downloaded %q, %v; want %q", got, err, data)
	}
	again, err := tm.CompleteSnapshot(ctx, snapProject, ref.ObjectID)
	if err != nil || again != ref {
		t.Errorf("completing again = %+v, %v; want the same ref", again, err)
	}
}

func TestSnapshotBeginRefuses(t *testing.T) {
	tm := newSnapshotManager(t, sm.Config{MaxSnapshotBytes: 1000})
	ctx := t.Context()
	req := snapRequest(1, make([]byte, 1001))
	if _, err := tm.BeginSnapshot(ctx, req); !errors.Is(err, sm.ErrSnapshotTooLarge) {
		t.Errorf("over the cap: got %v, want ErrSnapshotTooLarge", err)
	}
	req = snapRequest(1, []byte("x"))
	req.ContentType = "image/png"
	if _, err := tm.BeginSnapshot(ctx, req); !errors.Is(err, sm.ErrInvalidSnapshot) {
		t.Errorf("not a snapshot type: got %v, want ErrInvalidSnapshot", err)
	}
	req = snapRequest(1, []byte("x"))
	req.ProjectID = "no-such-project"
	if _, err := tm.BeginSnapshot(ctx, req); !errors.Is(err, sm.ErrForbidden) {
		t.Errorf("unknown project: got %v, want ErrForbidden", err)
	}
	storeSnapshot(t, tm, 20, []byte("newer"))
	if _, err := tm.BeginSnapshot(ctx, snapRequest(10, []byte("older"))); !errors.Is(err, sm.ErrSnapshotStale) {
		t.Errorf("before the latest seq: got %v, want ErrSnapshotStale", err)
	}
}

func TestSnapshotCompleteRefuses(t *testing.T) {
	tm := newSnapshotManager(t, sm.Config{})
	ctx := t.Context()
	ticket, err := tm.BeginSnapshot(ctx, snapRequest(1, []byte("data")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tm.CompleteSnapshot(ctx, snapProject, ticket.ObjectID); !errors.Is(err, sm.ErrNotUploaded) {
		t.Errorf("before the PUT: got %v, want ErrNotUploaded", err)
	}
	if _, err := tm.CompleteSnapshot(ctx, "another-project", ticket.ObjectID); !errors.Is(err, sm.ErrSnapshotNotFound) {
		t.Errorf("as another project: got %v, want ErrSnapshotNotFound", err)
	}
	if err := tm.blobs.PutSigned(ticket.URL, snapType, []byte("data")); err != nil {
		t.Fatal(err)
	}
	sm.SetClock(tm.Manager, func() time.Time { return time.Now().Add(time.Hour) })
	if _, err := tm.CompleteSnapshot(ctx, snapProject, ticket.ObjectID); !errors.Is(err, sm.ErrSnapshotExpired) {
		t.Errorf("after expiry: got %v, want ErrSnapshotExpired", err)
	}
}

// A snapshot begun before a newer one completed is refused at completion,
// and its object and ledger row are removed: the latest never goes back.
func TestSnapshotStaleCompleteIsDropped(t *testing.T) {
	tm := newSnapshotManager(t, sm.Config{})
	ctx := t.Context()
	old := beginAndUpload(t, tm, 10, []byte("old"))
	newer := storeSnapshot(t, tm, 20, []byte("newer"))
	if _, err := tm.CompleteSnapshot(ctx, snapProject, old.ObjectID); !errors.Is(err, sm.ErrSnapshotStale) {
		t.Fatalf("got %v, want ErrSnapshotStale", err)
	}
	if latest, _ := tm.LatestSnapshot(ctx, snapProject); latest.ObjectID != newer.ObjectID {
		t.Errorf("latest = %s, want %s", latest.ObjectID, newer.ObjectID)
	}
	if _, err := tm.ledger.Get(ctx, old.ObjectID); !errors.Is(err, ledger.ErrNotFound) {
		t.Errorf("stale snapshot's ledger row: got %v, want it removed", err)
	}
	if _, err := tm.blobs.Stat(ctx, testBucket, "projects/"+snapProject+"/snapshot/"+old.ObjectID); !errors.Is(err, blob.ErrNotFound) {
		t.Errorf("stale snapshot's object: got %v, want it deleted", err)
	}
}

func TestSnapshotURLRefuses(t *testing.T) {
	tm := newSnapshotManager(t, sm.Config{})
	ctx := t.Context()
	ref := storeSnapshot(t, tm, 1, []byte("x"))
	if _, err := tm.SnapshotURL(ctx, "another-project", ref.ObjectID); !errors.Is(err, sm.ErrSnapshotNotFound) {
		t.Errorf("another project's snapshot: got %v, want ErrSnapshotNotFound", err)
	}
	pending := beginAndUpload(t, tm, 2, []byte("y"))
	if _, err := tm.SnapshotURL(ctx, snapProject, pending.ObjectID); !errors.Is(err, sm.ErrSnapshotNotFound) {
		t.Errorf("a pending snapshot: got %v, want ErrSnapshotNotFound", err)
	}
}

func TestListSnapshotsPages(t *testing.T) {
	tm := newSnapshotManager(t, sm.Config{})
	ctx := t.Context()
	for seq := range uint64(55) {
		storeSnapshot(t, tm, seq, []byte(fmt.Sprint(seq)))
	}
	first, err := tm.ListSnapshots(ctx, snapProject, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Snapshots) != 50 || first.Snapshots[0].Version != 55 || first.NextPageToken == "" {
		t.Fatalf("first page: %d snapshots from v%d, token %q; want 50 from v55 and a token",
			len(first.Snapshots), first.Snapshots[0].Version, first.NextPageToken)
	}
	second, err := tm.ListSnapshots(ctx, snapProject, first.NextPageToken)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Snapshots) != 5 || second.Snapshots[0].Version != 5 || second.NextPageToken != "" {
		t.Errorf("second page: %d snapshots from v%d, token %q; want 5 from v5, no token",
			len(second.Snapshots), second.Snapshots[0].Version, second.NextPageToken)
	}
}

func TestCleanupSnapshots(t *testing.T) {
	tm := newSnapshotManager(t, sm.Config{SnapshotKeep: 2})
	ctx := t.Context()
	var refs []sm.SnapshotRef
	for seq := range uint64(4) {
		refs = append(refs, storeSnapshot(t, tm, seq, []byte(fmt.Sprint(seq))))
	}
	abandoned := beginAndUpload(t, tm, 9, []byte("never completed"))

	r, err := tm.Cleanup(ctx, time.Now().Add(time.Hour)) // past the upload's expiry
	if err != nil {
		t.Fatal(err)
	}
	if r.OldSnapshots != 2 || r.ExpiredSnapshots != 1 {
		t.Errorf("report %+v, want 2 old and 1 expired snapshot", r)
	}
	page, _ := tm.ListSnapshots(ctx, snapProject, "")
	if len(page.Snapshots) != 2 || page.Snapshots[0].Version != 4 || page.Snapshots[1].Version != 3 {
		t.Errorf("kept %+v, want versions 4 and 3", page.Snapshots)
	}
	for _, id := range []string{refs[0].ObjectID, refs[1].ObjectID, abandoned.ObjectID} {
		if _, err := tm.blobs.Stat(ctx, testBucket, "projects/"+snapProject+"/snapshot/"+id); !errors.Is(err, blob.ErrNotFound) {
			t.Errorf("object %s still stored", id)
		}
		if _, err := tm.ledger.Get(ctx, id); !errors.Is(err, ledger.ErrNotFound) {
			t.Errorf("ledger row %s still there", id)
		}
	}
}

func TestSnapshotsFollowOwnerLifecycle(t *testing.T) {
	tm := newSnapshotManager(t, sm.Config{})
	ctx := t.Context()
	storeSnapshot(t, tm, 1, []byte("x"))

	r, err := tm.RemoveUser(ctx, "ann", "")
	if err != nil {
		t.Fatal(err)
	}
	latest, _ := tm.LatestSnapshot(ctx, snapProject)
	if r.ClearedSnapshots != 1 || latest.EditedBy != "" {
		t.Errorf("after removing ann: cleared %d, editor %q; want 1 and none", r.ClearedSnapshots, latest.EditedBy)
	}

	d, err := tm.DeleteOwned(ctx, ledger.Owner{Kind: ledger.OwnerProject, ID: snapProject})
	if err != nil {
		t.Fatal(err)
	}
	if d.Snapshots != 1 {
		t.Errorf("DeleteOwned removed %d snapshot records, want 1", d.Snapshots)
	}
	if _, err := tm.LatestSnapshot(ctx, snapProject); !errors.Is(err, sm.ErrSnapshotNotFound) {
		t.Errorf("after deleting the project: got %v, want ErrSnapshotNotFound", err)
	}
}
