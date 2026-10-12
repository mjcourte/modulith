package manager_test

import (
	"errors"
	"gyoza/services/iam/internal/directory"
	"gyoza/services/iam/internal/manager"
	"slices"
	"testing"

	"github.com/google/uuid"
)

// projectWorld: ann is admin of acme, mem a member; eve is admin of beta;
// bob, vic and edi are in no org.
type projectWorld struct {
	m                            *manager.Manager
	ann, mem, eve, bob, vic, edi directory.User
	acme, beta                   directory.Org
}

func newProjectWorld(t *testing.T) projectWorld {
	t.Helper()
	m, _ := newManager(t)
	w := projectWorld{m: m}
	w.ann, w.mem, w.eve = signIn(t, m, "ann"), signIn(t, m, "mem"), signIn(t, m, "eve")
	w.bob, w.vic, w.edi = signIn(t, m, "bob"), signIn(t, m, "vic"), signIn(t, m, "edi")
	w.acme = managerOrg(t, m, w.ann.ID, "Acme")
	join(t, m, w.ann.ID, w.acme.ID, w.mem, directory.RoleMember)
	w.beta = managerOrg(t, m, w.eve.ID, "Beta")
	return w
}

func (w projectWorld) create(t *testing.T, actor string, owner string, orgID string, locked bool) directory.Project {
	t.Helper()
	o := userOwner(actor)
	if orgID != "" {
		o = orgOwner(orgID)
	}
	p, err := w.m.CreateProject(t.Context(), actor, o, owner, locked)
	if err != nil {
		t.Fatalf("CreateProject(%s): %v", owner, err)
	}
	return p
}

func (w projectWorld) share(t *testing.T, actor, projectID string, u directory.User, a directory.Access) {
	t.Helper()
	if _, err := w.m.ShareProject(t.Context(), actor, projectID, u.Email, a); err != nil {
		t.Fatalf("ShareProject(%s): %v", u.ID, err)
	}
}

func TestManagerCreateProject(t *testing.T) {
	ctx := t.Context()
	w := newProjectWorld(t)

	mine := w.create(t, w.ann.ID, "Mine", "", false)
	if mine.Owner != userOwner(w.ann.ID) || mine.CreatedBy != w.ann.ID {
		t.Errorf("personal project = %+v", mine)
	}
	// Any member may create an org project, locked or not.
	locked := w.create(t, w.mem.ID, "Brand", w.acme.ID, true)
	open := w.create(t, w.mem.ID, "Open", w.acme.ID, false)
	if !locked.OrgLocked || open.OrgLocked || locked.CreatedBy != w.mem.ID {
		t.Errorf("org projects: locked %+v, open %+v", locked, open)
	}

	tests := []struct {
		name  string
		actor string
		owner directory.Project
		want  error
	}{
		{"for someone else", w.bob.ID, directory.Project{Owner: userOwner(w.ann.ID)}, manager.ErrForbidden},
		{"for another org", w.mem.ID, directory.Project{Owner: orgOwner(w.beta.ID)}, manager.ErrForbidden},
		{"for an org, from outside", w.bob.ID, directory.Project{Owner: orgOwner(w.acme.ID)}, manager.ErrForbidden},
		{"a template", w.ann.ID, directory.Project{Owner: gyoza}, manager.ErrForbidden},
		{"by an unknown user", "ghost", directory.Project{Owner: userOwner("ghost")}, manager.ErrForbidden},
		{"a locked personal project", w.ann.ID, directory.Project{Owner: userOwner(w.ann.ID), OrgLocked: true}, directory.ErrInvalidProject},
	}
	for _, tt := range tests {
		if _, err := w.m.CreateProject(ctx, tt.actor, tt.owner.Owner, "X", tt.owner.OrgLocked); !errors.Is(err, tt.want) {
			t.Errorf("%s: %v, want %v", tt.name, err, tt.want)
		}
	}

	tmpl, err := w.m.CreateTemplate(ctx, "Poster template")
	if err != nil || tmpl.Owner != gyoza || tmpl.CreatedBy != "" {
		t.Errorf("CreateTemplate = %+v, %v", tmpl, err)
	}
}

