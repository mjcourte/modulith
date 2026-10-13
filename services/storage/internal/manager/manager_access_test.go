package manager_test

// Routes around an asset's owner: ways an actor could reach, or put files
// into, a scope they have no right to. Each is closed by a check this file
// pins down.

import (
	"errors"
	"testing"

	"gyoza/services/storage/internal/dam"
	"gyoza/services/storage/internal/ledger"
	sm "gyoza/services/storage/internal/manager"
)

// Owning the files isn't enough to associate them with a project: that puts
// them in the project's listings, so it needs write on the project too.
func TestUploadAssociationNeedsProjectWrite(t *testing.T) {
	tm := newTestManager(t)
	ctx := t.Context()
	mine := newAssetRequest()
	mine.Owner = alice
	mine.ProjectID = "p1"

	if _, err := tm.BeginUpload(ctx, "alice", mine); !errors.Is(err, sm.ErrForbidden) {
		t.Errorf("own files into a project alice can't see = %v, want ErrForbidden", err)
	}
	tm.authz.AllowProject("alice", "p1", sm.AccessRead)
	if _, err := tm.BeginUpload(ctx, "alice", mine); !errors.Is(err, sm.ErrForbidden) {
		t.Errorf("own files into a project alice only reads = %v, want ErrForbidden", err)
	}
	if n := len(pendingUploads(t, tm.store)); n != 0 {
		t.Errorf("refused uploads left %d sessions", n)
	}

	// With write on the project it works; losing it mid-upload stops it.
	tm.authz.AllowProject("alice", "p1", sm.AccessWrite)
	ticket := beginAndPut(t, tm, "alice", mine)
	tm.authz.AllowProject("alice", "p1", sm.AccessRead)
	if _, err := tm.CompleteUpload(ctx, "alice", ticket.UploadID); !errors.Is(err, sm.ErrForbidden) {
		t.Errorf("CompleteUpload after losing write on the project = %v, want ErrForbidden", err)
	}
	tm.authz.AllowProject("alice", "p1", sm.AccessWrite)
	a, err := tm.CompleteUpload(ctx, "alice", ticket.UploadID)
	if err != nil {
		t.Fatalf("CompleteUpload: %v", err)
	}

	// A new version keeps the association made at creation: alice may update
	// her own asset even after leaving the project.
	tm.authz.AllowProject("alice", "p1", sm.AccessNone)
	v := sm.UploadRequest{AssetID: a.ID, ContentType: "image/png", SizeBytes: 2048}
	if _, err := tm.CompleteUpload(ctx, "alice", beginAndPut(t, tm, "alice", v).UploadID); err != nil {
		t.Errorf("new version of her own associated asset: %v", err)
	}
}

