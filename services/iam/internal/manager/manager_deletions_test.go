package manager_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"gyoza/services/iam/internal/directory"
	"gyoza/services/iam/internal/manager"
	"gyoza/services/storage"
)

// Deletion tests run the cascades against storage's memory fakes, authorized
// by iam's own rules, so they check what happens to the files too.

var errOutage = errors.New("storage unreachable")

// flakyStorage fails every call while down, as an outage would.
type flakyStorage struct {
	manager.Storage
	down bool
}

func (f *flakyStorage) DeleteOwned(ctx context.Context, owner storage.Owner) (storage.OwnerDeletion, error) {
	if f.down {
		return storage.OwnerDeletion{}, errOutage
	}
	return f.Storage.DeleteOwned(ctx, owner)
}

func (f *flakyStorage) CopyProjectFiles(ctx context.Context, actorID, from, to string) (storage.CopyReport, error) {
	if f.down {
		return storage.CopyReport{}, errOutage
	}
	return f.Storage.CopyProjectFiles(ctx, actorID, from, to)
}

func (f *flakyStorage) RemoveUser(ctx context.Context, userID, orgID string) (storage.UserRemoval, error) {
	if f.down {
		return storage.UserRemoval{}, errOutage
	}
	return f.Storage.RemoveUser(ctx, userID, orgID)
}

// recordingIDP remembers which sign-in accounts it was asked to delete: that
// call is the whole contract with the identity provider.
type recordingIDP struct{ deleted []string }

func (r *recordingIDP) DeleteUser(_ context.Context, userID string) error {
	r.deleted = append(r.deleted, userID)
	return nil
}

type deletionWorld struct {
	m     *manager.Manager
	store *directory.MemoryStore
	files *storage.Memory
	flaky *flakyStorage
	idp   *recordingIDP
}

func newDeletionWorld(t *testing.T) deletionWorld {
	t.Helper()
	s := directory.NewMemoryStore()
	w := deletionWorld{store: s, files: newFilesFor(t, s), idp: &recordingIDP{}}
	w.flaky = &flakyStorage{Storage: w.files}
	m, err := manager.NewManager(s, w.flaky, w.idp, manager.Config{})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	w.m = m
	return w
}

// asset returns a file as the inspector sees it, trashed or not.
func (w deletionWorld) assetOwnerAndTrashed(t *testing.T, id string) (storage.Owner, bool) {
	t.Helper()
	a, err := w.files.GetAsset(t.Context(), inspector, id)
	if err != nil {
		t.Fatalf("GetAsset(%s): %v", id, err)
	}
	return a.Owner, a.DeletedAt != nil
}

func (w deletionWorld) project(t *testing.T, actor string, owner storage.Owner, name string) directory.Project {
	t.Helper()
	p, err := w.m.CreateProject(t.Context(), actor, owner, name, false)
	if err != nil {
		t.Fatalf("CreateProject(%s): %v", name, err)
	}
	return p
}

func (w deletionWorld) gone(t *testing.T, projectID string) bool {
	t.Helper()
	_, err := w.store.GetProject(t.Context(), projectID)
	if err != nil && !errors.Is(err, directory.ErrProjectNotFound) {
		t.Fatalf("GetProject: %v", err)
	}
	return err != nil
}

