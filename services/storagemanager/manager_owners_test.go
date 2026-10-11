package storagemanager_test

import (
	"errors"
	"testing"

	"gyoza/services/storageledger"
	sm "gyoza/services/storagemanager"
)

func ledgerObjects(t *testing.T, tm testManager, owner storageledger.Owner, includeDeleted bool) []storageledger.Object {
	t.Helper()
	page, err := tm.ledger.List(t.Context(), storageledger.ListQuery{Owner: owner, IncludeDeleted: includeDeleted})
	if err != nil {
		t.Fatalf("ledger List: %v", err)
	}
	return page.Objects
}

func TestDeleteOwnedProject(t *testing.T) {
	tm := newTestManager(t)
	ctx := t.Context()
	tm.authz.AllowProject("member", "p1", sm.AccessWrite)
	own := newAssetRequest()
	own.Owner = project
	projects, _ := uploadAsset(t, tm, "member", own)
	assoc := newAssetRequest()
	assoc.ProjectID = "p1" // acme's, used in p1
	associated, _ := uploadAsset(t, tm, "member", assoc)

	del, err := tm.DeleteOwned(ctx, project)
	if err != nil || del.LedgerObjects != 1 || del.Trash.Assets != 1 {
		t.Fatalf("DeleteOwned = %+v, %v; want 1 ledger object and 1 asset", del, err)
	}
	if g, _ := tm.store.GetAsset(ctx, projects.ID); g.DeletedAt == nil {
		t.Error("the project's asset wasn't trashed")
	}
	if n := len(ledgerObjects(t, tm, project, false)); n != 0 {
		t.Errorf("%d of the project's ledger objects still live", n)
	}
	if g, _ := tm.store.GetAsset(ctx, associated.ID); g.DeletedAt != nil {
		t.Error("acme's asset that the project used was trashed")
	}
	if again, err := tm.DeleteOwned(ctx, project); err != nil || again.LedgerObjects != 0 || again.Trash.Assets != 0 {
		t.Errorf("running it again = %+v, %v; want nothing left to do", again, err)
	}
}

// A user with an org: their library moves to the org, intact.
func TestRemoveUserWithOrg(t *testing.T) {
	tm := newTestManager(t)
	ctx := t.Context()
	mine := newAssetRequest()
	mine.Owner = alice
	personal, data := uploadAsset(t, tm, "alice", mine)
	orgAsset, _ := uploadAsset(t, tm, "member", newAssetRequest()) // acme's, created by member
	tm.authz.AllowOwner("alice", acme, sm.AccessWrite)
	byAlice, _ := uploadAsset(t, tm, "alice", newAssetRequest()) // acme's, created by alice
	pending, err := tm.BeginUpload(ctx, "alice", mine)
	if err != nil {
		t.Fatalf("BeginUpload: %v", err)
	}

	r, err := tm.RemoveUser(ctx, "alice", "acme")
	if err != nil {
		t.Fatalf("RemoveUser: %v", err)
	}
	if r.ExpiredUploads != 1 || r.TransferredTo != acme || r.Transfer.Assets != 1 || r.LedgerObjects != 1 {
		t.Errorf("RemoveUser = %+v", r)
	}

	// Her personal asset is now acme's, in the DAM and the ledger, and still downloads.
	moved, _ := tm.store.GetAsset(ctx, personal.ID)
	if moved.Owner != acme || moved.DeletedAt != nil || moved.CreatedBy != "" {
		t.Errorf("personal asset = %+v; want acme's, live, creator cleared", moved)
	}
	if got, _ := download(t, tm, "member", personal.ID, 0); string(got) != string(data) {
		t.Error("acme's member can't download the transferred asset")
	}
	vs := mustListVersions(t, tm.store, personal.ID)
	if obj, _ := tm.ledger.Get(ctx, vs[0].ObjectID); obj.Owner != acme || obj.CreatedBy != "" {
		t.Errorf("ledger object = %+v; want acme's, creator cleared", obj)
	}
	// What she created for acme stays, without her name on it; others' work is untouched.
	if g, _ := tm.store.GetAsset(ctx, byAlice.ID); g.CreatedBy != "" || g.DeletedAt != nil {
		t.Errorf("asset alice made for acme = %+v; want live, creator cleared", g)
	}
	if g, _ := tm.store.GetAsset(ctx, orgAsset.ID); g.CreatedBy != "member" {
		t.Errorf("member's asset creator = %q, want member", g.CreatedBy)
	}
	// Her half-finished upload can't land anymore.
	if _, err := tm.CompleteUpload(ctx, "alice", pending.UploadID); !errors.Is(err, sm.ErrUploadExpired) {
		t.Errorf("completing her pending upload = %v, want ErrUploadExpired", err)
	}
	if again, err := tm.RemoveUser(ctx, "alice", "acme"); err != nil || again.Transfer.Assets != 0 {
		t.Errorf("running it again = %+v, %v; want nothing left to do", again, err)
	}
}

// A user without an org: their library goes to the trash.
func TestRemoveUserWithoutOrg(t *testing.T) {
	tm := newTestManager(t)
	ctx := t.Context()
	mine := newAssetRequest()
	mine.Owner = alice
	personal, _ := uploadAsset(t, tm, "alice", mine)

	r, err := tm.RemoveUser(ctx, "alice", "")
	if err != nil {
		t.Fatalf("RemoveUser: %v", err)
	}
	if !r.TransferredTo.IsZero() || r.Deleted.Trash.Assets != 1 || r.LedgerObjects != 1 {
		t.Errorf("RemoveUser = %+v; want her asset trashed, nothing transferred", r)
	}
	if g, _ := tm.store.GetAsset(ctx, personal.ID); g.DeletedAt == nil || g.CreatedBy != "" {
		t.Errorf("personal asset = %+v; want trashed, creator cleared", g)
	}
	if n := len(ledgerObjects(t, tm, alice, false)); n != 0 {
		t.Errorf("%d of her ledger objects still live", n)
	}
	if objs := ledgerObjects(t, tm, alice, true); len(objs) != 1 || objs[0].CreatedBy != "" {
		t.Errorf("her ledger objects = %+v; want 1, deleted, creator cleared", objs)
	}
	if _, err := tm.RemoveUser(ctx, "", ""); !errors.Is(err, sm.ErrInvalidQuery) {
		t.Errorf("no user = %v, want ErrInvalidQuery", err)
	}
}
