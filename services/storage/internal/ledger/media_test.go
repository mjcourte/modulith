package ledger_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	ledger "github.com/mjcourte/modulith/services/storage/internal/ledger"
)

// letTimePass makes sure the next write gets a later timestamp: stores keep
// microseconds, so two quick writes can share one.
func letTimePass() { time.Sleep(2 * time.Millisecond) }

// sameJSON reports whether a and b decode to the same value: Postgres
// normalises jsonb, so Details comes back equivalent, not identical.
func sameJSON(t *testing.T, a, b []byte) bool {
	t.Helper()
	var va, vb any
	if err := json.Unmarshal(a, &va); err != nil {
		t.Fatalf("decode %s: %v", a, err)
	}
	if err := json.Unmarshal(b, &vb); err != nil {
		t.Fatalf("decode %s: %v", b, err)
	}
	return reflect.DeepEqual(va, vb)
}

func testMediaLifecycle(t *testing.T, svc ledger.Service) {
	ctx := t.Context()
	obj := mustCreate(t, svc, ledger.Object{Owner: alice, Kind: ledger.KindAsset, ContentType: "video/mp4"})

	if _, err := svc.GetMediaInfo(ctx, obj.ID); !errors.Is(err, ledger.ErrNotFound) {
		t.Fatalf("GetMediaInfo before any = %v, want ErrNotFound", err)
	}

	if err := svc.MarkMediaPending(ctx, obj.ID); err != nil {
		t.Fatalf("MarkMediaPending: %v", err)
	}
	pending, err := svc.GetMediaInfo(ctx, obj.ID)
	if err != nil {
		t.Fatalf("GetMediaInfo: %v", err)
	}
	if pending.Status != ledger.MediaPending || pending.ObjectID != obj.ID || pending.UpdatedAt.IsZero() {
		t.Errorf("after MarkMediaPending = %+v, want pending with UpdatedAt", pending)
	}

	letTimePass()
	ready := ledger.MediaInfo{
		ObjectID: obj.ID,
		Status:   ledger.MediaReady,
		Width:    1080,
		Height:   1920,
		Duration: 2837292 * time.Microsecond,
		Details:  json.RawMessage(`{"format":"mov,mp4","video":[{"codec":"hevc","rotation":-90}]}`),
		Attempts: 1,
	}
	if err := svc.SetMediaInfo(ctx, ready); err != nil {
		t.Fatalf("SetMediaInfo: %v", err)
	}
	got, err := svc.GetMediaInfo(ctx, obj.ID)
	if err != nil {
		t.Fatalf("GetMediaInfo: %v", err)
	}
	if !got.UpdatedAt.After(pending.UpdatedAt) {
		t.Errorf("UpdatedAt %v not after the pending row's %v", got.UpdatedAt, pending.UpdatedAt)
	}
	if !sameJSON(t, got.Details, ready.Details) {
		t.Errorf("Details = %s, want %s", got.Details, ready.Details)
	}
	got.Details, got.UpdatedAt, ready.Details = nil, time.Time{}, nil
	if !reflect.DeepEqual(got, ready) {
		t.Errorf("GetMediaInfo = %+v, want %+v", got, ready)
	}

	// Queuing again (a retried CompleteUpload) must not undo the result.
	if err := svc.MarkMediaPending(ctx, obj.ID); err != nil {
		t.Fatalf("MarkMediaPending again: %v", err)
	}
	if got, _ := svc.GetMediaInfo(ctx, obj.ID); got.Status != ledger.MediaReady {
		t.Errorf("status after a repeated MarkMediaPending = %q, want ready", got.Status)
	}
}

func testMediaRejects(t *testing.T, svc ledger.Service) {
	ctx := t.Context()
	if err := svc.MarkMediaPending(ctx, "never-existed"); !errors.Is(err, ledger.ErrNotFound) {
		t.Errorf("MarkMediaPending(missing) = %v, want ErrNotFound", err)
	}
	if err := svc.SetMediaInfo(ctx, ledger.MediaInfo{ObjectID: "never-existed", Status: ledger.MediaReady}); !errors.Is(err, ledger.ErrNotFound) {
		t.Errorf("SetMediaInfo(missing) = %v, want ErrNotFound", err)
	}

	obj := mustCreate(t, svc, ledger.Object{Owner: alice, Kind: ledger.KindAsset})
	for name, info := range map[string]ledger.MediaInfo{
		"unknown status": {ObjectID: obj.ID, Status: "done"},
		"invalid JSON":   {ObjectID: obj.ID, Status: ledger.MediaReady, Details: json.RawMessage(`{"width":`)},
	} {
		if err := svc.SetMediaInfo(ctx, info); !errors.Is(err, ledger.ErrInvalidObject) {
			t.Errorf("SetMediaInfo(%s) = %v, want ErrInvalidObject", name, err)
		}
	}
	if _, err := svc.GetMediaInfo(ctx, obj.ID); !errors.Is(err, ledger.ErrNotFound) {
		t.Errorf("rejected SetMediaInfo left a row: GetMediaInfo = %v", err)
	}
}