func TestDeleteProject(t *testing.T) {
	ctx := t.Context()
	w := newDeletionWorld(t)
	ann, bob, mem := signIn(t, w.m, "ann"), signIn(t, w.m, "bob"), signIn(t, w.m, "mem")
	acme := managerOrg(t, w.m, ann.ID, "Acme")
	join(t, w.m, ann.ID, acme.ID, mem, directory.RoleMember)

	annP := w.project(t, ann.ID, userOwner(ann.ID), "ann's")
	if _, err := w.m.ShareProject(ctx, ann.ID, annP.ID, bob.Email, directory.AccessEditor); err != nil {
		t.Fatalf("ShareProject: %v", err)
	}
	file := mustUpload(t, w.files, ann.ID, storage.Owner{Kind: storage.OwnerProject, ID: annP.ID})

	if err := w.m.DeleteProject(ctx, bob.ID, annP.ID); !isForbidden(err) {
		t.Errorf("editor share DeleteProject = %v, want ErrForbidden (owners only)", err)
	}
	if err := w.m.DeleteProject(ctx, ann.ID, annP.ID); err != nil {
		t.Fatalf("owner DeleteProject: %v", err)
	}
	if !w.gone(t, annP.ID) {
		t.Error("project still stored")
	}
	if _, err := w.store.GetShare(ctx, annP.ID, bob.ID); !errors.Is(err, directory.ErrShareNotFound) {
		t.Errorf("share after delete = %v, want gone", err)
	}
	if _, trashed := w.assetOwnerAndTrashed(t, file); !trashed {
		t.Error("project's file not trashed")
	}

	acmeP := w.project(t, mem.ID, orgOwner(acme.ID), "acme's")
	if err := w.m.DeleteProject(ctx, mem.ID, acmeP.ID); !isForbidden(err) {
		t.Errorf("org member DeleteProject = %v, want ErrForbidden (admins only)", err)
	}
	if err := w.m.DeleteProject(ctx, ann.ID, acmeP.ID); err != nil || !w.gone(t, acmeP.ID) {
		t.Errorf("org admin DeleteProject = %v, gone %v; want deleted", err, w.gone(t, acmeP.ID))
	}
}

func TestDeleteTemplate(t *testing.T) {
	ctx := t.Context()
	w := newDeletionWorld(t)
	ann := signIn(t, w.m, "ann")
	tmpl, err := w.m.CreateTemplate(ctx, "Poster")
	if err != nil {
		t.Fatalf("CreateTemplate: %v", err)
	}
	if err := w.m.DeleteProject(ctx, ann.ID, tmpl.ID); !isForbidden(err) {
		t.Errorf("user DeleteProject on a template = %v, want ErrForbidden", err)
	}
	annP := w.project(t, ann.ID, userOwner(ann.ID), "ann's")
	if err := w.m.DeleteTemplate(ctx, annP.ID); !errors.Is(err, directory.ErrInvalidProject) || w.gone(t, annP.ID) {
		t.Errorf("DeleteTemplate on a user project = %v; want ErrInvalidProject, project kept", err)
	}
	if err := w.m.DeleteTemplate(ctx, tmpl.ID); err != nil || !w.gone(t, tmpl.ID) {
		t.Errorf("DeleteTemplate = %v; want the template gone", err)
	}
}

func TestDeleteOrg(t *testing.T) {
	ctx := t.Context()
	w := newDeletionWorld(t)
	ann, mem := signIn(t, w.m, "ann"), signIn(t, w.m, "mem")
	acme := managerOrg(t, w.m, ann.ID, "Acme")
	join(t, w.m, ann.ID, acme.ID, mem, directory.RoleMember)
	acmeP := w.project(t, mem.ID, orgOwner(acme.ID), "acme's")
	memP := w.project(t, mem.ID, userOwner(mem.ID), "mem's")
	orgFile := mustUpload(t, w.files, mem.ID, orgOwner(acme.ID))
	projectFile := mustUpload(t, w.files, mem.ID, storage.Owner{Kind: storage.OwnerProject, ID: acmeP.ID})
	memFile := mustUpload(t, w.files, mem.ID, userOwner(mem.ID))
	if _, err := w.m.InviteMember(ctx, ann.ID, acme.ID, "new@example.com", directory.RoleMember); err != nil {
		t.Fatalf("InviteMember: %v", err)
	}

	if err := w.m.DeleteOrg(ctx, mem.ID, acme.ID); !isForbidden(err) {
		t.Errorf("member DeleteOrg = %v, want ErrForbidden", err)
	}
	if err := w.m.DeleteOrg(ctx, ann.ID, acme.ID); err != nil {
		t.Fatalf("admin DeleteOrg: %v", err)
	}
	if _, err := w.store.GetOrg(ctx, acme.ID); !errors.Is(err, directory.ErrOrgNotFound) {
		t.Errorf("org after delete = %v, want gone", err)
	}
	if !w.gone(t, acmeP.ID) || w.gone(t, memP.ID) {
		t.Errorf("org project gone %v, member's own project gone %v; want only the org's gone", w.gone(t, acmeP.ID), w.gone(t, memP.ID))
	}
	for name, id := range map[string]string{"org file": orgFile, "org project file": projectFile} {
		if _, trashed := w.assetOwnerAndTrashed(t, id); !trashed {
			t.Errorf("%s not trashed", name)
		}
	}
	if _, trashed := w.assetOwnerAndTrashed(t, memFile); trashed {
		t.Error("member's own file trashed with the org")
	}
	if me, err := w.m.GetMe(ctx, mem.ID); err != nil || me.Org != nil {
		t.Errorf("member after org delete: org %v, %v; want none", me.Org, err)
	}
	if invites, _ := w.store.ListEmailInvites(ctx, "new@example.com"); len(invites) != 0 {
		t.Errorf("invites after org delete = %v, want none", invites)
	}
}