func TestManagerSeeProjects(t *testing.T) {
	ctx := t.Context()
	w := newProjectWorld(t)
	annP := w.create(t, w.ann.ID, "ann's", "", false)
	w.share(t, w.ann.ID, annP.ID, w.vic, directory.AccessViewer)
	orgP := w.create(t, w.ann.ID, "acme's", w.acme.ID, false)
	tmpl, _ := w.m.CreateTemplate(ctx, "T")

	tests := []struct {
		name    string
		actor   string
		project string
		want    directory.Access // AccessNone: forbidden
	}{
		{"owner", w.ann.ID, annP.ID, directory.AccessEditor},
		{"viewer share", w.vic.ID, annP.ID, directory.AccessViewer},
		{"stranger", w.bob.ID, annP.ID, directory.AccessNone},
		{"org member", w.mem.ID, orgP.ID, directory.AccessEditor},
		{"other org", w.eve.ID, orgP.ID, directory.AccessNone},
		{"template", w.bob.ID, tmpl.ID, directory.AccessViewer},
		{"missing", w.ann.ID, uuid.NewString(), directory.AccessNone},
		{"unknown user", "ghost", tmpl.ID, directory.AccessNone},
	}
	for _, tt := range tests {
		v, err := w.m.GetProject(ctx, tt.actor, tt.project)
		switch {
		case tt.want == directory.AccessNone && !isForbidden(err):
			t.Errorf("%s: %+v, %v; want ErrForbidden", tt.name, v, err)
		case tt.want != directory.AccessNone && (err != nil || v.Access != tt.want || v.Project.ID != tt.project):
			t.Errorf("%s: %+v, %v; want access %q", tt.name, v, err, tt.want)
		}
	}

	page, err := w.m.ListMyProjects(ctx, w.mem.ID, 0, "")
	if err != nil || len(page.Projects) != 1 || page.Projects[0].ID != orgP.ID {
		t.Errorf("mem's projects = %+v, %v; want acme's", page.Projects, err)
	}
	page, err = w.m.ListTemplates(ctx, w.bob.ID, 0, "")
	if err != nil || len(page.Projects) != 1 || page.Projects[0].ID != tmpl.ID {
		t.Errorf("templates = %+v, %v", page.Projects, err)
	}
	if _, err := w.m.ListMyProjects(ctx, "ghost", 0, ""); !isForbidden(err) {
		t.Errorf("unknown user's projects = %v, want ErrForbidden", err)
	}
	if _, err := w.m.ListTemplates(ctx, "ghost", 0, ""); !isForbidden(err) {
		t.Errorf("unknown user's templates = %v, want ErrForbidden", err)
	}
}

