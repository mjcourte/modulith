package grpcapi_test

import (
	"errors"
	"testing"
	"time"

	"gyoza/services/storage/internal/manager"
)

// The snapshot flow over gRPC gives the same answers as the Manager, and its
// errors come back as the same sentinels.
func TestGRPCSnapshots(t *testing.T) {
	tm := newTestManager(t)
	tm.authz.SetProjectOwner(project.ID, acme)
	c := newGRPCClient(t, tm.Manager)
	ctx := t.Context()
	data := []byte("ecs tables")
	req := manager.SnapshotRequest{
		ProjectID: project.ID, Seq: 20, EditedBy: "ann", EditedAt: time.Now().UTC().Truncate(time.Microsecond),
		ContentType: "application/vnd.gyoza.app+flatbuffers; version=1", SizeBytes: int64(len(data)),
	}

	ticket, err := c.BeginSnapshot(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if err := tm.blobs.PutSigned(ticket.URL, ticket.Headers["Content-Type"], data); err != nil {
		t.Fatal(err)
	}
	ref, err := c.CompleteSnapshot(ctx, project.ID, ticket.ObjectID)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := tm.LatestSnapshot(ctx, project.ID)
	if ref != want || ref.Version != 1 || ref.Seq != 20 || ref.EditedBy != "ann" || !ref.EditedAt.Equal(req.EditedAt) {
		t.Errorf("CompleteSnapshot over gRPC = %+v, want %+v", ref, want)
	}
	if latest, err := c.LatestSnapshot(ctx, project.ID); err != nil || latest != want {
		t.Errorf("LatestSnapshot = %+v, %v; want %+v", latest, err, want)
	}
	page, err := c.ListSnapshots(ctx, project.ID, "")
	if err != nil || len(page.Snapshots) != 1 || page.Snapshots[0] != want {
		t.Errorf("ListSnapshots = %+v, %v", page, err)
	}
	dl, err := c.SnapshotURL(ctx, project.ID, ref.ObjectID)
	if err != nil {
		t.Fatal(err)
	}
	if got, _, err := tm.blobs.GetSigned(dl.URL); err != nil || string(got) != string(data) {
		t.Errorf("downloaded %q, %v", got, err)
	}

	stale := req
	stale.Seq = 10
	if _, err := c.BeginSnapshot(ctx, stale); !errors.Is(err, manager.ErrSnapshotStale) {
		t.Errorf("stale: got %v, want ErrSnapshotStale", err)
	}
	big := req
	big.SizeBytes = manager.DefaultMaxSnapshotBytes + 1
	if _, err := c.BeginSnapshot(ctx, big); !errors.Is(err, manager.ErrSnapshotTooLarge) {
		t.Errorf("too large: got %v, want ErrSnapshotTooLarge", err)
	}
	if _, err := c.LatestSnapshot(ctx, "no-snapshots"); !errors.Is(err, manager.ErrSnapshotNotFound) {
		t.Errorf("none: got %v, want ErrSnapshotNotFound", err)
	}
}
