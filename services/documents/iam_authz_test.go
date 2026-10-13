package documents_test

import (
	"context"
	"errors"
	"testing"

	"gyoza/services/documents"
	"gyoza/services/iam"
	"gyoza/services/storage"
)

// iamWorld is iam's real authorizer over its memory store, holding:
//   - owen's own project, shared with vic (viewer) and eddie (editor)
//   - an org project of olga's org (olga is its admin)
//   - a clone of owen's project still being copied, and one being deleted
//
// sam has no access to anything.
type iamWorld struct {
	authz                       *documents.IAMAuthorizer
	own, org, cloning, deleting string
}

func newIAMWorld(t *testing.T) iamWorld {
	t.Helper()
	ctx := context.Background()
	store := iam.NewMemoryStore()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"owen", "olga", "vic", "eddie", "sam"} {
		_, _, err := store.UpsertUser(ctx, iam.Identity{UserID: id, Email: id + "@example.com", EmailVerified: true, Name: id})
		must(err)
	}
	org, _, err := store.CreateOrg(ctx, iam.Org{Name: "Acme"}, "olga")
	must(err)

	project := func(owner storage.Owner, p iam.Project) string {
		t.Helper()
		p.Owner, p.Name = owner, "Poster"
		created, err := store.CreateProject(ctx, p)
		must(err)
		return created.ID
	}
	owen := storage.Owner{Kind: storage.OwnerUser, ID: "owen"}
	w := iamWorld{
		own: project(owen, iam.Project{CreatedBy: "owen"}),
		org: project(storage.Owner{Kind: storage.OwnerOrg, ID: org.ID}, iam.Project{CreatedBy: "olga"}),
	}
	w.cloning = project(owen, iam.Project{CreatedBy: "owen", State: iam.ProjectCloning, ClonedFrom: w.own})
	w.deleting = project(owen, iam.Project{CreatedBy: "owen"})
	for user, access := range map[string]iam.Access{"vic": iam.AccessViewer, "eddie": iam.AccessEditor} {
		for _, p := range []string{w.own, w.cloning, w.deleting} {
			_, err := store.ShareProject(ctx, iam.Share{ProjectID: p, UserID: user, Access: access, SharedBy: "owen"})
			must(err)
		}
	}
	_, err = store.MarkProjectDeleting(ctx, w.deleting) // after sharing: iam won't share it once deleting
	must(err)
	w.authz = documents.NewIAMAuthorizer(iam.NewStorageAuthorizer(store))
	return w
}

func TestIAMAuthorizer(t *testing.T) {
	w := newIAMWorld(t)
	ctx := context.Background()
	cases := []struct {
		actor, project, name string
		read, write          bool
	}{
		{"owen", w.own, "owner", true, true},
		{"eddie", w.own, "editor share", true, true},
		{"vic", w.own, "viewer share", true, false},
		{"sam", w.own, "stranger", false, false},
		{"olga", w.org, "org member", true, true},
		{"owen", w.org, "outside the org", false, false},
		{"owen", w.cloning, "owner of a clone being copied", false, false},
		{"eddie", w.cloning, "editor of a clone being copied", false, false},
		{"owen", w.deleting, "owner of a project being deleted", false, false},
		{"owen", "0190a5e0-0000-7000-8000-000000000000", "unknown project", false, false},
		{"", w.own, "no actor", false, false},
		{"nobody", w.own, "unknown user", false, false},
	}
	check := func(c string, verb string, allowed bool, err error) {
		t.Helper()
		switch {
		case allowed && err != nil:
			t.Errorf("%s: %s refused: %v", c, verb, err)
		case !allowed && !errors.Is(err, documents.ErrForbidden):
			t.Errorf("%s: %s got %v, want documents.ErrForbidden", c, verb, err)
		}
	}
	for _, c := range cases {
		check(c.name, "read", c.read, w.authz.CanRead(ctx, c.actor, c.project))
		check(c.name, "write", c.write, w.authz.CanWrite(ctx, c.actor, c.project))
	}
}

// failingIAM is iam whose store is down: not a refusal.
type failingIAM struct{}

var errIAMDown = errors.New("iam store unavailable")

func (failingIAM) CanRead(context.Context, string, storage.Owner, string) error  { return errIAMDown }
func (failingIAM) CanWrite(context.Context, string, storage.Owner, string) error { return errIAMDown }
func (failingIAM) ProjectOwner(context.Context, string) (storage.Owner, error) {
	return storage.Owner{}, errIAMDown
}

// A failing iam is an error, not "forbidden": callers must not report a
// refusal (403) for an outage.
func TestIAMAuthorizerPassesFailuresThrough(t *testing.T) {
	a := documents.NewIAMAuthorizer(failingIAM{})
	err := a.CanWrite(context.Background(), "owen", "p")
	if !errors.Is(err, errIAMDown) || errors.Is(err, documents.ErrForbidden) {
		t.Errorf("got %v, want the iam error, not ErrForbidden", err)
	}
}