func TestLastMemberLeavingDeletesOrg(t *testing.T) {
	ctx := t.Context()
	w := newDeletionWorld(t)
	ann := signIn(t, w.m, "ann")
	acme := managerOrg(t, w.m, ann.ID, "Acme")
	acmeP := w.project(t, ann.ID, orgOwner(acme.ID), "acme's")
	file := mustUpload(t, w.files, ann.ID, orgOwner(acme.ID))

	if n, err := w.m.LeaveOrg(ctx, ann.ID); err != nil || n != 0 {
		t.Fatalf("LeaveOrg = %d, %v; want 0 left", n, err)
	}
	if _, err := w.store.GetOrg(ctx, acme.ID); !errors.Is(err, directory.ErrOrgNotFound) {
		t.Errorf("org after its last member left = %v, want gone", err)
	}
	if !w.gone(t, acmeP.ID) {
		t.Error("org project kept after its last member left")
	}
	if _, trashed := w.assetOwnerAndTrashed(t, file); !trashed {
		t.Error("org file not trashed")
	}
}

func TestDeleteAccountWithOrg(t *testing.T) {
	ctx := t.Context()
	w := newDeletionWorld(t)
	ann, bob, carl := signIn(t, w.m, "ann"), signIn(t, w.m, "bob"), signIn(t, w.m, "carl")
	acme := managerOrg(t, w.m, ann.ID, "Acme")
	join(t, w.m, ann.ID, acme.ID, bob, directory.RoleMember)

	bobP := w.project(t, bob.ID, userOwner(bob.ID), "bob's")
	if _, err := w.m.ShareProject(ctx, bob.ID, bobP.ID, carl.Email, directory.AccessViewer); err != nil {
		t.Fatalf("ShareProject: %v", err)
	}
	annP := w.project(t, ann.ID, userOwner(ann.ID), "ann's")
	if _, err := w.m.ShareProject(ctx, ann.ID, annP.ID, bob.Email, directory.AccessEditor); err != nil {
		t.Fatalf("ShareProject: %v", err)
	}
	bobFile := mustUpload(t, w.files, bob.ID, userOwner(bob.ID))

	if err := w.m.DeleteAccount(ctx, bob.ID); err != nil {
		t.Fatalf("DeleteAccount: %v", err)
	}
	if _, err := w.store.GetUser(ctx, bob.ID); !errors.Is(err, directory.ErrUserNotFound) {
		t.Errorf("user after delete = %v, want gone", err)
	}
	if !slices.Equal(w.idp.deleted, []string{bob.ID}) {
		t.Errorf("sign-in accounts deleted = %v, want [bob]", w.idp.deleted)
	}
	moved, err := w.store.GetProject(ctx, bobP.ID)
	if err != nil || moved.Owner != orgOwner(acme.ID) || moved.CreatedBy != "" {
		t.Errorf("bob's project = %+v, %v; want the org's, creator cleared", moved, err)
	}
	if sh, err := w.store.GetShare(ctx, bobP.ID, carl.ID); err != nil || sh.SharedBy != "" {
		t.Errorf("carl's share of the moved project = %+v, %v; want kept, sharer cleared", sh, err)
	}
	if _, err := w.store.GetShare(ctx, annP.ID, bob.ID); !errors.Is(err, directory.ErrShareNotFound) {
		t.Errorf("share to bob = %v, want gone", err)
	}
	if owner, trashed := w.assetOwnerAndTrashed(t, bobFile); trashed || owner != orgOwner(acme.ID) {
		t.Errorf("bob's file: owner %v, trashed %v; want moved to the org, live", owner, trashed)
	}
	if members, _ := w.store.ListMembers(ctx, acme.ID); len(members) != 1 {
		t.Errorf("members after delete = %v, want ann only", members)
	}
	// The email is free again: the same person can sign up afresh.
	if _, err := w.m.SignIn(ctx, directory.Identity{UserID: "bob-2", Email: bob.Email, EmailVerified: true}); err != nil {
		t.Errorf("signing up again with the deleted user's email: %v", err)
	}
}

