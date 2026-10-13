package grpcapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	pb "gyoza/contracts/gen/go/gyoza/documents/v1"
	"gyoza/internal/engine"
	"gyoza/services/documents/internal/grpcapi"
	"gyoza/services/documents/internal/manager"
	"gyoza/services/documents/internal/snapshots"
	"gyoza/services/storage"
)

// testAuthz lets "ann" write and "rita" read; others get nothing.
type testAuthz struct{}

func (testAuthz) CanRead(ctx context.Context, actor, project string) error {
	if actor == "ann" || actor == "rita" {
		return nil
	}
	return fmt.Errorf("%w: %s", manager.ErrForbidden, actor)
}

func (testAuthz) CanWrite(ctx context.Context, actor, project string) error {
	if actor == "ann" {
		return nil
	}
	return fmt.Errorf("%w: %s", manager.ErrForbidden, actor)
}

// testUses is storage's usage table, failing on demand.
type testUses struct{ fail bool }

func (u *testUses) RecordAssetUse(ctx context.Context, actor, project string, ref engine.AssetRef) error {
	if u.fail {
		return errors.New("storage unavailable")
	}
	return nil
}

func (u *testUses) ForgetAssetUse(ctx context.Context, actor, project string, ref engine.AssetRef) error {
	return nil
}

