package directory_test

import (
	"errors"
	"gyoza/services/iam/internal/directory"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
)

func testMarkProjectDeleting(t *testing.T, s directory.Store) {
	ctx := t.Context()
	ann, bob := mustUser(t, s, "ann"), mustUser(t, s, "bob")
	p := mustProject(t, s, directory.Project{Owner: userOwner(ann.ID), Name: "Doomed"})
	mustShare(t, s, p.ID, bob.ID, directory.AccessViewer)
	kept := mustProject(t, s, directory.Project{Owner: userOwner(ann.ID), Name: "Kept"})

	marked, err := s.MarkProjectDeleting(ctx, p.ID)
	if err != nil || marked.DeletingAt == nil {
		t.Fatalf("MarkProjectDeleting = %+v, %v", marked, err)
	}
	letTimePass()
	if again, err := s.MarkProjectDeleting(ctx, p.ID); err != nil || !again.DeletingAt.Equal(*marked.DeletingAt) {
		t.Errorf("marking again = %+v, %v; want the first time kept", again, err)
	}
	if got, err := s.GetProject(ctx, p.ID); err != nil || got.DeletingAt == nil {
		t.Errorf("GetProject while deleting = %+v, %v; want it, marked", got, err)
	}
	// Hidden from listings, and no new references.
	for _, q := range []directory.ProjectQuery{{VisibleTo: ann.ID}, {VisibleTo: bob.ID}, {Owner: userOwner(ann.ID)}} {
		if got := listAll(t, s, q); slices.Contains(got, p.ID) {
			t.Errorf("listing %+v shows the project being deleted", q)
		}
	}
	if got := listAll(t, s, directory.ProjectQuery{VisibleTo: ann.ID}); !slices.Equal(got, []string{kept.ID}) {
		t.Errorf("ann's projects = %v, want only the kept one", got)
	}
	if _, err := s.ShareProject(ctx, directory.Share{ProjectID: p.ID, UserID: bob.ID, Access: directory.AccessEditor, SharedBy: "x"}); !errors.Is(err, directory.ErrProjectNotFound) {
		t.Errorf("sharing a project being deleted = %v, want ErrProjectNotFound", err)
	}
	if _, err := s.CreateProject(ctx, directory.Project{Owner: userOwner(ann.ID), Name: "Clone", State: directory.ProjectCloning, ClonedFrom: p.ID}); !errors.Is(err, directory.ErrProjectNotFound) {
		t.Errorf("cloning a project being deleted = %v, want ErrProjectNotFound", err)
	}
	if _, err := s.MarkProjectDeleting(ctx, uuid.NewString()); !errors.Is(err, directory.ErrProjectNotFound) {
		t.Errorf("marking a missing project = %v, want ErrProjectNotFound", err)
	}
}

func testDeleteProject(t *testing.T, s directory.Store) {
	ctx := t.Context()
	ann, bob := mustUser(t, s, "ann"), mustUser(t, s, "bob")
	p := mustProject(t, s, directory.Project{Owner: userOwner(ann.ID), Name: "Doomed"})
	mustShare(t, s, p.ID, bob.ID, directory.AccessViewer)
	mustSharePending(t, s, p.ID, "dan@example.com", directory.AccessViewer, time.Hour)

	if err := s.DeleteProject(ctx, p.ID); !errors.Is(err, directory.ErrNotDeleting) {
		t.Fatalf("deleting an unmarked project = %v, want ErrNotDeleting", err)
	}
	if _, err := s.MarkProjectDeleting(ctx, p.ID); err != nil {
		t.Fatalf("MarkProjectDeleting: %v", err)
	}
	if err := s.DeleteProject(ctx, p.ID); err != nil {
		t.Fatalf("DeleteProject: %v", err)
	}
	if _, err := s.GetProject(ctx, p.ID); !errors.Is(err, directory.ErrProjectNotFound) {
		t.Errorf("deleted project = %v, want ErrProjectNotFound", err)
	}
	if _, err := s.GetShare(ctx, p.ID, bob.ID); !errors.Is(err, directory.ErrShareNotFound) {
		t.Errorf("its share = %v, want ErrShareNotFound", err)
	}
	// Its pending share is gone too: dan signing in claims nothing.
	dan := mustUser(t, s, "dan")
	if n, err := s.ClaimPendingShares(ctx, dan.ID, dan.Email, time.Now()); err != nil || n != 0 {
		t.Errorf("claim after the project's deletion = %d, %v; want 0", n, err)
	}
	if err := s.DeleteProject(ctx, p.ID); err != nil {
		t.Errorf("deleting again = %v, want nil (safe to retry)", err)
	}
}

