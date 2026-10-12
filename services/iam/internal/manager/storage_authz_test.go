package manager_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"gyoza/services/iam/internal/directory"
	"gyoza/services/iam/internal/manager"
	"gyoza/services/storage"
)

// authzWorld is a small populated store:
//
//	ann: admin of acme; owns annP (shared: vic viewer, edi editor)
//	mem: member of acme
//	eve: admin of beta
//	bob, vic, edi: in no org
//	acmeP: acme's (vic viewer); lockedP: acme's, org-locked (bob's share suspended)
//	tmpl: a gyoza template; cloning: ann's clone still being copied
type authzWorld struct {
	store                               *directory.MemoryStore
	acme                                directory.Org
	annP, acmeP, lockedP, tmpl, cloning directory.Project
}

func newAuthzWorld(t *testing.T) authzWorld {
	t.Helper()
	s := directory.NewMemoryStore()
	w := authzWorld{store: s}
	ann := mustUser(t, s, "ann")
	for _, name := range []string{"mem", "eve", "bob", "vic", "edi"} {
		mustUser(t, s, name)
	}
	w.acme = mustOrg(t, s, "Acme", ann.ID)
	mem, _ := s.GetUser(t.Context(), "mem")
	mustJoin(t, s, w.acme.ID, mem, directory.RoleMember)
	mustOrg(t, s, "Beta", "eve")

	w.annP = mustProject(t, s, directory.Project{Owner: userOwner("ann"), Name: "ann's"})
	mustShare(t, s, w.annP.ID, "vic", directory.AccessViewer)
	mustShare(t, s, w.annP.ID, "edi", directory.AccessEditor)
	w.acmeP = mustProject(t, s, directory.Project{Owner: orgOwner(w.acme.ID), Name: "acme's"})
	mustShare(t, s, w.acmeP.ID, "vic", directory.AccessViewer)
	w.lockedP = mustProject(t, s, directory.Project{Owner: orgOwner(w.acme.ID), Name: "locked"})
	mustShare(t, s, w.lockedP.ID, "bob", directory.AccessEditor)
	if _, err := s.SetOrgLocked(t.Context(), w.lockedP.ID, true); err != nil {
		t.Fatalf("SetOrgLocked: %v", err)
	}
	w.tmpl = mustProject(t, s, directory.Project{Owner: gyoza, Name: "template"})
	w.cloning = mustProject(t, s, directory.Project{Owner: userOwner("ann"), Name: "clone", State: directory.ProjectCloning, ClonedFrom: w.tmpl.ID})
	return w
}

func projectOwner(p directory.Project) storage.Owner {
	return storage.Owner{Kind: storage.OwnerProject, ID: p.ID}
}

func TestStorageAuthorizer(t *testing.T) {
	w := newAuthzWorld(t)
	authz := manager.NewStorageAuthorizer(w.store)
	acmeLib := orgOwner(w.acme.ID)

	tests := []struct {
		name      string
		actor     string
		owner     storage.Owner
		projectID string
		read      bool
		write     bool
	}{
		{"own library", "ann", userOwner("ann"), "", true, true},
		{"someone else's library", "bob", userOwner("ann"), "", false, false},
		{"org library, admin", "ann", acmeLib, "", true, true},
		{"org library, member", "mem", acmeLib, "", true, true},
		{"org library, other org", "eve", acmeLib, "", false, false},
		{"org library, no org", "bob", acmeLib, "", false, false},

		// Project-owned files: storage passes the project as projectID too.
		{"project, owner", "ann", projectOwner(w.annP), w.annP.ID, true, true},
		{"project, viewer share", "vic", projectOwner(w.annP), w.annP.ID, true, false},
		{"project, editor share", "edi", projectOwner(w.annP), w.annP.ID, true, true},
		{"project, stranger", "bob", projectOwner(w.annP), w.annP.ID, false, false},
		{"project, org member of the owning org", "mem", projectOwner(w.acmeP), w.acmeP.ID, true, true},
		{"project, viewer share on an org project", "vic", projectOwner(w.acmeP), w.acmeP.ID, true, false},
		{"project, other org", "eve", projectOwner(w.acmeP), w.acmeP.ID, false, false},
		{"locked project, member", "mem", projectOwner(w.lockedP), w.lockedP.ID, true, true},
		{"locked project, suspended share", "bob", projectOwner(w.lockedP), w.lockedP.ID, false, false},
		{"template", "bob", projectOwner(w.tmpl), w.tmpl.ID, true, false},
		{"clone in progress, even its owner", "ann", projectOwner(w.cloning), w.cloning.ID, false, false},
		{"missing project", "ann", storage.Owner{Kind: storage.OwnerProject, ID: uuid.NewString()}, "", false, false},

		{"gyoza's shared files", "bob", gyoza, "", true, false},

		// Files owned by someone else but associated with a project: reading
		// the project lets you read them; writing still needs the owner.
		{"associated, viewer of the project", "vic", acmeLib, w.annP.ID, true, false},
		{"associated, editor of the project", "edi", acmeLib, w.annP.ID, true, false},
		{"associated, stranger", "bob", acmeLib, w.annP.ID, false, false},
		{"associated with a locked project, suspended", "bob", acmeLib, w.lockedP.ID, false, false},
		{"associated with a template", "bob", acmeLib, w.tmpl.ID, true, false},

		{"unknown user, own-looking library", "ghost", userOwner("ghost"), "", false, false},
		{"no actor", "", gyoza, "", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			check := func(verb string, err error, want bool) {
				switch {
				case want && err != nil:
					t.Errorf("%s = %v, want allowed", verb, err)
				case !want && !errors.Is(err, storage.ErrForbidden):
					t.Errorf("%s = %v, want ErrForbidden", verb, err)
				}
			}
			check("CanRead", authz.CanRead(t.Context(), tt.actor, tt.owner, tt.projectID), tt.read)
			check("CanWrite", authz.CanWrite(t.Context(), tt.actor, tt.owner, tt.projectID), tt.write)
		})
	}
}