// RecordAssetUse needs write on the project and read on the asset, and a
// project uses in place only what it, or its owner, owns: anything else
// must be forked.
func TestRecordAssetUseAccess(t *testing.T) {
	tm := newTestManager(t)
	ctx := t.Context()
	// p1 is acme's; member edits it, guest only reads it. p3 is guest's own,
	// p4 member's own.
	tm.authz.SetProjectOwner("p1", acme)
	tm.authz.SetProjectOwner("p3", ledger.Owner{Kind: ledger.OwnerUser, ID: "guest"})
	tm.authz.SetProjectOwner("p4", ledger.Owner{Kind: ledger.OwnerUser, ID: "member"})
	tm.authz.AllowProject("member", "p1", sm.AccessWrite)
	tm.authz.AllowProject("member", "p4", sm.AccessWrite)
	tm.authz.AllowProject("guest", "p1", sm.AccessRead)
	tm.authz.AllowProject("guest", "p3", sm.AccessWrite)

	assoc := newAssetRequest() // acme's, attached to p1: guest reads it through p1
	assoc.ProjectID = "p1"
	acmeShared, _ := uploadAsset(t, tm, "member", assoc)
	acmePrivate, _ := uploadAsset(t, tm, "member", newAssetRequest())
	own := newAssetRequest() // owned by p1 itself
	own.Owner = project
	p1Own, _ := uploadAsset(t, tm, "member", own)
	lib := newAssetRequest() // guest's library
	lib.Owner = ledger.Owner{Kind: ledger.OwnerUser, ID: "guest"}
	guestLib, _ := uploadAsset(t, tm, "guest", lib)
	gyoza := ledger.Owner{Kind: ledger.OwnerGyoza} // shared stock: everyone reads it
	stock := mustCreateAsset(t, tm.store, dam.Asset{Owner: gyoza}, "image/png")
	tm.authz.AllowOwner("guest", gyoza, sm.AccessRead)
	ref := func(a dam.Asset) dam.AssetRef { return dam.AssetRef{AssetID: a.ID, Version: 1} }

	tests := []struct {
		name, actor, project string
		ref                  dam.AssetRef
		want                 error // nil: recorded
	}{
		// Allowed: the project's own files, and its owner's.
		{"project's own asset", "member", "p1", ref(p1Own), nil},
		{"project owner's library (org)", "member", "p1", ref(acmeShared), nil},
		{"project owner's library (user)", "guest", "p3", ref(guestLib), nil},
		{"gyoza's shared asset, anywhere", "guest", "p3", ref(stock), nil},
		// Readable, but another owner's: fork it.
		{"another project's asset", "guest", "p3", ref(p1Own), sm.ErrForkRequired},
		{"an org's asset in a personal project", "guest", "p3", ref(acmeShared), sm.ErrForkRequired},
		{"own org's library in a personal project", "member", "p4", ref(acmePrivate), sm.ErrForkRequired},
		// Refused outright.
		{"asset the actor can't read", "guest", "p3", ref(acmePrivate), sm.ErrForbidden},
		{"project the actor only reads", "guest", "p1", ref(acmeShared), sm.ErrForbidden},
		{"project the actor can't see", "guest", "p9", ref(guestLib), sm.ErrForbidden},
		{"no actor", "", "p3", ref(guestLib), sm.ErrForbidden},
		{"no project", "guest", "", ref(guestLib), dam.ErrInvalidQuery},
		{"missing asset", "guest", "p3", dam.AssetRef{AssetID: "0198f000-0000-7000-8000-000000000000", Version: 1}, dam.ErrAssetNotFound},
	}
	for _, tt := range tests {
		err := tm.RecordAssetUse(ctx, tt.actor, tt.project, tt.ref)
		if tt.want == nil {
			if err != nil {
				t.Errorf("%s: RecordAssetUse = %v, want recorded", tt.name, err)
			}
			continue
		}
		if !errors.Is(err, tt.want) {
			t.Errorf("%s: RecordAssetUse = %v, want %v", tt.name, err, tt.want)
		}
		if errors.Is(tt.want, sm.ErrForkRequired) && !errors.Is(err, sm.ErrForbidden) {
			t.Errorf("%s: ErrForkRequired must come with ErrForbidden, got %v", tt.name, err)
		}
	}
	if got := mustUses(t, tm, "p3"); len(got) != 2 {
		t.Errorf("p3 uses %v, want %s and %s", got, ref(guestLib), ref(stock))
	}
	if got := mustUses(t, tm, "p4"); len(got) != 0 {
		t.Errorf("p4 uses %v, want none", got)
	}

	// Forgetting needs write on the project, not read on the asset.
	if err := tm.ForgetAssetUse(ctx, "guest", "p1", ref(acmeShared)); !errors.Is(err, sm.ErrForbidden) {
		t.Errorf("ForgetAssetUse in a project the actor only reads = %v, want ErrForbidden", err)
	}
	tm.authz.AllowOwner("member", acme, sm.AccessNone)
	if err := tm.ForgetAssetUse(ctx, "member", "p1", ref(acmeShared)); err != nil {
		t.Errorf("ForgetAssetUse after losing read on the asset: %v", err)
	}
	if got := mustUses(t, tm, "p1"); len(got) != 1 || got[0] != ref(p1Own) {
		t.Errorf("p1 uses %v, want only %s", got, ref(p1Own))
	}
}

func mustUses(t *testing.T, tm testManager, projectID string) []dam.AssetRef {
	t.Helper()
	refs, err := tm.store.ListUses(t.Context(), projectID)
	if err != nil {
		t.Fatalf("ListUses: %v", err)
	}
	return refs
}