func testMarkOrgDeleting(t *testing.T, s directory.Store) {
	ctx := t.Context()
	ann, eve, bob := mustUser(t, s, "ann"), mustUser(t, s, "eve"), mustUser(t, s, "bob")
	acme := mustOrg(t, s, "Acme", ann.ID)
	beta := mustOrg(t, s, "Beta", eve.ID)
	acmeP := mustProject(t, s, directory.Project{Owner: orgOwner(acme.ID), Name: "acme's"})
	betaP := mustProject(t, s, directory.Project{Owner: orgOwner(beta.ID), Name: "beta's"})
	annP := mustProject(t, s, directory.Project{Owner: userOwner(ann.ID), Name: "ann's"})

	o, err := s.MarkOrgDeleting(ctx, acme.ID)
	if err != nil || o.DeletingAt == nil {
		t.Fatalf("MarkOrgDeleting = %+v, %v", o, err)
	}
	// Its projects are marked with it; others aren't.
	if p, _ := s.GetProject(ctx, acmeP.ID); p.DeletingAt == nil {
		t.Error("the org's project wasn't marked")
	}
	for _, other := range []directory.Project{betaP, annP} {
		if p, _ := s.GetProject(ctx, other.ID); p.DeletingAt != nil {
			t.Errorf("%s was marked with someone else's org", other.Name)
		}
	}
	letTimePass()
	if again, err := s.MarkOrgDeleting(ctx, acme.ID); err != nil || !again.DeletingAt.Equal(*o.DeletingAt) {
		t.Errorf("marking again = %+v, %v; want the first time kept", again, err)
	}

	// No new references to the org.
	if _, err := s.CreateProject(ctx, directory.Project{Owner: orgOwner(acme.ID), Name: "X"}); !errors.Is(err, directory.ErrOrgNotFound) {
		t.Errorf("new project for it = %v, want ErrOrgNotFound", err)
	}
	if _, err := s.CreateInvite(ctx, directory.Invite{OrgID: acme.ID, Email: bob.Email, Role: directory.RoleMember, InvitedBy: "x", ExpiresAt: time.Now().Add(time.Hour)}); !errors.Is(err, directory.ErrOrgNotFound) {
		t.Errorf("new invite to it = %v, want ErrOrgNotFound", err)
	}
	if _, err := s.TransferProject(ctx, annP.ID, orgOwner(acme.ID)); !errors.Is(err, directory.ErrOrgNotFound) {
		t.Errorf("moving a project to it = %v, want ErrOrgNotFound", err)
	}
	if _, err := s.TransferUserProjects(ctx, ann.ID, acme.ID); !errors.Is(err, directory.ErrOrgNotFound) {
		t.Errorf("moving a user's projects to it = %v, want ErrOrgNotFound", err)
	}
	if _, err := s.MarkOrgDeleting(ctx, uuid.NewString()); !errors.Is(err, directory.ErrOrgNotFound) {
		t.Errorf("marking a missing org = %v, want ErrOrgNotFound", err)
	}
}

