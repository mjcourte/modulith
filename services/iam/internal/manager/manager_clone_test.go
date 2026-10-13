package manager_test

import (
	"errors"
	"slices"
	"testing"
	"time"

	"gyoza/services/iam/internal/directory"
	"gyoza/services/iam/internal/manager"
	"gyoza/services/storage"
)

// copyOf is the clone's copy of version 1 of a source file: storage's
// contract (storage.CopyAssetID), which documents relies on too.
func copyOf(clone directory.Project, assetID string) string {
	return storage.CopyAssetID(clone.ID, projectOwner(clone), storage.AssetRef{AssetID: assetID, Version: 1})
}

func TestCloneProject(t *testing.T) {
	ctx := t.Context()
	w := newDeletionWorld(t)
	ann, bob := signIn(t, w.m, "ann"), signIn(t, w.m, "bob")
	src := w.project(t, ann.ID, userOwner(ann.ID), "Poster")
	file := mustUpload(t, w.files, ann.ID, projectOwner(src))
	if _, err := w.m.ShareProject(ctx, ann.ID, src.ID, "bob@example.com", directory.AccessViewer); err != nil {
		t.Fatalf("ShareProject: %v", err)
	}

	clone, err := w.m.CloneProject(ctx, bob.ID, src.ID, userOwner(bob.ID))
	if err != nil {
		t.Fatalf("CloneProject: %v", err)
	}
	if clone.State != directory.ProjectReady || clone.Owner != userOwner(bob.ID) || clone.ClonedFrom != src.ID ||
		clone.Name != "Poster" || clone.CreatedBy != bob.ID || clone.OrgLocked || clone.ID == src.ID {
		t.Errorf("clone = %+v", clone)
	}
	// bob edits his clone, files included; ann can't see it; no shares came along.
	if v, err := w.m.GetProject(ctx, bob.ID, clone.ID); err != nil || v.Access != directory.AccessEditor {
		t.Errorf("bob's access to his clone = %+v, %v; want editor", v, err)
	}
	if a, err := w.files.GetAsset(ctx, bob.ID, copyOf(clone, file)); err != nil || a.Owner != projectOwner(clone) {
		t.Errorf("the clone's copy of the file = %+v, %v", a, err)
	}
	if _, err := w.m.GetProject(ctx, ann.ID, clone.ID); !isForbidden(err) {
		t.Errorf("ann sees bob's clone: %v", err)
	}
	if shares, _ := w.store.ListShares(ctx, clone.ID); len(shares) != 0 {
		t.Errorf("the clone has shares %+v", shares)
	}
	// The source is untouched.
	if a, err := w.files.GetAsset(ctx, ann.ID, file); err != nil || a.Owner != projectOwner(src) || a.DeletedAt != nil {
		t.Errorf("source file after cloning = %+v, %v", a, err)
	}

	// Anyone signed in can clone a gyoza template.
	tmpl, err := w.m.CreateTemplate(ctx, "Flyer")
	if err != nil {
		t.Fatalf("CreateTemplate: %v", err)
	}
	if c, err := w.m.CloneProject(ctx, bob.ID, tmpl.ID, userOwner(bob.ID)); err != nil || c.Owner != userOwner(bob.ID) {
		t.Errorf("cloning a template = %+v, %v", c, err)
	}
}

