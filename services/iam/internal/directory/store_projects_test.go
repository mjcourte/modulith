package directory_test

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"gyoza/services/iam/internal/directory"
	"gyoza/services/storage"
)

func userOwner(id string) storage.Owner {
	return storage.Owner{Kind: storage.OwnerUser, ID: id}
}

func orgOwner(id string) storage.Owner {
	return storage.Owner{Kind: storage.OwnerOrg, ID: id}
}

var gyoza = storage.Owner{Kind: storage.OwnerGyoza}

func mustProject(t *testing.T, s directory.Store, p directory.Project) directory.Project {
	t.Helper()
	created, err := s.CreateProject(t.Context(), p)
	if err != nil {
		t.Fatalf("CreateProject(%s): %v", p.Name, err)
	}
	return created
}

func mustShare(t *testing.T, s directory.Store, projectID, userID string, access directory.Access) directory.Share {
	t.Helper()
	sh, err := s.ShareProject(t.Context(), directory.Share{ProjectID: projectID, UserID: userID, Access: access, SharedBy: "someone"})
	if err != nil {
		t.Fatalf("ShareProject(%s): %v", userID, err)
	}
	return sh
}

// projectIDs lists the IDs of ps, in order.
func projectIDs(ps []directory.Project) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.ID
	}
	return out
}

// listAll lists every project in scope q, following page tokens.
func listAll(t *testing.T, s directory.Store, q directory.ProjectQuery) []string {
	t.Helper()
	var ids []string
	for pages := 0; ; pages++ {
		if pages > 100 {
			t.Fatalf("ListProjects(%+v) never ends: a page token leads back to an earlier page", q)
		}
		page, err := s.ListProjects(t.Context(), q)
		if err != nil {
			t.Fatalf("ListProjects(%+v): %v", q, err)
		}
		ids = append(ids, projectIDs(page.Projects)...)
		if page.NextPageToken == "" {
			return ids
		}
		q.PageToken = page.NextPageToken
	}
}

func testCreateProject(t *testing.T, s directory.Store) {
	ctx := t.Context()
	ann := mustUser(t, s, "ann")
	acme := mustOrg(t, s, "Acme", ann.ID)

	p, err := s.CreateProject(ctx, directory.Project{Owner: userOwner(ann.ID), Name: " Poster ", CreatedBy: ann.ID})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if !isUUIDv7(p.ID) || p.Name != "Poster" || p.State != directory.ProjectReady || p.OrgLocked || p.CreatedAt.IsZero() || !p.UpdatedAt.Equal(p.CreatedAt) {
		t.Errorf("project = %+v; want a UUIDv7, trimmed name, ready, unlocked, timestamps", p)
	}
	if got, err := s.GetProject(ctx, p.ID); err != nil || got != p {
		t.Errorf("GetProject = %+v, %v; want %+v", got, err, p)
	}

	locked := mustProject(t, s, directory.Project{Owner: orgOwner(acme.ID), Name: "Brand", OrgLocked: true})
	if !locked.OrgLocked {
		t.Error("org project created locked isn't locked")
	}
	template := mustProject(t, s, directory.Project{Owner: gyoza, Name: "Template"})
	if template.CreatedBy != "" || template.State != directory.ProjectReady {
		t.Errorf("template = %+v", template)
	}
	clone := mustProject(t, s, directory.Project{Owner: userOwner(ann.ID), Name: "Template", State: directory.ProjectCloning, ClonedFrom: template.ID})
	if clone.State != directory.ProjectCloning || clone.ClonedFrom != template.ID {
		t.Errorf("clone = %+v", clone)
	}
}