// An invite to an org that's being deleted can't be accepted.
func testAcceptInviteToDeletingOrg(t *testing.T, s directory.Store) {
	ctx := t.Context()
	acme := mustOrg(t, s, "Acme", mustUser(t, s, "ann").ID)
	bob := mustUser(t, s, "bob")
	inv := mustInvite(t, s, acme.ID, bob.Email, directory.RoleMember, time.Hour)
	if _, err := s.MarkOrgDeleting(ctx, acme.ID); err != nil {
		t.Fatalf("MarkOrgDeleting: %v", err)
	}
	if _, err := s.AcceptInvite(ctx, inv.ID, bob.ID); !errors.Is(err, directory.ErrOrgNotFound) {
		t.Errorf("accepting = %v, want ErrOrgNotFound", err)
	}
	if _, err := s.GetMembership(ctx, bob.ID); !errors.Is(err, directory.ErrNotMember) {
		t.Errorf("bob joined an org being deleted: %v", err)
	}
}

func testDeleteOrg(t *testing.T, s directory.Store) {
	ctx := t.Context()
	ann, bob := mustUser(t, s, "ann"), mustUser(t, s, "bob")
	acme := mustOrg(t, s, "Acme", ann.ID)
	mustJoin(t, s, acme.ID, bob, directory.RoleMember)
	mustInvite(t, s, acme.ID, "cat@example.com", directory.RoleMember, time.Hour)
	p := mustProject(t, s, directory.Project{Owner: orgOwner(acme.ID), Name: "acme's"})

	if err := s.DeleteOrg(ctx, acme.ID); !errors.Is(err, directory.ErrNotDeleting) {
		t.Fatalf("deleting an unmarked org = %v, want ErrNotDeleting", err)
	}
	if _, err := s.MarkOrgDeleting(ctx, acme.ID); err != nil {
		t.Fatalf("MarkOrgDeleting: %v", err)
	}
	if err := s.DeleteOrg(ctx, acme.ID); !errors.Is(err, directory.ErrInvalidOrg) {
		t.Errorf("deleting an org that still owns projects = %v, want ErrInvalidOrg", err)
	}
	if err := s.DeleteProject(ctx, p.ID); err != nil {
		t.Fatalf("DeleteProject: %v", err)
	}
	if err := s.DeleteOrg(ctx, acme.ID); err != nil {
		t.Fatalf("DeleteOrg: %v", err)
	}
	if _, err := s.GetOrg(ctx, acme.ID); !errors.Is(err, directory.ErrOrgNotFound) {
		t.Errorf("deleted org = %v, want ErrOrgNotFound", err)
	}
	for _, u := range []directory.User{ann, bob} {
		if _, err := s.GetMembership(ctx, u.ID); !errors.Is(err, directory.ErrNotMember) {
			t.Errorf("%s still a member: %v", u.ID, err)
		}
	}
	if invs, _ := s.ListEmailInvites(ctx, "cat@example.com"); len(invs) != 0 {
		t.Errorf("the org's invites survived: %+v", invs)
	}
	// Members are free again.
	if _, _, err := s.CreateOrg(ctx, directory.Org{Name: "Bob's"}, bob.ID); err != nil {
		t.Errorf("bob creates an org afterwards: %v", err)
	}
	if err := s.DeleteOrg(ctx, acme.ID); err != nil {
		t.Errorf("deleting again = %v, want nil", err)
	}
}