func TestCloneProjectRejects(t *testing.T) {
	ctx := t.Context()
	w := newDeletionWorld(t)
	ann, bob, eve := signIn(t, w.m, "ann"), signIn(t, w.m, "bob"), signIn(t, w.m, "eve")
	acme := managerOrg(t, w.m, ann.ID, "Acme")
	mustJoin(t, w.store, acme.ID, bob, directory.RoleMember)
	beta := managerOrg(t, w.m, eve.ID, "Beta")
	open := w.project(t, ann.ID, orgOwner(acme.ID), "Open")
	locked, err := w.m.CreateProject(ctx, ann.ID, orgOwner(acme.ID), "Locked", true)
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	tests := []struct {
		name, actor, source string
		target              storage.Owner
	}{
		{"source the actor can't read", eve.ID, open.ID, userOwner(eve.ID)},
		{"unknown source", bob.ID, "no-such-project", userOwner(bob.ID)},
		{"into someone else's library", bob.ID, open.ID, userOwner(ann.ID)},
		{"into an org the actor isn't in", bob.ID, open.ID, orgOwner(beta.ID)},
		{"into gyoza", bob.ID, open.ID, gyoza},
		{"locked source out of its org", bob.ID, locked.ID, userOwner(bob.ID)},
	}
	for _, tt := range tests {
		if _, err := w.m.CloneProject(ctx, tt.actor, tt.source, tt.target); !isForbidden(err) {
			t.Errorf("%s: CloneProject = %v, want ErrForbidden", tt.name, err)
		}
	}
	if cloning, _ := w.store.ListCloningProjects(ctx, time.Now().Add(time.Hour), 10); len(cloning) != 0 {
		t.Errorf("refused clones left rows: %+v", cloning)
	}

	// A locked source can be cloned into its own org, and the clone stays locked.
	c, err := w.m.CloneProject(ctx, bob.ID, locked.ID, orgOwner(acme.ID))
	if err != nil || !c.OrgLocked || c.Owner != orgOwner(acme.ID) {
		t.Errorf("cloning a locked project into its org = %+v, %v; want a locked acme project", c, err)
	}
}

// A clone cut off by a storage outage stays hidden; the cleanup loop leaves
// it alone while its call may still be running, then finishes it.
func TestCloneResumedByCleanup(t *testing.T) {
	ctx := t.Context()
	w := newDeletionWorld(t)
	ann := signIn(t, w.m, "ann")
	src := w.project(t, ann.ID, userOwner(ann.ID), "Poster")
	file := mustUpload(t, w.files, ann.ID, projectOwner(src))

	w.flaky.down = true
	clone, err := w.m.CloneProject(ctx, ann.ID, src.ID, userOwner(ann.ID))
	if !errors.Is(err, errOutage) || clone.ID == "" || clone.State != directory.ProjectCloning {
		t.Fatalf("CloneProject during an outage = %+v, %v; want the half-made clone and the outage", clone, err)
	}
	if _, err := w.m.GetProject(ctx, ann.ID, clone.ID); !isForbidden(err) {
		t.Errorf("a clone still copying is reachable: %v", err)
	}
	page, err := w.m.ListMyProjects(ctx, ann.ID, 0, "")
	if err != nil || len(page.Projects) != 1 {
		t.Errorf("ListMyProjects = %d projects, %v; want only the source", len(page.Projects), err)
	}

	w.flaky.down = false
	if r, err := w.m.Cleanup(ctx); err != nil || r.ClonesFinished != 0 || r.ClonesAbandoned != 0 {
		t.Errorf("Cleanup right away = %+v, %v; want the clone left to its call", r, err)
	}
	manager.SetClock(w.m, func() time.Time { return time.Now().Add(10 * time.Minute) })
	if r, err := w.m.Cleanup(ctx); err != nil || r.ClonesFinished != 1 {
		t.Fatalf("Cleanup later = %+v, %v; want the clone finished", r, err)
	}
	if v, err := w.m.GetProject(ctx, ann.ID, clone.ID); err != nil || v.Project.State != directory.ProjectReady {
		t.Errorf("finished clone = %+v, %v", v, err)
	}
	if _, err := w.files.GetAsset(ctx, ann.ID, copyOf(clone, file)); err != nil {
		t.Errorf("finished clone's file: %v", err)
	}
	if r, _ := w.m.Cleanup(ctx); r.ClonesFinished != 0 {
		t.Errorf("a ready clone was finished again: %+v", r)
	}
}

