package ledger_test

import (
	"errors"
	"testing"

	ledger "github.com/mjcourte/modulith/services/storage/internal/ledger"
)

// runServiceTests checks the behaviour every Service implementation must
// share. newService must return an empty service for each call.
func runServiceTests(t *testing.T, newService func(t *testing.T) ledger.Service) {
	tests := []struct {
		name string
		fn   func(*testing.T, ledger.Service)
	}{
		{"CreateThenGet", testCreateThenGet},
		{"GetMissing", testGetMissing},
		{"CreateRejectsInvalidObjects", testCreateRejectsInvalidObjects},
		{"ListRejectsInvalidQueries", testListRejectsInvalidQueries},
		{"ListFilters", testListFilters},
		{"ListPagination", testListPagination},
		{"ProjectDeletion", testProjectDeletion},
		{"UserDeletion", testUserDeletion},
		{"TransferOwner", testTransferOwner},
		{"MarkDeletedByOwner", testMarkDeletedByOwner},
		{"CreateWithChosenID", testCreateWithChosenID},
		{"ListDeleted", testListDeleted},
		{"Purge", testPurge},
		{"MediaLifecycle", testMediaLifecycle},
		{"MediaRejects", testMediaRejects},
		{"ListPendingMedia", testListPendingMedia},
		{"PurgeRemovesMediaInfo", testPurgeRemovesMediaInfo},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.fn(t, newService(t))
		})
	}
}

var (
	alice = ledger.Owner{Kind: ledger.OwnerUser, ID: "alice"}
	acme  = ledger.Owner{Kind: ledger.OwnerOrg, ID: "acme"}
)

// mustCreate creates obj or fails the test.
func mustCreate(t *testing.T, svc ledger.Service, obj ledger.Object) ledger.Object {
	t.Helper() // report failures at the caller's line, not here
	created, err := svc.Create(t.Context(), obj)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return created
}

// ids lists the IDs of objs, for readable comparisons.
func ids(objs []ledger.Object) []string {
	out := make([]string, len(objs))
	for i, o := range objs {
		out[i] = o.ID
	}
	return out
}

func testCreateThenGet(t *testing.T, svc ledger.Service) {

	created := mustCreate(t, svc, ledger.Object{
		Owner:       alice,
		CreatedBy:   "alice",
		Kind:        ledger.KindAsset,
		Bucket:      "modulith-assets",
		Key:         "users/alice/avatar.png",
		SizeBytes:   2048,
		ContentType: "image/png",
	})
	if created.ID == "" {
		t.Error("Create did not assign an ID")
	}
	if created.CreatedAt.IsZero() {
		t.Error("Create did not set CreatedAt")
	}

	got, err := svc.Get(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != created {
		t.Errorf("Get returned %+v, want %+v", got, created)
	}
}

func testGetMissing(t *testing.T, svc ledger.Service) {
	_, err := svc.Get(t.Context(), "nope")
	if !errors.Is(err, ledger.ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound", err)
	}
}

func testListRejectsInvalidQueries(t *testing.T, svc ledger.Service) {
	tests := []struct {
		name string
		q    ledger.ListQuery
	}{
		{"no scope", ledger.ListQuery{}},
		{"two scopes", ledger.ListQuery{ProjectID: "p1", Owner: alice}},
		{"filter without scope", ledger.ListQuery{CreatedBy: "alice"}},
		{"negative page size", ledger.ListQuery{ProjectID: "p1", PageSize: -1}},
		{"garbage page token", ledger.ListQuery{ProjectID: "p1", PageToken: "!!"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := svc.List(t.Context(), tt.q)
			if !errors.Is(err, ledger.ErrInvalidQuery) {
				t.Errorf("got %v, want ErrInvalidQuery", err)
			}
		})
	}
}

func testListFilters(t *testing.T, svc ledger.Service) {
	// Alice's personal upload, an org asset Alice uploaded, and a snapshot in a project.
	personal := mustCreate(t, svc, ledger.Object{Owner: alice, CreatedBy: "alice", Kind: ledger.KindAsset})
	orgAsset := mustCreate(t, svc, ledger.Object{Owner: acme, CreatedBy: "alice", Kind: ledger.KindAsset, ProjectID: "p1"})
	snapshot := mustCreate(t, svc, ledger.Object{Owner: acme, Kind: ledger.KindSnapshot, ProjectID: "p1"})
	deleted := mustCreate(t, svc, ledger.Object{Owner: acme, CreatedBy: "alice", Kind: ledger.KindAsset, ProjectID: "p1"})
	shared := mustCreate(t, svc, ledger.Object{Owner: ledger.Owner{Kind: ledger.OwnerModulith}, Kind: ledger.KindAsset})
	if err := svc.MarkDeleted(t.Context(), deleted.ID); err != nil {
		t.Fatalf("MarkDeleted: %v", err)
	}

	tests := []struct {
		name string
		q    ledger.ListQuery
		want []ledger.Object
	}{
		{"by project", ledger.ListQuery{ProjectID: "p1"}, []ledger.Object{orgAsset, snapshot}},
		{"by project and kind", ledger.ListQuery{ProjectID: "p1", Kind: ledger.KindSnapshot}, []ledger.Object{snapshot}},
		{"by owner", ledger.ListQuery{Owner: alice}, []ledger.Object{personal}},
		{"modulith-owned", ledger.ListQuery{Owner: ledger.Owner{Kind: ledger.OwnerModulith}}, []ledger.Object{shared}},
		{"by owner and creator", ledger.ListQuery{Owner: alice, CreatedBy: "alice"}, []ledger.Object{personal}},
		{"org assets alice created", ledger.ListQuery{Owner: acme, CreatedBy: "alice"}, []ledger.Object{orgAsset}},
		{"including deleted", ledger.ListQuery{Owner: acme, CreatedBy: "alice", IncludeDeleted: true}, []ledger.Object{orgAsset, deleted}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			page, err := svc.List(t.Context(), tt.q)
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if !sameIDs(ids(page.Objects), ids(tt.want)) {
				t.Errorf("got %v, want %v", ids(page.Objects), ids(tt.want))
			}
		})
	}
}