func testCreateProjectRejects(t *testing.T, s directory.Store) {
	ctx := t.Context()
	ann := mustUser(t, s, "ann")
	tests := []struct {
		name string
		p    directory.Project
		want error
	}{
		{"no name", directory.Project{Owner: userOwner(ann.ID)}, directory.ErrInvalidProject},
		{"locked user project", directory.Project{Owner: userOwner(ann.ID), Name: "X", OrgLocked: true}, directory.ErrInvalidProject},
		{"unknown user", directory.Project{Owner: userOwner("ghost"), Name: "X"}, directory.ErrUserNotFound},
		{"unknown org", directory.Project{Owner: orgOwner(uuid.NewString()), Name: "X"}, directory.ErrOrgNotFound},
		{"clone of nothing", directory.Project{Owner: userOwner(ann.ID), Name: "X", State: directory.ProjectCloning, ClonedFrom: uuid.NewString()}, directory.ErrProjectNotFound},
	}
	for _, tt := range tests {
		if _, err := s.CreateProject(ctx, tt.p); !errors.Is(err, tt.want) {
			t.Errorf("%s: %v, want %v", tt.name, err, tt.want)
		}
	}
	if ids := listAll(t, s, directory.ProjectQuery{VisibleTo: ann.ID}); len(ids) != 0 {
		t.Errorf("refused projects were stored: %v", ids)
	}
	for _, id := range []string{uuid.NewString(), "not-a-uuid"} {
		if _, err := s.GetProject(ctx, id); !errors.Is(err, directory.ErrProjectNotFound) {
			t.Errorf("GetProject(%s) = %v, want ErrProjectNotFound", id, err)
		}
	}
}

// "My projects": what I own, what my org owns, what's actively shared with me;
// each once, oldest first; never clones in progress or templates.
func testListMyProjects(t *testing.T, s directory.Store) {
	ann, bob, eve := mustUser(t, s, "ann"), mustUser(t, s, "bob"), mustUser(t, s, "eve")
	acme := mustOrg(t, s, "Acme", ann.ID)
	beta := mustOrg(t, s, "Beta", eve.ID)

	own := mustProject(t, s, directory.Project{Owner: userOwner(ann.ID), Name: "own"})
	orgs := mustProject(t, s, directory.Project{Owner: orgOwner(acme.ID), Name: "org's"})
	shared := mustProject(t, s, directory.Project{Owner: userOwner(bob.ID), Name: "shared"})
	mustShare(t, s, shared.ID, ann.ID, directory.AccessViewer)
	both := mustProject(t, s, directory.Project{Owner: orgOwner(acme.ID), Name: "org's and shared"})
	mustShare(t, s, both.ID, ann.ID, directory.AccessEditor) // also reachable through the org: listed once
	betas := mustProject(t, s, directory.Project{Owner: orgOwner(beta.ID), Name: "beta's"})
	mustShare(t, s, betas.ID, ann.ID, directory.AccessViewer)
	// Not mine: bob's unshared project, a template, a clone in progress.
	mustProject(t, s, directory.Project{Owner: userOwner(bob.ID), Name: "bob's"})
	template := mustProject(t, s, directory.Project{Owner: gyoza, Name: "template"})
	mustProject(t, s, directory.Project{Owner: userOwner(ann.ID), Name: "cloning", State: directory.ProjectCloning, ClonedFrom: template.ID})

	want := []string{own.ID, orgs.ID, shared.ID, both.ID, betas.ID}
	if got := listAll(t, s, directory.ProjectQuery{VisibleTo: ann.ID}); !slices.Equal(got, want) {
		t.Errorf("ann's projects = %v, want %v", got, want)
	}

	// Beta locks its project: ann's share is suspended, so it disappears;
	// unlocking brings it back.
	if _, err := s.SetOrgLocked(t.Context(), betas.ID, true); err != nil {
		t.Fatalf("SetOrgLocked: %v", err)
	}
	if got := listAll(t, s, directory.ProjectQuery{VisibleTo: ann.ID}); slices.Contains(got, betas.ID) {
		t.Error("a suspended share still lists the locked project")
	}
	if _, err := s.SetOrgLocked(t.Context(), betas.ID, false); err != nil {
		t.Fatalf("SetOrgLocked(false): %v", err)
	}
	if got := listAll(t, s, directory.ProjectQuery{VisibleTo: ann.ID}); !slices.Equal(got, want) {
		t.Errorf("after unlocking: %v, want %v", got, want)
	}

	// Someone with nothing sees nothing.
	if got := listAll(t, s, directory.ProjectQuery{VisibleTo: "nobody"}); len(got) != 0 {
		t.Errorf("a stranger's projects = %v", got)
	}
}