func TestDeleteAccountWithoutOrg(t *testing.T) {
	ctx := t.Context()
	w := newDeletionWorld(t)
	carl := signIn(t, w.m, "carl")
	carlP := w.project(t, carl.ID, userOwner(carl.ID), "carl's")
	projectFile := mustUpload(t, w.files, carl.ID, storage.Owner{Kind: storage.OwnerProject, ID: carlP.ID})
	libraryFile := mustUpload(t, w.files, carl.ID, userOwner(carl.ID))

	if err := w.m.DeleteAccount(ctx, carl.ID); err != nil {
		t.Fatalf("DeleteAccount: %v", err)
	}
	if !w.gone(t, carlP.ID) {
		t.Error("project of an org-less user kept")
	}
	for name, id := range map[string]string{"project file": projectFile, "library file": libraryFile} {
		if _, trashed := w.assetOwnerAndTrashed(t, id); !trashed {
			t.Errorf("%s not trashed", name)
		}
	}
	if _, err := w.store.GetUser(ctx, carl.ID); !errors.Is(err, directory.ErrUserNotFound) {
		t.Errorf("user after delete = %v, want gone", err)
	}
}

func TestDeleteAccountLastMember(t *testing.T) {
	ctx := t.Context()
	w := newDeletionWorld(t)
	ann := signIn(t, w.m, "ann")
	acme := managerOrg(t, w.m, ann.ID, "Acme")
	annP := w.project(t, ann.ID, userOwner(ann.ID), "ann's")

	if err := w.m.DeleteAccount(ctx, ann.ID); err != nil {
		t.Fatalf("DeleteAccount: %v", err)
	}
	if _, err := w.store.GetOrg(ctx, acme.ID); !errors.Is(err, directory.ErrOrgNotFound) {
		t.Errorf("org after its last member's deletion = %v, want gone", err)
	}
	if !w.gone(t, annP.ID) {
		t.Error("project kept after its owner and their org were deleted")
	}
}

func TestDeleteAccountLastAdmin(t *testing.T) {
	ctx := t.Context()
	w := newDeletionWorld(t)
	ann, bob := signIn(t, w.m, "ann"), signIn(t, w.m, "bob")
	acme := managerOrg(t, w.m, ann.ID, "Acme")
	join(t, w.m, ann.ID, acme.ID, bob, directory.RoleMember)

	if err := w.m.DeleteAccount(ctx, ann.ID); !errors.Is(err, directory.ErrLastAdmin) {
		t.Errorf("only admin DeleteAccount = %v, want ErrLastAdmin", err)
	}
	if u, err := w.store.GetUser(ctx, ann.ID); err != nil || u.DeletingAt != nil {
		t.Errorf("refused deletion left %+v, %v; want ann untouched", u, err)
	}
}