// failingMembership makes GetMembership fail, as a database outage would.
type failingMembership struct{ *directory.MemoryStore }

func (failingMembership) GetMembership(context.Context, string) (directory.Member, error) {
	return directory.Member{}, errors.New("connection reset")
}

// A store failure is reported as such, never mistaken for "forbidden" (which
// would tell a caller the user has no access when we simply don't know).
func TestStorageAuthorizerStoreFailure(t *testing.T) {
	w := newAuthzWorld(t)
	authz := manager.NewStorageAuthorizer(failingMembership{w.store})
	err := authz.CanWrite(t.Context(), "mem", orgOwner(w.acme.ID), "")
	if err == nil || errors.Is(err, storage.ErrForbidden) {
		t.Errorf("CanWrite during a store failure = %v; want the failure, not ErrForbidden", err)
	}
}

// End to end: storage with iam's authorizer instead of AllowAll. An org
// member uploads into the org's project; a viewer can download it but not add
// a version; a stranger can't even see it.
func TestStorageAuthorizerWithStorage(t *testing.T) {
	ctx := t.Context()
	w := newAuthzWorld(t)
	m, err := storage.NewMemory(manager.NewStorageAuthorizer(w.store), storage.Config{Bucket: "gyoza-test"})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	png := append([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"), make([]byte, 48)...)
	ticket, err := m.BeginUpload(ctx, "mem", storage.UploadRequest{
		Owner: projectOwner(w.acmeP), Name: "Logo", ContentType: "image/png", SizeBytes: int64(len(png)),
	})
	if err != nil {
		t.Fatalf("member BeginUpload: %v", err)
	}
	if err := m.Upload(ticket.URL, "image/png", png); err != nil {
		t.Fatalf("browser upload: %v", err)
	}
	asset, err := m.CompleteUpload(ctx, "mem", ticket.UploadID)
	if err != nil {
		t.Fatalf("member CompleteUpload: %v", err)
	}

	if _, err := m.DownloadURL(ctx, "vic", asset.ID, 0); err != nil {
		t.Errorf("viewer download: %v", err)
	}
	if _, err := m.BeginUpload(ctx, "vic", storage.UploadRequest{AssetID: asset.ID, ContentType: "image/png", SizeBytes: 64}); !errors.Is(err, storage.ErrForbidden) {
		t.Errorf("viewer new version = %v, want ErrForbidden", err)
	}
	if _, err := m.GetAsset(ctx, "bob", asset.ID); !errors.Is(err, storage.ErrForbidden) {
		t.Errorf("stranger GetAsset = %v, want ErrForbidden", err)
	}
}

// failingShares makes GetShare fail, as a database outage would.
type failingShares struct{ *directory.MemoryStore }

func (failingShares) GetShare(context.Context, string, string) (directory.Share, error) {
	return directory.Share{}, errors.New("connection reset")
}

func TestStorageAuthorizerShareFailure(t *testing.T) {
	w := newAuthzWorld(t)
	authz := manager.NewStorageAuthorizer(failingShares{w.store})
	err := authz.CanRead(t.Context(), "vic", projectOwner(w.annP), w.annP.ID)
	if err == nil || errors.Is(err, storage.ErrForbidden) {
		t.Errorf("CanRead during a share lookup failure = %v; want the failure, not ErrForbidden", err)
	}
}