func testCreateRejectsInvalidObjects(t *testing.T, svc ledger.Service) {
	tests := []struct {
		name  string
		owner ledger.Owner
		proj  string
	}{
		{"no owner", ledger.Owner{}, ""},
		{"unknown owner kind", ledger.Owner{Kind: "team", ID: "t1"}, ""},
		{"user without ID", ledger.Owner{Kind: ledger.OwnerUser}, ""},
		{"modulith with ID", ledger.Owner{Kind: ledger.OwnerModulith, ID: "x"}, ""},
		{"project owner, other ProjectID", ledger.Owner{Kind: ledger.OwnerProject, ID: "p1"}, "p2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := svc.Create(t.Context(), ledger.Object{Owner: tt.owner, ProjectID: tt.proj})
			if !errors.Is(err, ledger.ErrInvalidObject) {
				t.Errorf("got %v, want ErrInvalidObject", err)
			}
		})
	}
}

// TestProjectDeletion: listing by owner finds what dies with the project;
// listing by ProjectID finds everything associated with it.
func testProjectDeletion(t *testing.T, svc ledger.Service) {
	p1 := ledger.Owner{Kind: ledger.OwnerProject, ID: "p1"}

	snapshot := mustCreate(t, svc, ledger.Object{Owner: p1, Kind: ledger.KindSnapshot})
	upload := mustCreate(t, svc, ledger.Object{Owner: p1, CreatedBy: "alice", Kind: ledger.KindAsset})
	library := mustCreate(t, svc, ledger.Object{Owner: acme, CreatedBy: "alice", Kind: ledger.KindAsset, ProjectID: "p1"})

	if snapshot.ProjectID != "p1" {
		t.Errorf("project-owned object ProjectID = %q, want p1 filled in", snapshot.ProjectID)
	}

	owned, err := svc.List(t.Context(), ledger.ListQuery{Owner: p1})
	if err != nil {
		t.Fatalf("List by owner: %v", err)
	}
	if want := []ledger.Object{snapshot, upload}; !sameIDs(ids(owned.Objects), ids(want)) {
		t.Errorf("owned by p1: got %v, want %v", ids(owned.Objects), ids(want))
	}

	all, err := svc.List(t.Context(), ledger.ListQuery{ProjectID: "p1"})
	if err != nil {
		t.Fatalf("List by project: %v", err)
	}
	if want := []ledger.Object{snapshot, upload, library}; !sameIDs(ids(all.Objects), ids(want)) {
		t.Errorf("associated with p1: got %v, want %v", ids(all.Objects), ids(want))
	}
}