func testListProjectsByOwner(t *testing.T, s directory.Store) {
	ann := mustUser(t, s, "ann")
	acme := mustOrg(t, s, "Acme", ann.ID)
	t1 := mustProject(t, s, directory.Project{Owner: gyoza, Name: "T1"})
	mustProject(t, s, directory.Project{Owner: orgOwner(acme.ID), Name: "org"})
	t2 := mustProject(t, s, directory.Project{Owner: gyoza, Name: "T2"})
	mustProject(t, s, directory.Project{Owner: userOwner(ann.ID), Name: "clone of T1", State: directory.ProjectCloning, ClonedFrom: t1.ID})

	if got, want := listAll(t, s, directory.ProjectQuery{Owner: gyoza}), []string{t1.ID, t2.ID}; !slices.Equal(got, want) {
		t.Errorf("templates = %v, want %v", got, want)
	}
	if got := listAll(t, s, directory.ProjectQuery{Owner: orgOwner(acme.ID)}); len(got) != 1 {
		t.Errorf("acme's projects = %v, want 1", got)
	}
	if got := listAll(t, s, directory.ProjectQuery{Owner: userOwner(ann.ID)}); len(got) != 0 {
		t.Errorf("ann's own projects = %v, want none (her only one is still cloning)", got)
	}
}

func testListProjectsPaging(t *testing.T, s directory.Store) {
	ctx := t.Context()
	ann := mustUser(t, s, "ann")
	var want []string
	for range 7 {
		want = append(want, mustProject(t, s, directory.Project{Owner: userOwner(ann.ID), Name: "P"}).ID)
	}
	q := directory.ProjectQuery{VisibleTo: ann.ID, PageSize: 3}
	var sizes []int
	var got []string
	for len(sizes) < 10 {
		page, err := s.ListProjects(ctx, q)
		if err != nil {
			t.Fatalf("ListProjects: %v", err)
		}
		sizes = append(sizes, len(page.Projects))
		got = append(got, projectIDs(page.Projects)...)
		if page.NextPageToken == "" {
			break
		}
		q.PageToken = page.NextPageToken
	}
	if !slices.Equal(sizes, []int{3, 3, 1}) || !slices.Equal(got, want) {
		t.Errorf("pages %v of %v, want 3/3/1 of %v", sizes, got, want)
	}

	for name, q := range map[string]directory.ProjectQuery{
		"no scope":  {},
		"bad token": {VisibleTo: ann.ID, PageToken: "!!"},
	} {
		if _, err := s.ListProjects(ctx, q); !errors.Is(err, directory.ErrInvalidQuery) {
			t.Errorf("%s: %v, want ErrInvalidQuery", name, err)
		}
	}
}

func testRenameProject(t *testing.T, s directory.Store) {
	ctx := t.Context()
	p := mustProject(t, s, directory.Project{Owner: userOwner(mustUser(t, s, "ann").ID), Name: "Draft"})
	letTimePass()
	renamed, err := s.RenameProject(ctx, p.ID, " Final ")
	if err != nil || renamed.Name != "Final" || !renamed.UpdatedAt.After(p.UpdatedAt) {
		t.Fatalf("RenameProject = %+v, %v", renamed, err)
	}
	letTimePass()
	if same, err := s.RenameProject(ctx, p.ID, "Final"); err != nil || same != renamed {
		t.Errorf("same name again = %+v, %v; want no change", same, err)
	}
	if _, err := s.RenameProject(ctx, p.ID, " "); !errors.Is(err, directory.ErrInvalidProject) {
		t.Errorf("empty name = %v, want ErrInvalidProject", err)
	}
	if _, err := s.RenameProject(ctx, uuid.NewString(), "X"); !errors.Is(err, directory.ErrProjectNotFound) {
		t.Errorf("missing project = %v, want ErrProjectNotFound", err)
	}
}

