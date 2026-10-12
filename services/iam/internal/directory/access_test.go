package directory

import (
	"errors"
	"testing"

	"gyoza/services/storage"
)

func TestProjectAccess(t *testing.T) {
	userProject := Project{Owner: ruleUser, State: ProjectReady} // owned by ann
	orgProject := Project{Owner: ruleOrg, State: ProjectReady}   // owned by acme
	locked := Project{Owner: ruleOrg, State: ProjectReady, OrgLocked: true}
	template := Project{Owner: ruleGyoza, State: ProjectReady}
	cloning := Project{Owner: ruleUser, State: ProjectCloning}

	tests := []struct {
		name  string
		p     Project
		actor string
		org   string // the actor's org
		share Access
		want  Access
	}{
		{"owner", userProject, "ann", "", AccessNone, AccessEditor},
		{"stranger", userProject, "bob", "", AccessNone, AccessNone},
		{"viewer share", userProject, "bob", "", AccessViewer, AccessViewer},
		{"editor share", userProject, "bob", "", AccessEditor, AccessEditor},
		{"owner with a viewer share keeps editor", userProject, "ann", "", AccessViewer, AccessEditor},
		{"org member", orgProject, "bob", "acme", AccessNone, AccessEditor},
		{"member of another org", orgProject, "bob", "beta", AccessNone, AccessNone},
		{"org member with a viewer share keeps editor", orgProject, "bob", "acme", AccessViewer, AccessEditor},
		{"outsider share, unlocked", orgProject, "bob", "", AccessEditor, AccessEditor},
		{"outsider share, locked: suspended", locked, "bob", "", AccessEditor, AccessNone},
		{"other org's share, locked: suspended", locked, "bob", "beta", AccessViewer, AccessNone},
		{"member of a locked project's org", locked, "bob", "acme", AccessNone, AccessEditor},
		{"template: anyone views", template, "bob", "", AccessNone, AccessViewer},
		{"cloning: owner gets nothing yet", cloning, "ann", "", AccessNone, AccessNone},
		{"cloning: share gets nothing yet", cloning, "bob", "", AccessEditor, AccessNone},
		{"no actor", template, "", "", AccessNone, AccessNone},
	}
	for _, tt := range tests {
		if got := projectAccess(tt.p, tt.actor, tt.org, tt.share); got != tt.want {
			t.Errorf("%s: %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestAccessOrder(t *testing.T) {
	levels := []Access{AccessNone, AccessViewer, AccessEditor}
	for i, a := range levels {
		for j, b := range levels {
			if got, want := a.AtLeast(b), i >= j; got != want {
				t.Errorf("%q.AtLeast(%q) = %v, want %v", a, b, got, want)
			}
			want := a
			if j > i {
				want = b
			}
			if got := greatest(a, b); got != want {
				t.Errorf("greatest(%q, %q) = %q, want %q", a, b, got, want)
			}
		}
	}
	if Access("owner").rank() != 0 {
		t.Error("an unknown level must rank as no access")
	}
}

// The owner kinds the rules don't name (a project owning a project) grant nothing.
func TestProjectAccessUnknownOwner(t *testing.T) {
	p := Project{Owner: storage.Owner{Kind: storage.OwnerProject, ID: "p1"}, State: ProjectReady}
	if got := projectAccess(p, "ann", "acme", AccessNone); got != AccessNone {
		t.Errorf("project-owned project: %q, want none", got)
	}
}

// AccessTo gathers the facts from a Store. The Manager calls it directly (the
// authorizer checks the actor first), so its own checks are tested here.
func TestAccessTo(t *testing.T) {
	ctx := t.Context()
	s := NewMemoryStore()
	if _, _, err := s.UpsertUser(ctx, Identity{UserID: "ann", Email: "ann@example.com"}); err != nil {
		t.Fatalf("UpsertUser: %v", err)
	}
	tmpl, err := s.CreateProject(ctx, Project{Owner: ruleGyoza, Name: "T"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	if _, a, err := AccessTo(ctx, s, "ann", tmpl.ID); err != nil || a != AccessViewer {
		t.Errorf("signed-in user on a template = %q, %v; want viewer", a, err)
	}
	for _, actor := range []string{"ghost", ""} {
		if _, a, err := AccessTo(ctx, s, actor, tmpl.ID); err != nil || a != AccessNone {
			t.Errorf("actor %q on a template = %q, %v; want none (unknown users get nothing)", actor, a, err)
		}
	}
	if _, _, err := AccessTo(ctx, s, "ann", newID()); !errors.Is(err, ErrProjectNotFound) {
		t.Errorf("missing project = %v, want ErrProjectNotFound", err)
	}
}