func testMarkUserDeleting(t *testing.T, s directory.Store) {
	ctx := t.Context()
	ann, bob := mustUser(t, s, "ann"), mustUser(t, s, "bob")
	acme := mustOrg(t, s, "Acme", ann.ID)
	annP := mustProject(t, s, directory.Project{Owner: userOwner(ann.ID), Name: "ann's"})

	u, err := s.MarkUserDeleting(ctx, bob.ID, "")
	if err != nil || u.DeletingAt == nil || u.DeletingToOrg != "" {
		t.Fatalf("MarkUserDeleting = %+v, %v", u, err)
	}
	marked, err := s.MarkUserDeleting(ctx, ann.ID, acme.ID)
	if err != nil || marked.DeletingToOrg != acme.ID {
		t.Fatalf("MarkUserDeleting(with org) = %+v, %v", marked, err)
	}
	letTimePass()
	// The first mark decides: a resumed deletion can't change course.
	if again, err := s.MarkUserDeleting(ctx, ann.ID, ""); err != nil || again.DeletingToOrg != acme.ID || !again.DeletingAt.Equal(*marked.DeletingAt) {
		t.Errorf("marking again = %+v, %v; want the first destination and time", again, err)
	}

	// No new references to a user being deleted.
	if _, _, err := s.UpsertUser(ctx, directory.Identity{UserID: bob.ID, Email: bob.Email}); !errors.Is(err, directory.ErrUserNotFound) {
		t.Errorf("signing in while being deleted = %v, want ErrUserNotFound", err)
	}
	if _, _, err := s.CreateOrg(ctx, directory.Org{Name: "Bob's"}, bob.ID); !errors.Is(err, directory.ErrUserNotFound) {
		t.Errorf("creating an org = %v, want ErrUserNotFound", err)
	}
	if _, err := s.CreateProject(ctx, directory.Project{Owner: userOwner(bob.ID), Name: "X"}); !errors.Is(err, directory.ErrUserNotFound) {
		t.Errorf("a new project for them = %v, want ErrUserNotFound", err)
	}
	if _, err := s.ShareProject(ctx, directory.Share{ProjectID: annP.ID, UserID: bob.ID, Access: directory.AccessViewer, SharedBy: "x"}); !errors.Is(err, directory.ErrUserNotFound) {
		t.Errorf("sharing with them = %v, want ErrUserNotFound", err)
	}
	inv := mustInvite(t, s, acme.ID, bob.Email, directory.RoleMember, time.Hour)
	if _, err := s.AcceptInvite(ctx, inv.ID, bob.ID); !errors.Is(err, directory.ErrUserNotFound) {
		t.Errorf("accepting an invite = %v, want ErrUserNotFound", err)
	}

	for name, call := range map[string]func() error{
		"missing user": func() error { _, err := s.MarkUserDeleting(ctx, "ghost", ""); return err },
	} {
		if err := call(); !errors.Is(err, directory.ErrUserNotFound) {
			t.Errorf("%s = %v, want ErrUserNotFound", name, err)
		}
	}
	cat := mustUser(t, s, "cat")
	if _, err := s.MarkUserDeleting(ctx, cat.ID, "not-an-id"); !errors.Is(err, directory.ErrInvalidUser) {
		t.Errorf("bad destination org = %v, want ErrInvalidUser", err)
	}
}

func testTransferUserProjects(t *testing.T, s directory.Store) {
	ctx := t.Context()
	ann, bob := mustUser(t, s, "ann"), mustUser(t, s, "bob")
	acme := mustOrg(t, s, "Acme", ann.ID)
	p1 := mustProject(t, s, directory.Project{Owner: userOwner(ann.ID), Name: "one"})
	p2 := mustProject(t, s, directory.Project{Owner: userOwner(ann.ID), Name: "two"})
	mustShare(t, s, p1.ID, bob.ID, directory.AccessViewer)
	bobs := mustProject(t, s, directory.Project{Owner: userOwner(bob.ID), Name: "bob's"})

	n, err := s.TransferUserProjects(ctx, ann.ID, acme.ID)
	if err != nil || n != 2 {
		t.Fatalf("TransferUserProjects = %d, %v; want 2", n, err)
	}
	for _, p := range []directory.Project{p1, p2} {
		if got, _ := s.GetProject(ctx, p.ID); got.Owner != orgOwner(acme.ID) || got.OrgLocked {
			t.Errorf("%s after the transfer: %+v; want acme's, unlocked", p.Name, got)
		}
	}
	if _, err := s.GetShare(ctx, p1.ID, bob.ID); err != nil {
		t.Errorf("share lost in the transfer: %v", err)
	}
	if got, _ := s.GetProject(ctx, bobs.ID); got.Owner != userOwner(bob.ID) {
		t.Errorf("bob's project moved: %+v", got)
	}
	if n, err := s.TransferUserProjects(ctx, ann.ID, acme.ID); err != nil || n != 0 {
		t.Errorf("again = %d, %v; want 0", n, err)
	}
	if _, err := s.TransferUserProjects(ctx, bob.ID, uuid.NewString()); !errors.Is(err, directory.ErrOrgNotFound) {
		t.Errorf("to a missing org = %v, want ErrOrgNotFound", err)
	}

	owned, err := s.ListOwnedProjects(ctx, orgOwner(acme.ID), 10)
	want := []string{p1.ID, p2.ID}
	if err != nil || !slices.Equal(owned, want) {
		t.Errorf("ListOwnedProjects = %v, %v; want %v", owned, err, want)
	}
	if owned, _ := s.ListOwnedProjects(ctx, orgOwner(acme.ID), 1); len(owned) != 1 {
		t.Errorf("ListOwnedProjects(limit 1) = %v", owned)
	}
	if _, err := s.ListOwnedProjects(ctx, orgOwner(acme.ID), 0); !errors.Is(err, directory.ErrInvalidQuery) {
		t.Errorf("limit 0 = %v, want ErrInvalidQuery", err)
	}
}