func testTransferProject(t *testing.T, s directory.Store) {
	ctx := t.Context()
	ann, bob := mustUser(t, s, "ann"), mustUser(t, s, "bob")
	acme := mustOrg(t, s, "Acme", ann.ID)
	p := mustProject(t, s, directory.Project{Owner: userOwner(ann.ID), Name: "Personal"})
	mustShare(t, s, p.ID, bob.ID, directory.AccessViewer)

	letTimePass()
	moved, err := s.TransferProject(ctx, p.ID, orgOwner(acme.ID))
	if err != nil || moved.Owner != orgOwner(acme.ID) || !moved.UpdatedAt.After(p.UpdatedAt) {
		t.Fatalf("TransferProject = %+v, %v", moved, err)
	}
	if _, err := s.GetShare(ctx, p.ID, bob.ID); err != nil {
		t.Errorf("share lost in the transfer: %v", err)
	}
	if same, err := s.TransferProject(ctx, p.ID, orgOwner(acme.ID)); err != nil || same != moved {
		t.Errorf("transfer to the same owner = %+v, %v; want no change", same, err)
	}

	// The lock was the old owner's policy: it doesn't follow the project.
	if _, err := s.SetOrgLocked(ctx, p.ID, true); err != nil {
		t.Fatalf("SetOrgLocked: %v", err)
	}
	back, err := s.TransferProject(ctx, p.ID, userOwner(ann.ID))
	if err != nil || back.OrgLocked {
		t.Errorf("transfer of a locked project = %+v, %v; want unlocked", back, err)
	}

	template := mustProject(t, s, directory.Project{Owner: gyoza, Name: "T"})
	tests := []struct {
		name string
		id   string
		to   storage.Owner
		want error
	}{
		{"template away", template.ID, userOwner(ann.ID), directory.ErrInvalidProject},
		{"to gyoza", p.ID, gyoza, directory.ErrInvalidProject},
		{"to a project", p.ID, storage.Owner{Kind: storage.OwnerProject, ID: template.ID}, directory.ErrInvalidProject},
		{"to an unknown org", p.ID, orgOwner(uuid.NewString()), directory.ErrOrgNotFound},
		{"to an unknown user", p.ID, userOwner("ghost"), directory.ErrUserNotFound},
		{"missing project", uuid.NewString(), userOwner(ann.ID), directory.ErrProjectNotFound},
	}
	for _, tt := range tests {
		if _, err := s.TransferProject(ctx, tt.id, tt.to); !errors.Is(err, tt.want) {
			t.Errorf("%s: %v, want %v", tt.name, err, tt.want)
		}
	}
	if got, _ := s.GetProject(ctx, p.ID); got.Owner != userOwner(ann.ID) {
		t.Errorf("refused transfers moved the project to %v", got.Owner)
	}
}

func testProjectStateAndLock(t *testing.T, s directory.Store) {
	ctx := t.Context()
	ann := mustUser(t, s, "ann")
	acme := mustOrg(t, s, "Acme", ann.ID)
	source := mustProject(t, s, directory.Project{Owner: gyoza, Name: "T"})
	clone := mustProject(t, s, directory.Project{Owner: userOwner(ann.ID), Name: "T", State: directory.ProjectCloning, ClonedFrom: source.ID})

	letTimePass()
	ready, err := s.MarkProjectReady(ctx, clone.ID)
	if err != nil || ready.State != directory.ProjectReady || !ready.UpdatedAt.After(clone.UpdatedAt) {
		t.Fatalf("MarkProjectReady = %+v, %v", ready, err)
	}
	letTimePass()
	if again, err := s.MarkProjectReady(ctx, clone.ID); err != nil || again != ready {
		t.Errorf("marking ready again = %+v, %v; want no change", again, err)
	}

	org := mustProject(t, s, directory.Project{Owner: orgOwner(acme.ID), Name: "Brand"})
	locked, err := s.SetOrgLocked(ctx, org.ID, true)
	if err != nil || !locked.OrgLocked {
		t.Fatalf("SetOrgLocked = %+v, %v", locked, err)
	}
	letTimePass()
	if again, err := s.SetOrgLocked(ctx, org.ID, true); err != nil || again != locked {
		t.Errorf("locking again = %+v, %v; want no change", again, err)
	}
	if _, err := s.SetOrgLocked(ctx, clone.ID, true); !errors.Is(err, directory.ErrInvalidProject) {
		t.Errorf("locking a user project = %v, want ErrInvalidProject", err)
	}
	for _, call := range []func() error{
		func() error { _, err := s.MarkProjectReady(ctx, uuid.NewString()); return err },
		func() error { _, err := s.SetOrgLocked(ctx, uuid.NewString(), true); return err },
	} {
		if err := call(); !errors.Is(err, directory.ErrProjectNotFound) {
			t.Errorf("missing project = %v, want ErrProjectNotFound", err)
		}
	}
}