// TestUserDeletion walks the ledger's part of deleting a user:
// everything they own is listed for deletion, and they are removed as
// creator from everything else.
func testUserDeletion(t *testing.T, svc ledger.Service) {
	ctx := t.Context()

	personal := mustCreate(t, svc, ledger.Object{Owner: alice, CreatedBy: "alice", Kind: ledger.KindAsset})
	export := mustCreate(t, svc, ledger.Object{Owner: alice, Kind: ledger.KindExport}) // platform-created
	orgAsset := mustCreate(t, svc, ledger.Object{Owner: acme, CreatedBy: "alice", ProjectID: "p1"})
	oldOrgAsset := mustCreate(t, svc, ledger.Object{Owner: acme, CreatedBy: "alice", ProjectID: "p1"})
	bobsAsset := mustCreate(t, svc, ledger.Object{Owner: acme, CreatedBy: "bob", ProjectID: "p1"})
	if err := svc.MarkDeleted(ctx, oldOrgAsset.ID); err != nil {
		t.Fatalf("MarkDeleted: %v", err)
	}

	// 1. Everything alice owns is deleted, whoever created it.
	page, err := svc.List(ctx, ledger.ListQuery{Owner: alice})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if want := []ledger.Object{personal, export}; !sameIDs(ids(page.Objects), ids(want)) {
		t.Errorf("owned by alice: got %v, want %v", ids(page.Objects), ids(want))
	}

	// 2. Alice is cleared as creator everywhere else, including soft-deleted objects.
	n, err := svc.ClearCreatedBy(ctx, "alice")
	if err != nil {
		t.Fatalf("ClearCreatedBy: %v", err)
	}
	// personal also had CreatedBy alice; it is about to be deleted, so clearing it is harmless.
	if n != 3 {
		t.Errorf("ClearCreatedBy changed %d objects, want 3", n)
	}
	for _, id := range []string{orgAsset.ID, oldOrgAsset.ID} {
		got, err := svc.Get(ctx, id)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.CreatedBy != "" || got.Owner != acme {
			t.Errorf("object %s: CreatedBy=%q Owner=%v, want cleared and still owned by acme", id, got.CreatedBy, got.Owner)
		}
	}
	if got, _ := svc.Get(ctx, bobsAsset.ID); got.CreatedBy != "bob" {
		t.Errorf("bob's asset CreatedBy = %q, want bob", got.CreatedBy)
	}

	if _, err := svc.ClearCreatedBy(ctx, ""); !errors.Is(err, ledger.ErrInvalidQuery) {
		t.Errorf("ClearCreatedBy(\"\"): got %v, want ErrInvalidQuery", err)
	}
}

// sameIDs reports whether a and b hold the same IDs, ignoring order.
// Objects created within the same microsecond are ordered by ID, not by
// creation order, so tests that create in quick succession can't assume order.
func sameIDs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]int)
	for _, id := range a {
		seen[id]++
	}
	for _, id := range b {
		seen[id]--
	}
	for _, n := range seen {
		if n != 0 {
			return false
		}
	}
	return true
}

func testListPagination(t *testing.T, svc ledger.Service) {
	var want []string
	for range 7 {
		want = append(want, mustCreate(t, svc, ledger.Object{Owner: acme, ProjectID: "p1"}).ID)
	}

	// Page through with size 3: expect pages of 3, 3, 1.
	var got []string
	var sizes []int
	q := ledger.ListQuery{ProjectID: "p1", PageSize: 3}
	for {
		page, err := svc.List(t.Context(), q)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		got = append(got, ids(page.Objects)...)
		sizes = append(sizes, len(page.Objects))
		if page.NextPageToken == "" {
			break
		}
		q.PageToken = page.NextPageToken
	}

	if len(sizes) != 3 || sizes[0] != 3 || sizes[1] != 3 || sizes[2] != 1 {
		t.Errorf("page sizes = %v, want [3 3 1]", sizes)
	}
	if !sameIDs(got, want) {
		t.Errorf("paged through %v, want each of %v exactly once", got, want)
	}
}
