package grpcapi_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"reflect"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	pb "gyoza/contracts/gen/go/gyoza/iam/v1"
	"gyoza/services/iam/internal/directory"
	"gyoza/services/iam/internal/grpcapi"
	"gyoza/services/iam/internal/manager"
	"gyoza/services/storage"
)

// dial serves register's services over an in-memory connection and returns a
// connection to them.
func dial(t *testing.T, register func(*grpc.Server)) *grpc.ClientConn {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	register(srv)
	go srv.Serve(lis)
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient("passthrough:///bufconn",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func newManager(t *testing.T) (*manager.Manager, *directory.MemoryStore) {
	t.Helper()
	s := directory.NewMemoryStore()
	files, err := storage.NewMemory(manager.NewStorageAuthorizer(s), storage.Config{Bucket: "gyoza-test"})
	if err != nil {
		t.Fatalf("storage.NewMemory: %v", err)
	}
	m, err := manager.NewManager(s, files, manager.NoIdentityProvider{}, manager.Config{})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return m, s
}

// must fails the test on err, returning v otherwise.
func must[T any](t *testing.T, what string) func(T, error) T {
	return func(v T, err error) T {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		return v
	}
}

func same(t *testing.T, what string, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s over gRPC:\n got  %+v\n want %+v", what, got, want)
	}
}

// TestServiceOverGRPC calls every RPC through a real client and server, and
// compares each answer with what the Manager holds in-process: so every
// field survives the trip both ways.
func TestServiceOverGRPC(t *testing.T) {
	ctx := t.Context()
	m, _ := newManager(t)
	c := grpcapi.NewClient(dial(t, func(s *grpc.Server) { pb.RegisterIAMServiceServer(s, grpcapi.NewServer(m)) }))

	// Users.
	in, err := c.SignIn(ctx, directory.Identity{UserID: "ann", Email: "ann@example.com", EmailVerified: true, Name: "Ann", ProviderAvatarURL: "https://example.com/a.png"})
	if err != nil || !in.Created {
		t.Fatalf("SignIn = %+v, %v", in, err)
	}
	same(t, "SignIn user", in.User, must[manager.Me](t, "GetMe")(m.GetMe(ctx, "ann")).User)
	must[manager.SignInResult](t, "SignIn bob")(c.SignIn(ctx, directory.Identity{UserID: "bob", Email: "bob@example.com", EmailVerified: true}))
	must[manager.SignInResult](t, "SignIn cat")(c.SignIn(ctx, directory.Identity{UserID: "cat", Email: "cat@example.com", EmailVerified: true}))

	// Orgs, invites, members.
	org, admin, err := c.CreateOrg(ctx, "ann", "Acme")
	if err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}
	same(t, "CreateOrg", org, must[directory.Org](t, "GetOrg")(m.GetOrg(ctx, "ann", org.ID)))
	same(t, "GetOrg", must[directory.Org](t, "GetOrg")(c.GetOrg(ctx, "ann", org.ID)), org)
	org = must[directory.Org](t, "RenameOrg")(c.RenameOrg(ctx, "ann", org.ID, "Acme Inc"))
	same(t, "RenameOrg", org, must[directory.Org](t, "GetOrg")(m.GetOrg(ctx, "ann", org.ID)))

	inv := must[directory.Invite](t, "InviteMember")(c.InviteMember(ctx, "ann", org.ID, "bob@example.com", directory.RoleAdmin))
	same(t, "ListOrgInvites", must[[]directory.Invite](t, "ListOrgInvites")(c.ListOrgInvites(ctx, "ann", org.ID)), []directory.Invite{inv})
	same(t, "ListMyInvites", must[[]directory.Invite](t, "ListMyInvites")(c.ListMyInvites(ctx, "bob")), []directory.Invite{inv})
	bobMember := must[directory.Member](t, "AcceptInvite")(c.AcceptInvite(ctx, "bob", inv.ID))
	same(t, "ListMembers", must[[]directory.Member](t, "ListMembers")(c.ListMembers(ctx, "ann", org.ID)), []directory.Member{admin, bobMember})
	bobMember = must[directory.Member](t, "SetRole")(c.SetRole(ctx, "ann", org.ID, "bob", directory.RoleMember))
	if bobMember.Role != directory.RoleMember {
		t.Errorf("SetRole = %+v, want member", bobMember)
	}
	me := must[manager.Me](t, "GetMe")(c.GetMe(ctx, "bob"))
	same(t, "GetMe", me, must[manager.Me](t, "GetMe")(m.GetMe(ctx, "bob")))

	catInv := must[directory.Invite](t, "InviteMember cat")(c.InviteMember(ctx, "ann", org.ID, "cat@example.com", directory.RoleMember))
	if err := c.DeclineInvite(ctx, "cat", catInv.ID); err != nil {
		t.Errorf("DeclineInvite: %v", err)
	}
	catInv = must[directory.Invite](t, "InviteMember cat")(c.InviteMember(ctx, "ann", org.ID, "cat@example.com", directory.RoleMember))
	if err := c.RevokeInvite(ctx, "ann", catInv.ID); err != nil {
		t.Errorf("RevokeInvite: %v", err)
	}

	// Projects and sharing.
	annP := must[directory.Project](t, "CreateProject")(c.CreateProject(ctx, "ann", storage.Owner{Kind: storage.OwnerUser, ID: "ann"}, "Poster", false))
	orgP := must[directory.Project](t, "CreateProject org")(c.CreateProject(ctx, "bob", storage.Owner{Kind: storage.OwnerOrg, ID: org.ID}, "Flyer", true))
	same(t, "GetProject", must[manager.ProjectView](t, "GetProject")(c.GetProject(ctx, "ann", orgP.ID)),
		must[manager.ProjectView](t, "GetProject")(m.GetProject(ctx, "ann", orgP.ID)))
	annP = must[directory.Project](t, "RenameProject")(c.RenameProject(ctx, "ann", annP.ID, "Big poster"))
	orgP = must[directory.Project](t, "SetOrgLocked")(c.SetOrgLocked(ctx, "ann", orgP.ID, false))
	share := must[directory.Share](t, "ShareProject")(c.ShareProject(ctx, "ann", annP.ID, "cat@example.com", directory.AccessViewer))
	if share.Access != directory.AccessViewer {
		t.Errorf("ShareProject = %+v, want viewer", share)
	}
	same(t, "ListShares", must[[]directory.Share](t, "ListShares")(c.ListShares(ctx, "ann", annP.ID)), []directory.Share{share})
	page := must[directory.ProjectPage](t, "ListMyProjects")(c.ListMyProjects(ctx, "cat", 0, ""))
	same(t, "ListMyProjects", page, must[directory.ProjectPage](t, "ListMyProjects")(m.ListMyProjects(ctx, "cat", 0, "")))
	if err := c.Unshare(ctx, "cat", annP.ID, "cat"); err != nil {
		t.Errorf("Unshare: %v", err)
	}
	annP = must[directory.Project](t, "MoveProjectToOrg")(c.MoveProjectToOrg(ctx, "ann", annP.ID))
	same(t, "MoveProjectToOrg", annP, must[manager.ProjectView](t, "GetProject")(m.GetProject(ctx, "ann", annP.ID)).Project)

	tmpl := must[directory.Project](t, "CreateTemplate")(c.CreateTemplate(ctx, "Template"))
	same(t, "ListTemplates", must[directory.ProjectPage](t, "ListTemplates")(c.ListTemplates(ctx, "cat", 1, "")),
		must[directory.ProjectPage](t, "ListTemplates")(m.ListTemplates(ctx, "cat", 1, "")))

	// Deletions.
	for name, err := range map[string]error{
		"DeleteTemplate": c.DeleteTemplate(ctx, tmpl.ID),
		"DeleteProject":  c.DeleteProject(ctx, "ann", orgP.ID),
	} {
		if err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if n, err := c.RemoveMember(ctx, "ann", org.ID, "bob"); err != nil || n != 1 {
		t.Errorf("RemoveMember = %d, %v; want 1 left", n, err)
	}
	if n, err := c.LeaveOrg(ctx, "ann"); err != nil || n != 0 {
		t.Errorf("LeaveOrg = %d, %v; want 0 left", n, err)
	}
	other, _, _ := m.CreateOrg(ctx, "bob", "Beta")
	if err := c.DeleteOrg(ctx, "bob", other.ID); err != nil {
		t.Errorf("DeleteOrg: %v", err)
	}
	if err := c.DeleteAccount(ctx, "cat"); err != nil {
		t.Errorf("DeleteAccount: %v", err)
	}
	if _, err := m.GetMe(ctx, "cat"); !errors.Is(err, manager.ErrForbidden) {
		t.Errorf("deleted account still there: %v", err)
	}
}

// failing is a Service whose every call fails with err (the embedded nil
// interface is never reached: GetMe is the only call made).
type failing struct {
	manager.Service
	err error
}

func (f failing) GetMe(context.Context, string) (manager.Me, error) { return manager.Me{}, f.err }

// TestErrorsOverGRPC: every sentinel that may cross the API comes back as
// itself (errors.Is), whatever it was wrapped in; anything else comes back
// as an opaque internal error.
func TestErrorsOverGRPC(t *testing.T) {
	sentinels := []error{
		manager.ErrForbidden, directory.ErrUserNotFound, directory.ErrOrgNotFound, directory.ErrInviteNotFound,
		directory.ErrProjectNotFound, directory.ErrShareNotFound, directory.ErrInvalidUser, directory.ErrInvalidOrg,
		directory.ErrInvalidInvite, directory.ErrInvalidProject, directory.ErrInvalidShare, directory.ErrInvalidQuery,
		directory.ErrEmailTaken, directory.ErrAlreadyMember, directory.ErrNotMember, directory.ErrLastAdmin,
		directory.ErrInviteExpired, directory.ErrNotDeleting,
	}
	for _, want := range sentinels {
		t.Run(want.Error(), func(t *testing.T) {
			sent := fmt.Errorf("context: %w", want)
			c := grpcapi.NewClient(dial(t, func(s *grpc.Server) { pb.RegisterIAMServiceServer(s, grpcapi.NewServer(failing{err: sent})) }))
			_, err := c.GetMe(t.Context(), "ann")
			if !errors.Is(err, want) {
				t.Errorf("err = %v, want errors.Is %v", err, want)
			}
			for _, other := range sentinels {
				if other != want && errors.Is(err, other) {
					t.Errorf("err also matches %v", other)
				}
			}
		})
	}

	t.Run("Unexpected", func(t *testing.T) {
		c := grpcapi.NewClient(dial(t, func(s *grpc.Server) {
			pb.RegisterIAMServiceServer(s, grpcapi.NewServer(failing{err: errors.New("pq: password authentication failed")}))
		}))
		_, err := c.GetMe(t.Context(), "ann")
		if status.Code(err) != codes.Internal || strings.Contains(err.Error(), "password") {
			t.Errorf("err = %v, want an opaque internal error", err)
		}
	})
}

// failingShares makes GetShare fail, as a database outage would.
type failingShares struct{ *directory.MemoryStore }

func (failingShares) GetShare(context.Context, string, string) (directory.Share, error) {
	return directory.Share{}, errors.New("connection reset")
}

// TestAuthzOverGRPC: storage asking a remote iam gets the same answers as
// asking in-process: allowed, refused (storage.ErrForbidden), or a failure
// that must not pass for a refusal.
func TestAuthzOverGRPC(t *testing.T) {
	ctx := t.Context()
	m, s := newManager(t)
	for _, name := range []string{"ann", "bob"} {
		if _, err := m.SignIn(ctx, directory.Identity{UserID: name, Email: name + "@example.com", EmailVerified: true}); err != nil {
			t.Fatalf("SignIn: %v", err)
		}
	}
	p, err := m.CreateProject(ctx, "ann", storage.Owner{Kind: storage.OwnerUser, ID: "ann"}, "Poster", false)
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if _, err := m.ShareProject(ctx, "ann", p.ID, "bob@example.com", directory.AccessViewer); err != nil {
		t.Fatalf("ShareProject: %v", err)
	}
	files := storage.Owner{Kind: storage.OwnerProject, ID: p.ID}

	authz := grpcapi.NewAuthzClient(dial(t, func(srv *grpc.Server) {
		pb.RegisterAuthzServiceServer(srv, grpcapi.NewAuthzServer(manager.NewStorageAuthorizer(s)))
	}))
	if err := authz.CanWrite(ctx, "ann", files, ""); err != nil {
		t.Errorf("owner CanWrite = %v, want allowed", err)
	}
	if err := authz.CanRead(ctx, "bob", files, ""); err != nil {
		t.Errorf("viewer CanRead = %v, want allowed", err)
	}
	if err := authz.CanWrite(ctx, "bob", files, ""); !errors.Is(err, storage.ErrForbidden) {
		t.Errorf("viewer CanWrite = %v, want storage.ErrForbidden", err)
	}
	if err := authz.CanRead(ctx, "stranger", files, ""); !errors.Is(err, storage.ErrForbidden) {
		t.Errorf("stranger CanRead = %v, want storage.ErrForbidden", err)
	}

	if got, err := authz.ProjectOwner(ctx, p.ID); err != nil || got != p.Owner {
		t.Errorf("ProjectOwner = %v, %v; want %v", got, err, p.Owner)
	}
	if _, err := authz.ProjectOwner(ctx, "no-such-project"); !errors.Is(err, storage.ErrForbidden) {
		t.Errorf("ProjectOwner of an unknown project = %v, want storage.ErrForbidden", err)
	}

	broken := grpcapi.NewAuthzClient(dial(t, func(srv *grpc.Server) {
		pb.RegisterAuthzServiceServer(srv, grpcapi.NewAuthzServer(manager.NewStorageAuthorizer(failingShares{s})))
	}))
	if err := broken.CanRead(ctx, "bob", files, ""); err == nil || errors.Is(err, storage.ErrForbidden) {
		t.Errorf("CanRead during an outage = %v; want the failure, not ErrForbidden", err)
	}
}

// CloneProject over gRPC returns the ready clone, with its state and source,
// and a refusal as ErrForbidden.
func TestCloneProjectOverGRPC(t *testing.T) {
	ctx := t.Context()
	m, _ := newManager(t)
	c := grpcapi.NewClient(dial(t, func(s *grpc.Server) { pb.RegisterIAMServiceServer(s, grpcapi.NewServer(m)) }))
	for _, name := range []string{"ann", "bob"} {
		if _, err := m.SignIn(ctx, directory.Identity{UserID: name, Email: name + "@example.com", EmailVerified: true}); err != nil {
			t.Fatalf("SignIn: %v", err)
		}
	}
	src, err := m.CreateProject(ctx, "ann", storage.Owner{Kind: storage.OwnerUser, ID: "ann"}, "Poster", false)
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	clone, err := c.CloneProject(ctx, "ann", src.ID, storage.Owner{Kind: storage.OwnerUser, ID: "ann"})
	if err != nil || clone.State != directory.ProjectReady || clone.ClonedFrom != src.ID || clone.Name != "Poster" {
		t.Errorf("CloneProject over gRPC = %+v, %v", clone, err)
	}
	// The target crosses the wire: into ann's org this time.
	acme, _, err := m.CreateOrg(ctx, "ann", "Acme")
	if err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}
	org := storage.Owner{Kind: storage.OwnerOrg, ID: acme.ID}
	if oc, err := c.CloneProject(ctx, "ann", src.ID, org); err != nil || oc.Owner != org {
		t.Errorf("CloneProject into acme over gRPC = %+v, %v", oc, err)
	}
	if _, err := c.CloneProject(ctx, "bob", src.ID, storage.Owner{Kind: storage.OwnerUser, ID: "bob"}); !errors.Is(err, manager.ErrForbidden) {
		t.Errorf("CloneProject of an unreadable project over gRPC = %v, want ErrForbidden", err)
	}
}