func testShareProject(t *testing.T, s directory.Store) {
	ctx := t.Context()
	ann, bob, cat := mustUser(t, s, "ann"), mustUser(t, s, "bob"), mustUser(t, s, "cat")
	p := mustProject(t, s, directory.Project{Owner: userOwner(ann.ID), Name: "Poster"})

	first := mustShare(t, s, p.ID, bob.ID, directory.AccessViewer)
	if first.CreatedAt.IsZero() || !first.UpdatedAt.Equal(first.CreatedAt) || first.Access != directory.AccessViewer {
		t.Errorf("new share = %+v", first)
	}
	if got, err := s.GetShare(ctx, p.ID, bob.ID); err != nil || got != first {
		t.Errorf("GetShare = %+v, %v; want %+v", got, err, first)
	}
	letTimePass()
	if again := mustShare(t, s, p.ID, bob.ID, directory.AccessViewer); again != first {
		t.Errorf("same share again = %+v; want no change", again)
	}
	letTimePass()
	promoted := mustShare(t, s, p.ID, bob.ID, directory.AccessEditor)
	if promoted.Access != directory.AccessEditor || !promoted.CreatedAt.Equal(first.CreatedAt) || !promoted.UpdatedAt.After(first.UpdatedAt) {
		t.Errorf("promoted share = %+v; want editor, same CreatedAt, later UpdatedAt", promoted)
	}
	letTimePass()
	mustShare(t, s, p.ID, cat.ID, directory.AccessViewer)
	letTimePass()
	abe := mustUser(t, s, "abe") // first by name, last by share time
	mustShare(t, s, p.ID, abe.ID, directory.AccessViewer)
	shares, err := s.ListShares(ctx, p.ID)
	var order []string
	for _, sh := range shares {
		order = append(order, sh.UserID)
	}
	if want := []string{bob.ID, cat.ID, abe.ID}; err != nil || !slices.Equal(order, want) {
		t.Errorf("ListShares = %v, %v; want %v (oldest first)", order, err, want)
	}
	// Go randomises where each map iteration starts, so an unsorted store could
	// return the right order by chance: ask a few more times.
	for range 10 {
		if again, _ := s.ListShares(ctx, p.ID); len(again) != 3 || again[0].UserID != bob.ID || again[2].UserID != abe.ID {
			t.Fatalf("ListShares order varies between calls: %+v", again)
		}
	}
	// Another project's shares never show up here.
	other := mustProject(t, s, directory.Project{Owner: userOwner(ann.ID), Name: "Other"})
	mustShare(t, s, other.ID, bob.ID, directory.AccessViewer)
	if shares, _ := s.ListShares(ctx, p.ID); len(shares) != 3 {
		t.Errorf("ListShares includes another project's shares: %+v", shares)
	}

	template := mustProject(t, s, directory.Project{Owner: gyoza, Name: "T"})
	tests := []struct {
		name string
		sh   directory.Share
		want error
	}{
		{"a template", directory.Share{ProjectID: template.ID, UserID: bob.ID, Access: directory.AccessViewer, SharedBy: "x"}, directory.ErrInvalidShare},
		{"no level", directory.Share{ProjectID: p.ID, UserID: bob.ID, SharedBy: "x"}, directory.ErrInvalidShare},
		{"unknown user", directory.Share{ProjectID: p.ID, UserID: "ghost", Access: directory.AccessViewer, SharedBy: "x"}, directory.ErrUserNotFound},
		{"missing project", directory.Share{ProjectID: uuid.NewString(), UserID: bob.ID, Access: directory.AccessViewer, SharedBy: "x"}, directory.ErrProjectNotFound},
	}
	for _, tt := range tests {
		if _, err := s.ShareProject(ctx, tt.sh); !errors.Is(err, tt.want) {
			t.Errorf("%s: %v, want %v", tt.name, err, tt.want)
		}
	}
	if _, err := s.ListShares(ctx, uuid.NewString()); !errors.Is(err, directory.ErrProjectNotFound) {
		t.Errorf("ListShares(missing) = %v, want ErrProjectNotFound", err)
	}
}