func TestManagerManageProjects(t *testing.T) {
	ctx := t.Context()
	w := newProjectWorld(t)
	annP := w.create(t, w.ann.ID, "ann's", "", false)
	w.share(t, w.ann.ID, annP.ID, w.edi, directory.AccessEditor)
	orgP := w.create(t, w.mem.ID, "acme's", w.acme.ID, false)
	tmpl, _ := w.m.CreateTemplate(ctx, "T")

	if p, err := w.m.RenameProject(ctx, w.ann.ID, annP.ID, "Renamed"); err != nil || p.Name != "Renamed" {
		t.Errorf("owner renames = %+v, %v", p, err)
	}
	if p, err := w.m.RenameProject(ctx, w.ann.ID, orgP.ID, "Org renamed"); err != nil || p.Name != "Org renamed" {
		t.Errorf("org admin renames = %+v, %v", p, err)
	}
	refused := []struct {
		name, actor, project string
	}{
		{"editor share renames", w.edi.ID, annP.ID},
		{"org member (not admin) renames", w.mem.ID, orgP.ID},
		{"anyone renames a template", w.ann.ID, tmpl.ID},
		{"stranger renames", w.bob.ID, annP.ID},
	}
	for _, tt := range refused {
		if _, err := w.m.RenameProject(ctx, tt.actor, tt.project, "X"); !isForbidden(err) {
			t.Errorf("%s = %v, want ErrForbidden", tt.name, err)
		}
	}

	// Moving a personal project into your org.
	memP := w.create(t, w.mem.ID, "mem's", "", false)
	moved, err := w.m.MoveProjectToOrg(ctx, w.mem.ID, memP.ID)
	if err != nil || moved.Owner != orgOwner(w.acme.ID) || moved.OrgLocked {
		t.Errorf("MoveProjectToOrg = %+v, %v; want acme's, unlocked", moved, err)
	}
	bobP := w.create(t, w.bob.ID, "bob's", "", false)
	if _, err := w.m.MoveProjectToOrg(ctx, w.bob.ID, bobP.ID); !errors.Is(err, directory.ErrNotMember) {
		t.Errorf("moving without an org = %v, want ErrNotMember", err)
	}
	if _, err := w.m.MoveProjectToOrg(ctx, w.ann.ID, orgP.ID); !errors.Is(err, directory.ErrInvalidProject) {
		t.Errorf("moving an org project = %v, want ErrInvalidProject", err)
	}
	if _, err := w.m.MoveProjectToOrg(ctx, w.edi.ID, annP.ID); !isForbidden(err) {
		t.Errorf("moving someone else's project = %v, want ErrForbidden", err)
	}
}

func TestManagerSharing(t *testing.T) {
	ctx := t.Context()
	w := newProjectWorld(t)
	p := w.create(t, w.ann.ID, "Poster", "", false)
	w.share(t, w.ann.ID, p.ID, w.vic, directory.AccessViewer)
	w.share(t, w.ann.ID, p.ID, w.edi, directory.AccessEditor)

	// Viewers can't share or see the share list.
	if _, err := w.m.ShareProject(ctx, w.vic.ID, p.ID, w.bob.Email, directory.AccessViewer); !isForbidden(err) {
		t.Errorf("viewer shares = %v, want ErrForbidden", err)
	}
	if _, err := w.m.ListShares(ctx, w.vic.ID, p.ID); !isForbidden(err) {
		t.Errorf("viewer lists shares = %v, want ErrForbidden", err)
	}
	// Editors can share as editor, and change or remove others' shares.
	if sh, err := w.m.ShareProject(ctx, w.edi.ID, p.ID, w.bob.Email, directory.AccessEditor); err != nil || sh.UserID != w.bob.ID || sh.SharedBy != w.edi.ID {
		t.Errorf("editor shares = %+v, %v", sh, err)
	}
	if err := w.m.Unshare(ctx, w.edi.ID, p.ID, w.vic.ID); err != nil {
		t.Errorf("editor removes vic's share: %v", err)
	}
	shares, err := w.m.ListShares(ctx, w.edi.ID, p.ID)
	got := sharedWith(shares)
	slices.Sort(got) // who, not in what order (the store suite covers order)
	if err != nil || !slices.Equal(got, []string{w.bob.ID, w.edi.ID}) {
		t.Errorf("shares = %v, %v; want bob and edi", got, err)
	}

	// Anyone can leave a project shared with them; strangers can't remove anyone.
	if err := w.m.Unshare(ctx, w.cat(t).ID, p.ID, w.bob.ID); !isForbidden(err) {
		t.Errorf("stranger removes bob = %v, want ErrForbidden", err)
	}
	if err := w.m.Unshare(ctx, w.bob.ID, p.ID, w.bob.ID); err != nil {
		t.Errorf("bob leaves: %v", err)
	}
	if _, err := w.m.GetProject(ctx, w.bob.ID, p.ID); !isForbidden(err) {
		t.Errorf("bob after leaving = %v, want ErrForbidden", err)
	}
	// A viewer can't remove shares, but can still leave.
	dan := signIn(t, w.m, "dan")
	w.share(t, w.ann.ID, p.ID, dan, directory.AccessViewer)
	if err := w.m.Unshare(ctx, dan.ID, p.ID, w.edi.ID); !isForbidden(err) {
		t.Errorf("viewer removes edi = %v, want ErrForbidden", err)
	}
	if err := w.m.Unshare(ctx, dan.ID, p.ID, dan.ID); err != nil {
		t.Errorf("viewer leaves: %v", err)
	}

	if _, err := w.m.ShareProject(ctx, w.ann.ID, p.ID, "nobody@example.com", directory.AccessViewer); !errors.Is(err, directory.ErrUserNotFound) {
		t.Errorf("sharing with an address nobody signed up with = %v, want ErrUserNotFound", err)
	}
	tmpl, _ := w.m.CreateTemplate(ctx, "T")
	if _, err := w.m.ShareProject(ctx, w.ann.ID, tmpl.ID, w.bob.Email, directory.AccessViewer); !isForbidden(err) {
		t.Errorf("sharing a template = %v, want ErrForbidden (nobody edits templates)", err)
	}
}