// A deletion cut off by a storage outage stays marked: nothing is reachable
// meanwhile, and the cleanup loop finishes it once storage is back.
func TestCleanupResumesDeletions(t *testing.T) {
	ctx := t.Context()
	w := newDeletionWorld(t)
	ann, bob, carl := signIn(t, w.m, "ann"), signIn(t, w.m, "bob"), signIn(t, w.m, "carl")
	acme := managerOrg(t, w.m, bob.ID, "Acme")
	annP := w.project(t, ann.ID, userOwner(ann.ID), "ann's")
	acmeP := w.project(t, bob.ID, orgOwner(acme.ID), "acme's")
	carlFile := mustUpload(t, w.files, carl.ID, userOwner(carl.ID))

	w.flaky.down = true
	if err := w.m.DeleteProject(ctx, ann.ID, annP.ID); !errors.Is(err, errOutage) {
		t.Fatalf("DeleteProject during outage = %v, want the outage", err)
	}
	if err := w.m.DeleteOrg(ctx, bob.ID, acme.ID); !errors.Is(err, errOutage) {
		t.Fatalf("DeleteOrg during outage = %v, want the outage", err)
	}
	if err := w.m.DeleteAccount(ctx, carl.ID); !errors.Is(err, errOutage) {
		t.Fatalf("DeleteAccount during outage = %v, want the outage", err)
	}
	if _, err := w.m.GetProject(ctx, ann.ID, annP.ID); !isForbidden(err) {
		t.Errorf("half-deleted project = %v, want unreachable", err)
	}
	if _, err := w.m.GetMe(ctx, carl.ID); !isForbidden(err) {
		t.Errorf("half-deleted account = %v, want unusable", err)
	}
	if _, err := w.m.Cleanup(ctx); !errors.Is(err, errOutage) {
		t.Errorf("Cleanup during the outage = %v, want the outage reported", err)
	}

	w.flaky.down = false
	r, err := w.m.Cleanup(ctx)
	if err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if r.Users != 1 || r.Orgs != 1 || r.Projects != 1 {
		t.Errorf("Cleanup = %+v, want one user, org and project finished", r)
	}
	if !w.gone(t, annP.ID) || !w.gone(t, acmeP.ID) {
		t.Error("projects left after cleanup")
	}
	if _, err := w.store.GetOrg(ctx, acme.ID); !errors.Is(err, directory.ErrOrgNotFound) {
		t.Errorf("org after cleanup = %v, want gone", err)
	}
	if _, err := w.store.GetUser(ctx, carl.ID); !errors.Is(err, directory.ErrUserNotFound) {
		t.Errorf("user after cleanup = %v, want gone", err)
	}
	if _, trashed := w.assetOwnerAndTrashed(t, carlFile); !trashed {
		t.Error("carl's file not trashed after cleanup")
	}
	if r, err := w.m.Cleanup(ctx); err != nil || r != (manager.CleanupReport{}) {
		t.Errorf("second Cleanup = %+v, %v; want nothing left", r, err)
	}
}

// If the org's other admins step down while an admin's account deletion is
// cut off, finishing it promotes the longest-standing member rather than
// getting stuck on "an org needs an admin".
func TestCleanupPromotesWhenLastAdminDeleted(t *testing.T) {
	ctx := t.Context()
	w := newDeletionWorld(t)
	ann, bob, carl, dan := signIn(t, w.m, "ann"), signIn(t, w.m, "bob"), signIn(t, w.m, "carl"), signIn(t, w.m, "dan")
	acme := managerOrg(t, w.m, ann.ID, "Acme")
	join(t, w.m, ann.ID, acme.ID, bob, directory.RoleAdmin)
	join(t, w.m, ann.ID, acme.ID, carl, directory.RoleMember)
	join(t, w.m, ann.ID, acme.ID, dan, directory.RoleMember)

	w.flaky.down = true
	if err := w.m.DeleteAccount(ctx, ann.ID); !errors.Is(err, errOutage) {
		t.Fatalf("DeleteAccount during outage = %v, want the outage", err)
	}
	if _, err := w.m.SetRole(ctx, bob.ID, acme.ID, bob.ID, directory.RoleMember); err != nil {
		t.Fatalf("bob steps down: %v", err) // ann, being deleted, still counts as an admin
	}
	w.flaky.down = false
	if _, err := w.m.Cleanup(ctx); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	members, err := w.store.ListMembers(ctx, acme.ID)
	if err != nil || len(members) != 3 || members[0].UserID != bob.ID || members[0].Role != directory.RoleAdmin {
		t.Errorf("members = %+v, %v; want bob (longest-standing) promoted, ann gone", members, err)
	}
}