func testForgetUser(t *testing.T, s directory.Store) {
	ctx := t.Context()
	ann, bob, cat := mustUser(t, s, "ann"), mustUser(t, s, "bob"), mustUser(t, s, "cat")
	acme := mustOrg(t, s, "Acme", ann.ID)
	annP := mustProject(t, s, directory.Project{Owner: userOwner(ann.ID), Name: "ann's", CreatedBy: ann.ID})
	bobMade := mustProject(t, s, directory.Project{Owner: orgOwner(acme.ID), Name: "bob made it", CreatedBy: bob.ID})
	mustShare(t, s, annP.ID, bob.ID, directory.AccessViewer) // to bob: deleted
	if _, err := s.ShareProject(ctx, directory.Share{ProjectID: annP.ID, UserID: cat.ID, Access: directory.AccessViewer, SharedBy: bob.ID}); err != nil {
		t.Fatalf("ShareProject: %v", err) // by bob: kept, sharer cleared
	}
	toBob := mustInvite(t, s, acme.ID, bob.Email, directory.RoleMember, time.Hour) // to bob: deleted
	byBob, err := s.CreateInvite(ctx, directory.Invite{OrgID: acme.ID, Email: "dan@example.com", Role: directory.RoleMember, InvitedBy: bob.ID, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatalf("CreateInvite: %v", err) // by bob: kept, inviter cleared
	}
	mustSharePending(t, s, annP.ID, bob.Email, directory.AccessViewer, time.Hour) // to bob: deleted
	if _, err := s.SharePending(ctx, directory.Share{ProjectID: annP.ID, Email: "dan@example.com", Access: directory.AccessViewer, SharedBy: bob.ID, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatalf("SharePending: %v", err) // by bob: kept, sharer cleared
	}

	if _, err := s.ForgetUser(ctx, bob.ID); !errors.Is(err, directory.ErrNotDeleting) {
		t.Fatalf("forgetting an unmarked user = %v, want ErrNotDeleting", err)
	}
	if _, err := s.MarkUserDeleting(ctx, bob.ID, ""); err != nil {
		t.Fatalf("MarkUserDeleting: %v", err)
	}
	n, err := s.ForgetUser(ctx, bob.ID)
	if err != nil || n != 7 {
		t.Errorf("ForgetUser = %d, %v; want 7 changes", n, err)
	}
	if pending, _ := s.ListPendingShares(ctx, annP.ID); len(pending) != 1 || pending[0].Email != "dan@example.com" || pending[0].SharedBy != "" {
		t.Errorf("pending shares = %+v; want only dan's, without bob as sharer", pending)
	}
	if _, err := s.GetShare(ctx, annP.ID, bob.ID); !errors.Is(err, directory.ErrShareNotFound) {
		t.Errorf("share to bob = %v, want gone", err)
	}
	if sh, err := s.GetShare(ctx, annP.ID, cat.ID); err != nil || sh.SharedBy != "" {
		t.Errorf("share by bob = %+v, %v; want kept without bob", sh, err)
	}
	if _, err := s.GetInvite(ctx, toBob.ID); !errors.Is(err, directory.ErrInviteNotFound) {
		t.Errorf("invite to bob = %v, want gone", err)
	}
	if inv, err := s.GetInvite(ctx, byBob.ID); err != nil || inv.InvitedBy != "" {
		t.Errorf("invite by bob = %+v, %v; want kept without bob", inv, err)
	}
	if p, _ := s.GetProject(ctx, bobMade.ID); p.CreatedBy != "" {
		t.Errorf("project bob created still names him: %+v", p)
	}
	if p, _ := s.GetProject(ctx, annP.ID); p.CreatedBy != ann.ID {
		t.Errorf("ann's project lost its creator: %+v", p)
	}
	if n, err := s.ForgetUser(ctx, bob.ID); err != nil || n != 0 {
		t.Errorf("again = %d, %v; want 0", n, err)
	}
	if n, err := s.ForgetUser(ctx, "ghost"); err != nil || n != 0 {
		t.Errorf("a user already gone = %d, %v; want 0, nil", n, err)
	}
}

func testDeleteUser(t *testing.T, s directory.Store) {
	ctx := t.Context()
	ann, bob := mustUser(t, s, "ann"), mustUser(t, s, "bob")
	acme := mustOrg(t, s, "Acme", ann.ID)
	mustJoin(t, s, acme.ID, bob, directory.RoleMember)
	p := mustProject(t, s, directory.Project{Owner: userOwner(bob.ID), Name: "bob's"})

	if err := s.DeleteUser(ctx, bob.ID); !errors.Is(err, directory.ErrNotDeleting) {
		t.Fatalf("deleting an unmarked user = %v, want ErrNotDeleting", err)
	}
	if _, err := s.MarkUserDeleting(ctx, bob.ID, acme.ID); err != nil {
		t.Fatalf("MarkUserDeleting: %v", err)
	}
	if err := s.DeleteUser(ctx, bob.ID); !errors.Is(err, directory.ErrInvalidUser) {
		t.Errorf("deleting while still a member and owner = %v, want ErrInvalidUser", err)
	}
	if _, err := s.TransferUserProjects(ctx, bob.ID, acme.ID); err != nil {
		t.Fatalf("TransferUserProjects: %v", err)
	}
	if err := s.DeleteUser(ctx, bob.ID); !errors.Is(err, directory.ErrInvalidUser) {
		t.Errorf("deleting while still a member = %v, want ErrInvalidUser", err)
	}
	if _, err := s.RemoveMember(ctx, acme.ID, bob.ID); err != nil {
		t.Fatalf("RemoveMember: %v", err)
	}
	if err := s.DeleteUser(ctx, bob.ID); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	if _, err := s.GetUser(ctx, bob.ID); !errors.Is(err, directory.ErrUserNotFound) {
		t.Errorf("deleted user = %v, want ErrUserNotFound", err)
	}
	// The email is free for a new account.
	if _, _, err := s.UpsertUser(ctx, directory.Identity{UserID: "bob-again", Email: bob.Email}); err != nil {
		t.Errorf("new account with the freed email: %v", err)
	}
	if got, _ := s.GetProject(ctx, p.ID); got.Owner != orgOwner(acme.ID) {
		t.Errorf("bob's project = %+v, want acme's", got)
	}
	if err := s.DeleteUser(ctx, bob.ID); err != nil {
		t.Errorf("deleting again = %v, want nil", err)
	}

	// No org, but still owning a project: the cascade isn't done either.
	cat := mustUser(t, s, "cat")
	mustProject(t, s, directory.Project{Owner: userOwner(cat.ID), Name: "cat's"})
	if _, err := s.MarkUserDeleting(ctx, cat.ID, ""); err != nil {
		t.Fatalf("MarkUserDeleting: %v", err)
	}
	if err := s.DeleteUser(ctx, cat.ID); !errors.Is(err, directory.ErrInvalidUser) {
		t.Errorf("deleting an org-less user who still owns a project = %v, want ErrInvalidUser", err)
	}
}

func testListDeleting(t *testing.T, s directory.Store) {
	ctx := t.Context()
	var users []string
	for _, name := range []string{"ann", "bob", "cat"} {
		users = append(users, mustUser(t, s, name).ID)
	}
	// Marked in the reverse of name order, so name order can't pass for mark order.
	for _, id := range slices.Backward(users) {
		if _, err := s.MarkUserDeleting(ctx, id, ""); err != nil {
			t.Fatalf("MarkUserDeleting: %v", err)
		}
		letTimePass()
	}
	got, err := s.ListDeletingUsers(ctx, 10)
	var ids []string
	for _, u := range got {
		ids = append(ids, u.ID)
	}
	if want := []string{"cat", "bob", "ann"}; err != nil || !slices.Equal(ids, want) {
		t.Errorf("ListDeletingUsers = %v, %v; want %v (longest-running first)", ids, err, want)
	}
	if got, _ := s.ListDeletingUsers(ctx, 1); len(got) != 1 || got[0].ID != "cat" {
		t.Errorf("limit 1 = %+v", got)
	}

	dan := mustUser(t, s, "dan")
	acme := mustOrg(t, s, "Acme", dan.ID)
	p := mustProject(t, s, directory.Project{Owner: userOwner(dan.ID), Name: "P"})
	if _, err := s.MarkProjectDeleting(ctx, p.ID); err != nil {
		t.Fatalf("MarkProjectDeleting: %v", err)
	}
	if _, err := s.MarkOrgDeleting(ctx, acme.ID); err != nil {
		t.Fatalf("MarkOrgDeleting: %v", err)
	}
	if orgs, err := s.ListDeletingOrgs(ctx, 10); err != nil || len(orgs) != 1 || orgs[0].ID != acme.ID {
		t.Errorf("ListDeletingOrgs = %+v, %v", orgs, err)
	}
	if ps, err := s.ListDeletingProjects(ctx, 10); err != nil || len(ps) != 1 || ps[0].ID != p.ID {
		t.Errorf("ListDeletingProjects = %+v, %v", ps, err)
	}
	for name, call := range map[string]func() error{
		"users":    func() error { _, err := s.ListDeletingUsers(ctx, 0); return err },
		"orgs":     func() error { _, err := s.ListDeletingOrgs(ctx, directory.MaxPageSize+1); return err },
		"projects": func() error { _, err := s.ListDeletingProjects(ctx, -1); return err },
	} {
		if err := call(); !errors.Is(err, directory.ErrInvalidQuery) {
			t.Errorf("%s with a bad limit = %v, want ErrInvalidQuery", name, err)
		}
	}
}

func testDeleteExpiredInvites(t *testing.T, s directory.Store) {
	ctx := t.Context()
	acme := mustOrg(t, s, "Acme", mustUser(t, s, "ann").ID)
	old := mustInvite(t, s, acme.ID, "old@example.com", directory.RoleMember, -2*time.Hour)
	older := mustInvite(t, s, acme.ID, "older@example.com", directory.RoleMember, -3*time.Hour)
	live := mustInvite(t, s, acme.ID, "live@example.com", directory.RoleMember, time.Hour)

	if n, err := s.DeleteExpiredInvites(ctx, time.Now(), 1); err != nil || n != 1 {
		t.Fatalf("DeleteExpiredInvites(limit 1) = %d, %v", n, err)
	}
	if _, err := s.GetInvite(ctx, older.ID); !errors.Is(err, directory.ErrInviteNotFound) {
		t.Errorf("the longest-expired invite should go first: %v", err)
	}
	if n, err := s.DeleteExpiredInvites(ctx, time.Now(), 10); err != nil || n != 1 {
		t.Errorf("DeleteExpiredInvites = %d, %v; want the other expired one", n, err)
	}
	if _, err := s.GetInvite(ctx, old.ID); !errors.Is(err, directory.ErrInviteNotFound) {
		t.Errorf("expired invite kept: %v", err)
	}
	if _, err := s.GetInvite(ctx, live.ID); err != nil {
		t.Errorf("live invite deleted: %v", err)
	}
	if _, err := s.DeleteExpiredInvites(ctx, time.Now(), 0); !errors.Is(err, directory.ErrInvalidQuery) {
		t.Errorf("limit 0 = %v, want ErrInvalidQuery", err)
	}
}