// The document is copied after the files (its refs point at their copies),
// and a documents outage leaves the clone hidden until the cleanup loop
// finishes it.
func TestCloneCopiesDocument(t *testing.T) {
	ctx := t.Context()
	w := newDeletionWorld(t)
	ann := signIn(t, w.m, "ann")
	src := w.project(t, ann.ID, userOwner(ann.ID), "Poster")

	w.docs.down = true
	clone, err := w.m.CloneProject(ctx, ann.ID, src.ID, userOwner(ann.ID))
	if !errors.Is(err, errOutage) || clone.State != directory.ProjectCloning {
		t.Fatalf("CloneProject during a documents outage = %+v, %v; want the half-made clone and the outage", clone, err)
	}
	w.docs.down = false
	manager.SetClock(w.m, func() time.Time { return time.Now().Add(10 * time.Minute) })
	if r, err := w.m.Cleanup(ctx); err != nil || r.ClonesFinished != 1 {
		t.Fatalf("Cleanup = %+v, %v; want the clone finished", r, err)
	}
	if want := []string{src.ID + "->" + clone.ID}; !slices.Equal(w.docs.clones, want) {
		t.Errorf("documents copied %v, want %v", w.docs.clones, want)
	}
	if v, err := w.m.GetProject(ctx, ann.ID, clone.ID); err != nil || v.Project.State != directory.ProjectReady {
		t.Errorf("finished clone = %+v, %v", v, err)
	}
}

// A clone whose source was deleted meanwhile, or that keeps failing for a
// day, is deleted with what was copied.
func TestCloneAbandonedByCleanup(t *testing.T) {
	ctx := t.Context()
	w := newDeletionWorld(t)
	ann := signIn(t, w.m, "ann")
	src := w.project(t, ann.ID, userOwner(ann.ID), "Poster")
	other := w.project(t, ann.ID, userOwner(ann.ID), "Flyer")

	w.flaky.down = true
	orphan, _ := w.m.CloneProject(ctx, ann.ID, src.ID, userOwner(ann.ID))
	stale, _ := w.m.CloneProject(ctx, ann.ID, other.ID, userOwner(ann.ID))
	w.flaky.down = false
	if err := w.m.DeleteProject(ctx, ann.ID, src.ID); err != nil {
		t.Fatalf("DeleteProject: %v", err)
	}

	// Ten minutes on: the orphan goes, the other is retried (and works).
	manager.SetClock(w.m, func() time.Time { return time.Now().Add(10 * time.Minute) })
	w.flaky.down = true // the retry fails...
	r, _ := w.m.Cleanup(ctx)
	if r.ClonesAbandoned != 0 {
		t.Errorf("Cleanup during an outage abandoned %d clones: deleting needs storage too", r.ClonesAbandoned)
	}
	w.flaky.down = false
	// The orphan was marked for deletion when abandoned during the outage, so
	// it finishes as a project deletion now.
	if r, err := w.m.Cleanup(ctx); err != nil || r.Projects != 1 || r.ClonesFinished != 1 {
		t.Errorf("Cleanup = %+v, %v; want the orphan's deletion finished and the other clone finished", r, err)
	}
	if _, err := w.store.GetProject(ctx, orphan.ID); !errors.Is(err, directory.ErrProjectNotFound) {
		t.Errorf("orphaned clone still there: %v", err)
	}

	// A clone failing for over a day is given up on.
	w.flaky.down = true
	late, _ := w.m.CloneProject(ctx, ann.ID, stale.ID, userOwner(ann.ID))
	w.flaky.down = false
	manager.SetClock(w.m, func() time.Time { return time.Now().Add(25 * time.Hour) })
	if r, err := w.m.Cleanup(ctx); err != nil || r.ClonesAbandoned != 1 {
		t.Errorf("Cleanup a day on = %+v, %v; want the clone abandoned", r, err)
	}
	if _, err := w.store.GetProject(ctx, late.ID); !errors.Is(err, directory.ErrProjectNotFound) {
		t.Errorf("day-old clone still there: %v", err)
	}
}