func testListPendingMedia(t *testing.T, svc ledger.Service) {
	ctx := t.Context()
	var pending []string
	for range 3 {
		obj := mustCreate(t, svc, ledger.Object{Owner: alice, Kind: ledger.KindAsset})
		if err := svc.MarkMediaPending(ctx, obj.ID); err != nil {
			t.Fatalf("MarkMediaPending: %v", err)
		}
		pending = append(pending, obj.ID)
		letTimePass()
	}
	// Neither of these is listed: one is done, one belongs to a deleted object.
	done := mustCreate(t, svc, ledger.Object{Owner: alice, Kind: ledger.KindAsset})
	if err := svc.SetMediaInfo(ctx, ledger.MediaInfo{ObjectID: done.ID, Status: ledger.MediaReady}); err != nil {
		t.Fatalf("SetMediaInfo: %v", err)
	}
	trashed := mustCreate(t, svc, ledger.Object{Owner: alice, Kind: ledger.KindAsset})
	if err := svc.MarkMediaPending(ctx, trashed.ID); err != nil {
		t.Fatalf("MarkMediaPending: %v", err)
	}
	if err := svc.MarkDeleted(ctx, trashed.ID); err != nil {
		t.Fatalf("MarkDeleted: %v", err)
	}

	list := func(limit int) []string {
		t.Helper()
		infos, err := svc.ListPendingMedia(ctx, limit)
		if err != nil {
			t.Fatalf("ListPendingMedia(%d): %v", limit, err)
		}
		var ids []string
		for _, info := range infos {
			ids = append(ids, info.ObjectID)
		}
		return ids
	}
	if got := list(10); !reflect.DeepEqual(got, pending) {
		t.Errorf("ListPendingMedia = %v, want %v (oldest first)", got, pending)
	}
	if got := list(2); !reflect.DeepEqual(got, pending[:2]) {
		t.Errorf("ListPendingMedia(2) = %v, want %v", got, pending[:2])
	}

	// A failed attempt that stays pending goes to the back of the queue.
	if err := svc.SetMediaInfo(ctx, ledger.MediaInfo{ObjectID: pending[0], Status: ledger.MediaPending, Attempts: 1, Error: "timeout"}); err != nil {
		t.Fatalf("SetMediaInfo: %v", err)
	}
	if got, want := list(10), append(pending[1:], pending[0]); !reflect.DeepEqual(got, want) {
		t.Errorf("after a retry ListPendingMedia = %v, want %v", got, want)
	}

	for _, limit := range []int{0, ledger.MaxPageSize + 1} {
		if _, err := svc.ListPendingMedia(ctx, limit); !errors.Is(err, ledger.ErrInvalidQuery) {
			t.Errorf("ListPendingMedia(%d) = %v, want ErrInvalidQuery", limit, err)
		}
	}
}

func testPurgeRemovesMediaInfo(t *testing.T, svc ledger.Service) {
	ctx := t.Context()
	obj := mustCreate(t, svc, ledger.Object{Owner: alice, Kind: ledger.KindAsset})
	if err := svc.MarkMediaPending(ctx, obj.ID); err != nil {
		t.Fatalf("MarkMediaPending: %v", err)
	}
	if err := svc.MarkDeleted(ctx, obj.ID); err != nil {
		t.Fatalf("MarkDeleted: %v", err)
	}
	if err := svc.Purge(ctx, obj.ID); err != nil {
		t.Fatalf("Purge: %v", err)
	}
	if _, err := svc.GetMediaInfo(ctx, obj.ID); !errors.Is(err, ledger.ErrNotFound) {
		t.Errorf("GetMediaInfo after Purge = %v, want ErrNotFound", err)
	}
}
