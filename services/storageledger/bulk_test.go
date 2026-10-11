package storageledger_test

// Bulk owner operations for the shared suite (listed in runServiceTests).

import (
	"errors"
	"testing"

	ledger "gyoza/services/storageledger"
)

func countOwned(t *testing.T, svc ledger.Service, owner ledger.Owner, includeDeleted bool) int {
	t.Helper()
	page, err := svc.List(t.Context(), ledger.ListQuery{Owner: owner, IncludeDeleted: includeDeleted, PageSize: ledger.MaxPageSize})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	return len(page.Objects)
}

func testTransferOwner(t *testing.T, svc ledger.Service) {
	ctx := t.Context()
	mustCreate(t, svc, ledger.Object{Owner: alice, CreatedBy: "alice"})
	gone := mustCreate(t, svc, ledger.Object{Owner: alice, CreatedBy: "alice"})
	if err := svc.MarkDeleted(ctx, gone.ID); err != nil {
		t.Fatalf("MarkDeleted: %v", err)
	}
	bob := ledger.Owner{Kind: ledger.OwnerUser, ID: "bob"}
	mustCreate(t, svc, ledger.Object{Owner: bob})

	n, err := svc.TransferOwner(ctx, alice, acme)
	if err != nil || n != 2 {
		t.Fatalf("TransferOwner = %d, %v; want 2 (deleted objects move too)", n, err)
	}
	if got := countOwned(t, svc, acme, true); got != 2 {
		t.Errorf("acme owns %d objects, want 2", got)
	}
	if got := countOwned(t, svc, alice, true); got != 0 {
		t.Errorf("alice still owns %d objects", got)
	}
	if got := countOwned(t, svc, bob, true); got != 1 {
		t.Errorf("bob's object moved too")
	}
	// Ownership changes; nothing else does.
	if got, _ := svc.Get(ctx, gone.ID); got.DeletedAt == nil || got.CreatedBy != "alice" || got.Owner != acme {
		t.Errorf("transferred object = %+v; want acme's, still deleted, creator kept", got)
	}

	project := ledger.Owner{Kind: ledger.OwnerProject, ID: "p1"}
	invalid := map[string][2]ledger.Owner{
		"to a project":    {alice, project},
		"from a project":  {project, acme},
		"to the same":     {acme, acme},
		"malformed owner": {{Kind: ledger.OwnerUser}, acme},
	}
	for name, pair := range invalid {
		if _, err := svc.TransferOwner(ctx, pair[0], pair[1]); !errors.Is(err, ledger.ErrInvalidQuery) {
			t.Errorf("%s = %v, want ErrInvalidQuery", name, err)
		}
	}
}

func testMarkDeletedByOwner(t *testing.T, svc ledger.Service) {
	ctx := t.Context()
	project := ledger.Owner{Kind: ledger.OwnerProject, ID: "p1"}
	mustCreate(t, svc, ledger.Object{Owner: project})
	mustCreate(t, svc, ledger.Object{Owner: project})
	earlier := mustCreate(t, svc, ledger.Object{Owner: project})
	if err := svc.MarkDeleted(ctx, earlier.ID); err != nil {
		t.Fatalf("MarkDeleted: %v", err)
	}
	before, _ := svc.Get(ctx, earlier.ID)
	other := mustCreate(t, svc, ledger.Object{Owner: acme, ProjectID: "p1"}) // associated, not owned

	n, err := svc.MarkDeletedByOwner(ctx, project)
	if err != nil || n != 2 {
		t.Fatalf("MarkDeletedByOwner = %d, %v; want 2 newly deleted", n, err)
	}
	if got := countOwned(t, svc, project, false); got != 0 {
		t.Errorf("%d of the project's objects still live", got)
	}
	if got, _ := svc.Get(ctx, earlier.ID); !got.DeletedAt.Equal(*before.DeletedAt) {
		t.Errorf("an already-deleted object's time changed from %v to %v", *before.DeletedAt, *got.DeletedAt)
	}
	if got, _ := svc.Get(ctx, other.ID); got.DeletedAt != nil {
		t.Error("an object merely associated with the project was deleted")
	}
	if again, err := svc.MarkDeletedByOwner(ctx, project); err != nil || again != 0 {
		t.Errorf("second MarkDeletedByOwner = %d, %v; want 0", again, err)
	}
	if _, err := svc.MarkDeletedByOwner(ctx, ledger.Owner{}); !errors.Is(err, ledger.ErrInvalidQuery) {
		t.Errorf("no owner = %v, want ErrInvalidQuery", err)
	}
}