func TestCleanupExpiredInvites(t *testing.T) {
	ctx := t.Context()
	w := newDeletionWorld(t)
	ann := signIn(t, w.m, "ann")
	acme := managerOrg(t, w.m, ann.ID, "Acme")
	if _, err := w.m.InviteMember(ctx, ann.ID, acme.ID, "new@example.com", directory.RoleMember); err != nil {
		t.Fatalf("InviteMember: %v", err)
	}
	if r, _ := w.m.Cleanup(ctx); r.ExpiredInvites != 0 {
		t.Errorf("Cleanup removed %d live invites", r.ExpiredInvites)
	}
	later := time.Now().Add(manager.DefaultInviteTTL + time.Hour)
	manager.SetClock(w.m, func() time.Time { return later })
	if r, err := w.m.Cleanup(ctx); err != nil || r.ExpiredInvites != 1 {
		t.Errorf("Cleanup after expiry = %+v, %v; want the invite removed", r, err)
	}
	if invites, _ := w.store.ListOrgInvites(ctx, acme.ID); len(invites) != 0 {
		t.Errorf("invites after cleanup = %v, want none", invites)
	}
}

// Marking a row is the first step of every deletion, and access stops right
// there: before any file or row is actually gone (step 6b).
func TestMarkedRowsGrantNothing(t *testing.T) {
	ctx := t.Context()
	newWorld := func(t *testing.T) (deletionWorld, directory.User, directory.User, directory.Org, directory.Project, directory.Project) {
		w := newDeletionWorld(t)
		ann, mem := signIn(t, w.m, "ann"), signIn(t, w.m, "mem")
		acme := managerOrg(t, w.m, ann.ID, "Acme")
		join(t, w.m, ann.ID, acme.ID, mem, directory.RoleMember)
		return w, ann, mem, acme, w.project(t, ann.ID, userOwner(ann.ID), "ann's"), w.project(t, mem.ID, orgOwner(acme.ID), "acme's")
	}
	canRead := func(w deletionWorld, actor string, owner storage.Owner) bool {
		return manager.NewStorageAuthorizer(w.store).CanRead(ctx, actor, owner, "") == nil
	}
	projectFiles := func(p directory.Project) storage.Owner {
		return storage.Owner{Kind: storage.OwnerProject, ID: p.ID}
	}

	t.Run("Project", func(t *testing.T) {
		w, ann, _, _, annP, _ := newWorld(t)
		if _, err := w.store.MarkProjectDeleting(ctx, annP.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := w.m.GetProject(ctx, ann.ID, annP.ID); !isForbidden(err) {
			t.Errorf("owner GetProject = %v, want ErrForbidden", err)
		}
		if canRead(w, ann.ID, projectFiles(annP)) {
			t.Error("owner can still read the project's files")
		}
	})
	t.Run("Org", func(t *testing.T) {
		w, _, mem, acme, _, acmeP := newWorld(t)
		if _, err := w.store.MarkOrgDeleting(ctx, acme.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := w.m.GetProject(ctx, mem.ID, acmeP.ID); !isForbidden(err) {
			t.Errorf("member GetProject = %v, want ErrForbidden", err)
		}
		if canRead(w, mem.ID, orgOwner(acme.ID)) || canRead(w, mem.ID, projectFiles(acmeP)) {
			t.Error("member can still read the org's files")
		}
		if _, err := w.m.CreateProject(ctx, mem.ID, orgOwner(acme.ID), "new", false); !isForbidden(err) {
			t.Errorf("CreateProject in a deleting org = %v, want ErrForbidden", err)
		}
	})
	t.Run("User", func(t *testing.T) {
		w, ann, _, acme, annP, acmeP := newWorld(t)
		if _, err := w.store.MarkUserDeleting(ctx, ann.ID, acme.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := w.m.GetProject(ctx, ann.ID, annP.ID); !isForbidden(err) {
			t.Errorf("GetProject by a user being deleted = %v, want ErrForbidden", err)
		}
		if canRead(w, ann.ID, userOwner(ann.ID)) || canRead(w, ann.ID, projectFiles(acmeP)) {
			t.Error("a user being deleted can still read files")
		}
		if _, err := w.m.SignIn(ctx, directory.Identity{UserID: ann.ID, Email: ann.Email, EmailVerified: true}); err == nil {
			t.Error("a user being deleted signed in again")
		}
		// Still in the org until the cascade removes them, but not acting in it.
		if _, err := w.m.ListMembers(ctx, ann.ID, acme.ID); !isForbidden(err) {
			t.Errorf("ListMembers by a user being deleted = %v, want ErrForbidden", err)
		}
	})
}