// newClient serves a Manager over an in-memory connection.
func newClient(t *testing.T, uses *testUses) (*grpcapi.Client, *manager.Manager) {
	t.Helper()
	m := manager.New(engine.Mock{}, snapshots.NewMemory(), testAuthz{}, uses, manager.Config{Replica: "server"})
	t.Cleanup(func() { m.Close(context.Background()) })
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	pb.RegisterDocumentsServiceServer(srv, grpcapi.NewServer(m))
	go srv.Serve(lis)
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient("passthrough:///bufconn",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return grpcapi.NewClient(conn), m
}

func set(t *testing.T, key string, value any) engine.DeltaInput {
	t.Helper()
	in, err := engine.MockDelta("", 0, engine.MockSet{Key: key, Value: value})
	if err != nil {
		t.Fatal(err)
	}
	return in
}

func TestGRPCRoundTrips(t *testing.T) {
	c, m := newClient(t, &testUses{})
	ctx := context.Background()

	if _, err := c.InitDocument(ctx, "ann", "p"); err != nil {
		t.Fatal(err)
	}
	out, err := c.UpdateDocument(ctx, "ann", "p", set(t, "title", "Poster"))
	if err != nil || out.Format != engine.WireNative || len(out.Payload) == 0 {
		t.Fatalf("UpdateDocument = %s with %d bytes, %v", out.Format, len(out.Payload), err)
	}
	remote, err := c.GetDocument(ctx, "rita", "p")
	if err != nil {
		t.Fatal(err)
	}
	local, _ := m.GetDocument(ctx, "rita", "p")
	if string(remote.Snapshot.Payload) != string(local.Snapshot.Payload) || remote.Seq != local.Seq ||
		remote.Snapshot.Format != local.Snapshot.Format || remote.Snapshot.Version != local.Snapshot.Version {
		t.Errorf("GetDocument over gRPC differs from in-process")
	}
	v, err := c.QueryDocument(ctx, "rita", "p", engine.Query{Format: engine.QueryNative, Variables: map[string]any{"x": 1}})
	if err != nil {
		t.Fatal(err)
	}
	var view struct{ Registers map[string]any }
	if err := json.Unmarshal(v.Data, &view); err != nil || view.Registers["title"] != "Poster" {
		t.Errorf("QueryDocument = %s, %v", v.Data, err)
	}
}

func TestGRPCErrors(t *testing.T) {
	c, _ := newClient(t, &testUses{})
	ctx := context.Background()
	if _, err := c.GetDocument(ctx, "mallory", "p"); !errors.Is(err, manager.ErrForbidden) {
		t.Errorf("stranger: got %v, want ErrForbidden", err)
	}
	if _, err := c.InitDocument(ctx, "ann", "p"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.InitDocument(ctx, "ann", "p"); !errors.Is(err, manager.ErrExists) {
		t.Errorf("second init: got %v, want ErrExists", err)
	}
	bad := engine.DeltaInput{Format: engine.WireJSON, Version: 1, Payload: []byte("{}")}
	if _, err := c.UpdateDocument(ctx, "ann", "p", bad); !errors.Is(err, engine.ErrUnsupportedFormat) {
		t.Errorf("json delta: got %v, want engine.ErrUnsupportedFormat", err)
	}
	forged, _ := engine.MockDelta("attacker", engine.HLC(time.Now().Add(time.Hour)), engine.MockSet{Key: "k", Value: "x"})
	if _, err := c.UpdateDocument(ctx, "ann", "p", forged); !errors.Is(err, engine.ErrBadStamp) {
		t.Errorf("future stamp: got %v, want engine.ErrBadStamp", err)
	}
	if _, err := c.QueryDocument(ctx, "rita", "p", engine.Query{Format: "sql"}); !errors.Is(err, engine.ErrUnsupportedFormat) {
		t.Errorf("unknown query format: got %v, want engine.ErrUnsupportedFormat", err)
	}
}

// An edit applied whose uses failed still returns its delta, with the error.
func TestGRPCAppliedButUsesFailed(t *testing.T) {
	c, _ := newClient(t, &testUses{fail: true})
	ctx := context.Background()
	out, err := c.UpdateDocument(ctx, "ann", "p", set(t, "bg", engine.AssetRef{ID: "x", Version: 1}))
	if !errors.Is(err, manager.ErrUsesNotRecorded) || errors.Is(err, manager.ErrNotPublished) || len(out.Payload) == 0 {
		t.Errorf("got a %d-byte delta and %v; want the delta with ErrUsesNotRecorded only", len(out.Payload), err)
	}
}

func TestGRPCRevertDocument(t *testing.T) {
	c, m := newClient(t, &testUses{})
	ctx := context.Background()
	if _, err := c.UpdateDocument(ctx, "ann", "p", set(t, "title", "first")); err != nil {
		t.Fatal(err)
	}
	if _, err := c.UpdateDocument(ctx, "ann", "p", set(t, "title", "second")); err != nil {
		t.Fatal(err)
	}
	// Version 1 is the empty first snapshot: reverting to it removes the title.
	out, err := c.RevertDocument(ctx, "ann", "p", 1)
	if err != nil || len(out.Payload) == 0 {
		t.Fatalf("RevertDocument = %d bytes, %v", len(out.Payload), err)
	}
	v, _ := m.QueryDocument(ctx, "ann", "p", engine.Query{Format: engine.QueryNative})
	var view struct{ Registers map[string]any }
	json.Unmarshal(v.Data, &view)
	if len(view.Registers) != 0 {
		t.Errorf("after reverting to version 1: %v, want an empty document", view.Registers)
	}
	if _, err := c.RevertDocument(ctx, "ann", "p", 42); !errors.Is(err, snapshots.ErrNotFound) {
		t.Errorf("unknown version over gRPC: got %v, want snapshots.ErrNotFound", err)
	}
	if _, err := c.RevertDocument(ctx, "rita", "p", 1); !errors.Is(err, manager.ErrForbidden) {
		t.Errorf("reader over gRPC: got %v, want ErrForbidden", err)
	}
}

func TestGRPCCloneAndReplaceAssetRef(t *testing.T) {
	c, m := newClient(t, &testUses{})
	ctx := context.Background()
	x1 := engine.AssetRef{ID: "x", Version: 1}
	if _, err := c.UpdateDocument(ctx, "ann", "p", set(t, "bg", x1)); err != nil {
		t.Fatal(err)
	}
	if err := c.CloneDocument(ctx, "rita", "p", "q"); err != nil {
		t.Fatalf("CloneDocument over gRPC: %v", err)
	}
	copyID := storage.CopyAssetID("q", storage.Owner{Kind: storage.OwnerProject, ID: "q"}, storage.AssetRef{AssetID: "x", Version: 1})
	v, err := m.QueryDocument(ctx, "ann", "q", engine.Query{Format: engine.QueryNative})
	if err != nil || !strings.Contains(string(v.Data), copyID) {
		t.Errorf("clone = %s, %v; want it to use %s", v.Data, err, copyID)
	}
	if err := c.CloneDocument(ctx, "mallory", "p", "r"); !errors.Is(err, manager.ErrForbidden) {
		t.Errorf("stranger's clone over gRPC: got %v, want ErrForbidden", err)
	}

	fork := engine.AssetRef{ID: "fork", Version: 1}
	out, err := c.ReplaceAssetRef(ctx, "ann", "p", x1, fork)
	if err != nil || len(out.Payload) == 0 {
		t.Fatalf("ReplaceAssetRef over gRPC = %d bytes, %v", len(out.Payload), err)
	}
	if v, _ := m.QueryDocument(ctx, "ann", "p", engine.Query{Format: engine.QueryNative}); !strings.Contains(string(v.Data), `"fork"`) {
		t.Errorf("after ReplaceAssetRef: %s", v.Data)
	}
}
