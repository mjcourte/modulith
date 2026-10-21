package ledger_test

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	ledger "github.com/mjcourte/modulith/services/storage/internal/ledger"
)

// photo is a file description reused by the chosen-ID tests.
func photo(id string) ledger.Object {
	return ledger.Object{
		ID: id, Owner: alice, CreatedBy: "alice", Kind: ledger.KindAsset,
		Bucket: "modulith-assets", Key: "library/asset/" + id, SizeBytes: 10, ContentType: "image/png",
	}
}

func testCreateWithChosenID(t *testing.T, svc ledger.Service) {
	first := mustCreate(t, svc, photo("obj-1"))
	if first.ID != "obj-1" {
		t.Fatalf("ID = %q, want the chosen obj-1", first.ID)
	}

	// A retry with the same file returns the existing object, even after its
	// owner changed (e.g. the user left and their files went to the org).
	if _, err := svc.TransferOwner(t.Context(), alice, acme); err != nil {
		t.Fatalf("TransferOwner: %v", err)
	}
	again := mustCreate(t, svc, photo("obj-1"))
	if again.Owner != acme || !again.CreatedAt.Equal(first.CreatedAt) {
		t.Errorf("retry returned %+v, want the existing object (now owned by acme)", again)
	}
	page, err := svc.List(t.Context(), ledger.ListQuery{Owner: acme})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(page.Objects) != 1 {
		t.Errorf("%d objects after the retry, want 1", len(page.Objects))
	}

	// The same ID for a different file is refused.
	other := photo("obj-1")
	other.SizeBytes = 11
	if _, err := svc.Create(t.Context(), other); !errors.Is(err, ledger.ErrAlreadyExists) {
		t.Errorf("different file under a used ID: got %v, want ErrAlreadyExists", err)
	}

	for _, id := range []string{"has space", "tab\t", "ünïcode", strings.Repeat("x", ledger.MaxIDLen+1)} {
		if _, err := svc.Create(t.Context(), photo(id)); !errors.Is(err, ledger.ErrInvalidObject) {
			t.Errorf("ID %q: got %v, want ErrInvalidObject", id, err)
		}
	}
}

func testListDeleted(t *testing.T, svc ledger.Service) {
	var deleted []string
	for i := range 4 {
		obj := mustCreate(t, svc, ledger.Object{Owner: alice, Kind: ledger.KindAsset})
		if i == 3 {
			continue // stays live
		}
		if err := svc.MarkDeleted(t.Context(), obj.ID); err != nil {
			t.Fatalf("MarkDeleted: %v", err)
		}
		deleted = append(deleted, obj.ID)
		time.Sleep(time.Millisecond) // distinct deletion times where the clock allows
	}

	// Nothing was deleted before this test started... (an hour of slack for clock skew)
	if objs, err := svc.ListDeleted(t.Context(), time.Now().Add(-time.Hour), 10); err != nil || len(objs) != 0 {
		t.Errorf("ListDeleted(an hour ago) = %d objects, %v; want none", len(objs), err)
	}
	// ...and everything deleted so far was deleted before a minute from now.
	all, err := svc.ListDeleted(t.Context(), time.Now().Add(time.Minute), 10)
	if err != nil {
		t.Fatalf("ListDeleted: %v", err)
	}
	if !sameIDs(ids(all), deleted) {
		t.Errorf("ListDeleted = %v, want exactly the deleted %v", ids(all), deleted)
	}
	// Longest-deleted first, ties broken by ID.
	if !slices.IsSortedFunc(all, func(a, b ledger.Object) int {
		if c := a.DeletedAt.Compare(*b.DeletedAt); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	}) {
		t.Errorf("ListDeleted not in deletion order: %+v", all)
	}

	two, err := svc.ListDeleted(t.Context(), time.Now().Add(time.Minute), 2)
	if err != nil {
		t.Fatalf("ListDeleted(limit 2): %v", err)
	}
	if !slices.Equal(ids(two), ids(all)[:2]) {
		t.Errorf("ListDeleted(limit 2) = %v, want the first two of %v", ids(two), ids(all))
	}

	for _, limit := range []int{0, -1, ledger.MaxPageSize + 1} {
		if _, err := svc.ListDeleted(t.Context(), time.Now(), limit); !errors.Is(err, ledger.ErrInvalidQuery) {
			t.Errorf("limit %d: got %v, want ErrInvalidQuery", limit, err)
		}
	}
}

func testPurge(t *testing.T, svc ledger.Service) {
	obj := mustCreate(t, svc, ledger.Object{Owner: alice, Kind: ledger.KindAsset})

	if err := svc.Purge(t.Context(), obj.ID); !errors.Is(err, ledger.ErrNotDeleted) {
		t.Fatalf("Purge(live) = %v, want ErrNotDeleted", err)
	}
	if _, err := svc.Get(t.Context(), obj.ID); err != nil {
		t.Fatalf("live object gone after a refused Purge: %v", err)
	}

	if err := svc.MarkDeleted(t.Context(), obj.ID); err != nil {
		t.Fatalf("MarkDeleted: %v", err)
	}
	if err := svc.Purge(t.Context(), obj.ID); err != nil {
		t.Fatalf("Purge: %v", err)
	}
	if _, err := svc.Get(t.Context(), obj.ID); !errors.Is(err, ledger.ErrNotFound) {
		t.Errorf("Get after Purge = %v, want ErrNotFound", err)
	}
	if objs, _ := svc.ListDeleted(t.Context(), time.Now().Add(time.Minute), 10); len(objs) != 0 {
		t.Errorf("purged object still listed as deleted: %v", ids(objs))
	}
	// Retrying, or purging something that never existed, is fine.
	for _, id := range []string{obj.ID, "never-existed"} {
		if err := svc.Purge(t.Context(), id); err != nil {
			t.Errorf("Purge(%s) again = %v, want nil", id, err)
		}
	}
}