// A locked project takes no new or changed shares for outsiders; existing
// ones stay recorded (suspended) and members can still be shared with.
func testShareLockedProject(t *testing.T, s directory.Store) {
	ctx := t.Context()
	ann, bob, cat := mustUser(t, s, "ann"), mustUser(t, s, "bob"), mustUser(t, s, "cat")
	acme := mustOrg(t, s, "Acme", ann.ID)
	mustJoin(t, s, acme.ID, cat, directory.RoleMember)
	p := mustProject(t, s, directory.Project{Owner: orgOwner(acme.ID), Name: "Brand"})
	before := mustShare(t, s, p.ID, bob.ID, directory.AccessViewer) // bob: an outsider
	if _, err := s.SetOrgLocked(ctx, p.ID, true); err != nil {
		t.Fatalf("SetOrgLocked: %v", err)
	}

	if _, err := s.ShareProject(ctx, directory.Share{ProjectID: p.ID, UserID: bob.ID, Access: directory.AccessEditor, SharedBy: "x"}); !errors.Is(err, directory.ErrInvalidShare) {
		t.Errorf("changing an outsider's share while locked = %v, want ErrInvalidShare", err)
	}
	dan := mustUser(t, s, "dan")
	if _, err := s.ShareProject(ctx, directory.Share{ProjectID: p.ID, UserID: dan.ID, Access: directory.AccessViewer, SharedBy: "x"}); !errors.Is(err, directory.ErrInvalidShare) {
		t.Errorf("a new outsider while locked = %v, want ErrInvalidShare", err)
	}
	mustShare(t, s, p.ID, cat.ID, directory.AccessViewer) // a member: allowed (and harmless)

	// Bob's share is suspended, not deleted.
	if got, err := s.GetShare(ctx, p.ID, bob.ID); err != nil || got != before {
		t.Errorf("bob's share while locked = %+v, %v; want it kept as it was", got, err)
	}
	if shares, _ := s.ListShares(ctx, p.ID); len(shares) != 2 {
		t.Errorf("ListShares while locked = %+v; want bob's (suspended) and cat's", shares)
	}
}

func testUnshare(t *testing.T, s directory.Store) {
	ctx := t.Context()
	ann, bob := mustUser(t, s, "ann"), mustUser(t, s, "bob")
	p := mustProject(t, s, directory.Project{Owner: userOwner(ann.ID), Name: "Poster"})
	mustShare(t, s, p.ID, bob.ID, directory.AccessEditor)

	if err := s.Unshare(ctx, p.ID, bob.ID); err != nil {
		t.Fatalf("Unshare: %v", err)
	}
	if _, err := s.GetShare(ctx, p.ID, bob.ID); !errors.Is(err, directory.ErrShareNotFound) {
		t.Errorf("GetShare after Unshare = %v, want ErrShareNotFound", err)
	}
	if got := listAll(t, s, directory.ProjectQuery{VisibleTo: bob.ID}); len(got) != 0 {
		t.Errorf("bob still sees %v", got)
	}
	for _, args := range [][2]string{{p.ID, bob.ID}, {uuid.NewString(), bob.ID}, {"junk", "junk"}} {
		if err := s.Unshare(ctx, args[0], args[1]); err != nil {
			t.Errorf("Unshare(%v) again = %v, want nil (safe to retry)", args, err)
		}
	}
}

// The cleanup loop's list of stalled clones: only clones still copying, not
// deleting, created before the cut-off, oldest first.
func testListCloningProjects(t *testing.T, s directory.Store) {
	ctx := t.Context()
	ann := mustUser(t, s, "ann")
	source := mustProject(t, s, directory.Project{Owner: gyoza, Name: "T"})
	clone := func() directory.Project {
		letTimePass()
		return mustProject(t, s, directory.Project{Owner: userOwner(ann.ID), Name: "T", State: directory.ProjectCloning, ClonedFrom: source.ID})
	}
	first, second, done, deleting := clone(), clone(), clone(), clone()
	if _, err := s.MarkProjectReady(ctx, done.ID); err != nil {
		t.Fatalf("MarkProjectReady: %v", err)
	}
	if _, err := s.MarkProjectDeleting(ctx, deleting.ID); err != nil {
		t.Fatalf("MarkProjectDeleting: %v", err)
	}
	letTimePass()
	cutoff := time.Now()
	clone() // too recent: may still be running

	got, err := s.ListCloningProjects(ctx, cutoff, 10)
	if err != nil {
		t.Fatalf("ListCloningProjects: %v", err)
	}
	if ids := projectIDs(got); !slices.Equal(ids, []string{first.ID, second.ID}) {
		t.Errorf("ListCloningProjects = %v, want [%s %s] (oldest first; not ready, deleting or recent)", ids, first.ID, second.ID)
	}
	if got, _ := s.ListCloningProjects(ctx, cutoff, 1); len(got) != 1 || got[0].ID != first.ID {
		t.Errorf("limit 1 = %v, want the oldest", projectIDs(got))
	}
	if _, err := s.ListCloningProjects(ctx, cutoff, 0); !errors.Is(err, directory.ErrInvalidQuery) {
		t.Errorf("limit 0 = %v, want ErrInvalidQuery", err)
	}
}