// cat is one more user in no org.
func (w projectWorld) cat(t *testing.T) directory.User { return signIn(t, w.m, "cat") }

func sharedWith(shares []directory.Share) []string {
	var out []string
	for _, s := range shares {
		out = append(out, s.UserID)
	}
	return out
}

func TestManagerOrgLock(t *testing.T) {
	ctx := t.Context()
	w := newProjectWorld(t)
	p := w.create(t, w.mem.ID, "Brand", w.acme.ID, false)
	w.share(t, w.mem.ID, p.ID, w.vic, directory.AccessViewer) // an outsider, before the lock

	if _, err := w.m.SetOrgLocked(ctx, w.mem.ID, p.ID, true); !isForbidden(err) {
		t.Errorf("member locks = %v, want ErrForbidden (admins only)", err)
	}
	if _, err := w.m.SetOrgLocked(ctx, w.vic.ID, p.ID, true); !isForbidden(err) {
		t.Errorf("outsider locks = %v, want ErrForbidden", err)
	}
	if locked, err := w.m.SetOrgLocked(ctx, w.ann.ID, p.ID, true); err != nil || !locked.OrgLocked {
		t.Fatalf("admin locks = %+v, %v", locked, err)
	}

	// While locked: vic's share is suspended and outsiders can't be added.
	if _, err := w.m.GetProject(ctx, w.vic.ID, p.ID); !isForbidden(err) {
		t.Errorf("suspended outsider sees the project: %v", err)
	}
	if _, err := w.m.ShareProject(ctx, w.mem.ID, p.ID, w.bob.Email, directory.AccessViewer); !errors.Is(err, directory.ErrInvalidShare) {
		t.Errorf("sharing outside a locked project = %v, want ErrInvalidShare", err)
	}
	if v, err := w.m.GetProject(ctx, w.mem.ID, p.ID); err != nil || v.Access != directory.AccessEditor {
		t.Errorf("member while locked = %+v, %v; want editor", v, err)
	}

	// Unlocking brings vic's share back.
	if _, err := w.m.SetOrgLocked(ctx, w.ann.ID, p.ID, false); err != nil {
		t.Fatalf("admin unlocks: %v", err)
	}
	if v, err := w.m.GetProject(ctx, w.vic.ID, p.ID); err != nil || v.Access != directory.AccessViewer {
		t.Errorf("vic after unlocking = %+v, %v; want viewer again", v, err)
	}

	mine := w.create(t, w.ann.ID, "Mine", "", false)
	if _, err := w.m.SetOrgLocked(ctx, w.ann.ID, mine.ID, true); !errors.Is(err, directory.ErrInvalidProject) {
		t.Errorf("locking a personal project = %v, want ErrInvalidProject", err)
	}
}
