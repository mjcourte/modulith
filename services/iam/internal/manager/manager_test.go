package manager_test

import (
	"errors"
	"github.com/mjcourte/modulith/services/iam/internal/directory"
	"github.com/mjcourte/modulith/services/iam/internal/manager"
	"github.com/mjcourte/modulith/services/storage"
	"testing"
	"time"

	"github.com/google/uuid"
)

func newManager(t *testing.T) (*manager.Manager, *directory.MemoryStore) {
	t.Helper()
	m, s, _ := newManagerWithFiles(t)
	return m, s
}

// newManagerWithFiles also returns the storage the Manager's cascades call.
func newManagerWithFiles(t *testing.T) (*manager.Manager, *directory.MemoryStore, *storage.Memory) {
	t.Helper()
	s := directory.NewMemoryStore()
	files := newFilesFor(t, s)
	m, err := manager.NewManager(s, files, &recordingDocs{}, manager.NoIdentityProvider{}, manager.Config{})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return m, s, files
}

// signIn signs a user in through the Manager, as the gateway's hook would,
// with a verified email name@example.com.
func signIn(t *testing.T, m *manager.Manager, name string) directory.User {
	t.Helper()
	r, err := m.SignIn(t.Context(), directory.Identity{UserID: name, Email: name + "@example.com", EmailVerified: true, Name: name})
	if err != nil {
		t.Fatalf("SignIn(%s): %v", name, err)
	}
	return r.User
}

func managerOrg(t *testing.T, m *manager.Manager, actor, name string) directory.Org {
	t.Helper()
	o, _, err := m.CreateOrg(t.Context(), actor, name)
	if err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}
	return o
}

// join invites and accepts, the way someone really joins.
func join(t *testing.T, m *manager.Manager, admin, orgID string, u directory.User, role directory.Role) {
	t.Helper()
	inv, err := m.InviteMember(t.Context(), admin, orgID, u.Email, role)
	if err != nil {
		t.Fatalf("InviteMember: %v", err)
	}
	if _, err := m.AcceptInvite(t.Context(), u.ID, inv.ID); err != nil {
		t.Fatalf("AcceptInvite: %v", err)
	}
}

func isForbidden(err error) bool { return errors.Is(err, manager.ErrForbidden) }

func TestNewManagerConfig(t *testing.T) {
	m, _ := newManager(t)
	if got := m.Config().InviteTTL; got != manager.DefaultInviteTTL {
		t.Errorf("InviteTTL = %s, want the default %s", got, manager.DefaultInviteTTL)
	}
	if _, err := manager.NewManager(directory.NewMemoryStore(), newFilesFor(t, directory.NewMemoryStore()), &recordingDocs{}, manager.NoIdentityProvider{}, manager.Config{InviteTTL: -time.Hour}); err == nil {
		t.Error("negative InviteTTL accepted")
	}
	if _, err := manager.NewManager(nil, newFilesFor(t, directory.NewMemoryStore()), &recordingDocs{}, manager.NoIdentityProvider{}, manager.Config{}); err == nil {
		t.Error("NewManager without a store succeeded")
	}
	if _, err := manager.NewManager(directory.NewMemoryStore(), nil, &recordingDocs{}, manager.NoIdentityProvider{}, manager.Config{}); err == nil {
		t.Error("NewManager without storage succeeded")
	}
	if _, err := manager.NewManager(directory.NewMemoryStore(), newFilesFor(t, directory.NewMemoryStore()), nil, manager.NoIdentityProvider{}, manager.Config{}); err == nil {
		t.Error("NewManager without documents succeeded")
	}
	if _, err := manager.NewManager(directory.NewMemoryStore(), newFilesFor(t, directory.NewMemoryStore()), &recordingDocs{}, nil, manager.Config{}); err == nil {
		t.Error("NewManager without an identity provider succeeded")
	}
}

func TestSignIn(t *testing.T) {
	ctx := t.Context()
	m, _ := newManager(t)
	ann := signIn(t, m, "ann")
	acme := managerOrg(t, m, ann.ID, "Acme")
	if _, err := m.InviteMember(ctx, ann.ID, acme.ID, "Bob@Example.com", directory.RoleMember); err != nil {
		t.Fatalf("InviteMember: %v", err)
	}

	first, err := m.SignIn(ctx, directory.Identity{UserID: "bob", Email: "bob@example.com", EmailVerified: true})
	if err != nil || !first.Created || len(first.Invites) != 1 || first.Invites[0].OrgID != acme.ID {
		t.Fatalf("bob's first sign-in = %+v, %v; want created, with acme's invite", first, err)
	}
	again, err := m.SignIn(ctx, directory.Identity{UserID: "bob", Email: "bob@example.com", EmailVerified: true})
	if err != nil || again.Created || len(again.Invites) != 1 {
		t.Errorf("bob's second sign-in = %+v, %v; want not created, invite still there", again, err)
	}

	// An unverified address doesn't get the invites sent to it.
	if _, err := m.InviteMember(ctx, ann.ID, acme.ID, "cat@example.com", directory.RoleMember); err != nil {
		t.Fatalf("InviteMember: %v", err)
	}
	unverified, err := m.SignIn(ctx, directory.Identity{UserID: "cat", Email: "cat@example.com"})
	if err != nil || len(unverified.Invites) != 0 {
		t.Errorf("unverified sign-in = %+v, %v; want no invites", unverified, err)
	}
	if _, err := m.SignIn(ctx, directory.Identity{UserID: "dan", Email: "not an email"}); !errors.Is(err, directory.ErrInvalidUser) {
		t.Errorf("bad identity = %v, want ErrInvalidUser", err)
	}
}

func TestGetMe(t *testing.T) {
	ctx := t.Context()
	m, _ := newManager(t)
	ann := signIn(t, m, "ann")
	me, err := m.GetMe(ctx, ann.ID)
	if err != nil || me.User.ID != ann.ID || me.Org != nil || me.Membership != nil {
		t.Fatalf("GetMe without an org = %+v, %v", me, err)
	}
	acme := managerOrg(t, m, ann.ID, "Acme")
	me, err = m.GetMe(ctx, ann.ID)
	if err != nil || me.Org == nil || me.Org.ID != acme.ID || me.Membership.Role != directory.RoleAdmin {
		t.Errorf("GetMe with an org = %+v, %v; want acme, admin", me, err)
	}
	for _, actor := range []string{"ghost", ""} {
		if _, err := m.GetMe(ctx, actor); !isForbidden(err) {
			t.Errorf("GetMe(%q) = %v, want ErrForbidden", actor, err)
		}
	}
}

func TestManagerOrgs(t *testing.T) {
	ctx := t.Context()
	m, _ := newManager(t)
	ann, bob, eve := signIn(t, m, "ann"), signIn(t, m, "bob"), signIn(t, m, "eve")

	acme, admin, err := m.CreateOrg(ctx, ann.ID, "Acme")
	if err != nil || admin.Role != directory.RoleAdmin || admin.UserID != ann.ID {
		t.Fatalf("CreateOrg = %+v, %v", admin, err)
	}
	if _, _, err := m.CreateOrg(ctx, ann.ID, "Second"); !errors.Is(err, directory.ErrAlreadyMember) {
		t.Errorf("second org = %v, want ErrAlreadyMember", err)
	}
	if _, _, err := m.CreateOrg(ctx, "ghost", "Ghost Co"); !isForbidden(err) {
		t.Errorf("unknown user creating an org = %v, want ErrForbidden", err)
	}
	join(t, m, ann.ID, acme.ID, bob, directory.RoleMember)
	managerOrg(t, m, eve.ID, "Beta")

	// Members may look; others may not, whether or not the org exists.
	if _, err := m.GetOrg(ctx, bob.ID, acme.ID); err != nil {
		t.Errorf("member GetOrg: %v", err)
	}
	if ms, err := m.ListMembers(ctx, bob.ID, acme.ID); err != nil || len(ms) != 2 {
		t.Errorf("member ListMembers = %d, %v", len(ms), err)
	}
	cat := signIn(t, m, "cat") // in no org at all
	if _, err := m.GetOrg(ctx, cat.ID, acme.ID); !isForbidden(err) {
		t.Errorf("GetOrg by someone in no org = %v, want ErrForbidden", err)
	}
	for _, orgID := range []string{acme.ID, uuid.NewString()} {
		if _, err := m.GetOrg(ctx, eve.ID, orgID); !isForbidden(err) {
			t.Errorf("outsider GetOrg(%s) = %v, want ErrForbidden", orgID, err)
		}
		if _, err := m.ListMembers(ctx, eve.ID, orgID); !isForbidden(err) {
			t.Errorf("outsider ListMembers(%s) = %v, want ErrForbidden", orgID, err)
		}
	}

	// Admins change things; members don't.
	if _, err := m.RenameOrg(ctx, bob.ID, acme.ID, "Bob Co"); !isForbidden(err) {
		t.Errorf("member RenameOrg = %v, want ErrForbidden", err)
	}
	if o, err := m.RenameOrg(ctx, ann.ID, acme.ID, "Acme Ltd"); err != nil || o.Name != "Acme Ltd" {
		t.Errorf("admin RenameOrg = %+v, %v", o, err)
	}
}

func TestManagerInvites(t *testing.T) {
	ctx := t.Context()
	m, _ := newManager(t)
	at := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	manager.SetClock(m, func() time.Time { return at })
	ann, bob, eve := signIn(t, m, "ann"), signIn(t, m, "bob"), signIn(t, m, "eve")
	acme := managerOrg(t, m, ann.ID, "Acme")
	managerOrg(t, m, eve.ID, "Beta")

	inv, err := m.InviteMember(ctx, ann.ID, acme.ID, "BOB@example.com", directory.RoleMember)
	if err != nil {
		t.Fatalf("InviteMember: %v", err)
	}
	if inv.Email != bob.Email || inv.InvitedBy != ann.ID || !inv.ExpiresAt.Equal(at.Add(manager.DefaultInviteTTL)) {
		t.Errorf("invite = %+v; want bob's address, ann as inviter, expiry now + TTL", inv)
	}
	// Someone in another org can be invited (they accept after leaving it).
	if _, err := m.InviteMember(ctx, ann.ID, acme.ID, eve.Email, directory.RoleMember); err != nil {
		t.Errorf("inviting a member of another org: %v", err)
	}
	if _, err := m.InviteMember(ctx, eve.ID, acme.ID, "x@example.com", directory.RoleMember); !isForbidden(err) {
		t.Errorf("outsider invites = %v, want ErrForbidden", err)
	}
	if invs, err := m.ListOrgInvites(ctx, ann.ID, acme.ID); err != nil || len(invs) != 2 {
		t.Errorf("admin ListOrgInvites = %d, %v; want 2", len(invs), err)
	}

	// Only bob can see and act on bob's invite.
	if mine, err := m.ListMyInvites(ctx, bob.ID); err != nil || len(mine) != 1 || mine[0].ID != inv.ID {
		t.Errorf("bob's invites = %+v, %v", mine, err)
	}
	if _, err := m.AcceptInvite(ctx, eve.ID, inv.ID); !errors.Is(err, directory.ErrInviteNotFound) {
		t.Errorf("someone else accepting = %v, want ErrInviteNotFound", err)
	}
	if err := m.DeclineInvite(ctx, eve.ID, inv.ID); !errors.Is(err, directory.ErrInviteNotFound) {
		t.Errorf("someone else declining = %v, want ErrInviteNotFound", err)
	}
	mem, err := m.AcceptInvite(ctx, bob.ID, inv.ID)
	if err != nil || mem.OrgID != acme.ID || mem.Role != directory.RoleMember {
		t.Fatalf("bob accepts = %+v, %v", mem, err)
	}

	// Members can't invite or see the org's invites; already-members can't be invited.
	if _, err := m.InviteMember(ctx, bob.ID, acme.ID, "x@example.com", directory.RoleMember); !isForbidden(err) {
		t.Errorf("member invites = %v, want ErrForbidden", err)
	}
	if _, err := m.ListOrgInvites(ctx, bob.ID, acme.ID); !isForbidden(err) {
		t.Errorf("member ListOrgInvites = %v, want ErrForbidden", err)
	}
	if _, err := m.InviteMember(ctx, ann.ID, acme.ID, bob.Email, directory.RoleAdmin); !errors.Is(err, directory.ErrAlreadyMember) {
		t.Errorf("inviting a member = %v, want ErrAlreadyMember", err)
	}

	// Eve is in beta: acme's invite waits until she leaves.
	eveInv, _ := m.ListMyInvites(ctx, eve.ID)
	if len(eveInv) != 1 {
		t.Fatalf("eve's invites = %+v", eveInv)
	}
	if _, err := m.AcceptInvite(ctx, eve.ID, eveInv[0].ID); !errors.Is(err, directory.ErrAlreadyMember) {
		t.Errorf("accepting while in another org = %v, want ErrAlreadyMember", err)
	}
	if err := m.DeclineInvite(ctx, eve.ID, eveInv[0].ID); err != nil {
		t.Errorf("eve declines: %v", err)
	}

	// Revoking: admins of the invite's org only; unknown invites look the same.
	toCat, _ := m.InviteMember(ctx, ann.ID, acme.ID, "cat@example.com", directory.RoleMember)
	if err := m.RevokeInvite(ctx, eve.ID, toCat.ID); !isForbidden(err) {
		t.Errorf("beta's admin revoking acme's invite = %v, want ErrForbidden", err)
	}
	if err := m.RevokeInvite(ctx, eve.ID, uuid.NewString()); !isForbidden(err) {
		t.Errorf("revoking a missing invite = %v, want ErrForbidden", err)
	}
	if err := m.RevokeInvite(ctx, ann.ID, toCat.ID); err != nil {
		t.Errorf("admin revokes: %v", err)
	}
	if invs, _ := m.ListOrgInvites(ctx, ann.ID, acme.ID); len(invs) != 0 {
		t.Errorf("invites left = %+v, want none", invs)
	}
}

// An unverified address can't claim invites sent to it.
func TestManagerInvitesNeedVerifiedEmail(t *testing.T) {
	ctx := t.Context()
	m, _ := newManager(t)
	ann := signIn(t, m, "ann")
	acme := managerOrg(t, m, ann.ID, "Acme")
	inv, err := m.InviteMember(ctx, ann.ID, acme.ID, "cat@example.com", directory.RoleMember)
	if err != nil {
		t.Fatalf("InviteMember: %v", err)
	}
	if _, err := m.SignIn(ctx, directory.Identity{UserID: "cat", Email: "cat@example.com"}); err != nil {
		t.Fatalf("SignIn: %v", err)
	}
	if mine, err := m.ListMyInvites(ctx, "cat"); err != nil || len(mine) != 0 {
		t.Errorf("unverified ListMyInvites = %+v, %v; want none", mine, err)
	}
	if _, err := m.AcceptInvite(ctx, "cat", inv.ID); !isForbidden(err) {
		t.Errorf("unverified AcceptInvite = %v, want ErrForbidden", err)
	}
}

func TestManagerRolesAndLeaving(t *testing.T) {
	ctx := t.Context()
	m, _ := newManager(t)
	ann, bob, cat := signIn(t, m, "ann"), signIn(t, m, "bob"), signIn(t, m, "cat")
	acme := managerOrg(t, m, ann.ID, "Acme")
	join(t, m, ann.ID, acme.ID, bob, directory.RoleMember)
	join(t, m, ann.ID, acme.ID, cat, directory.RoleMember)

	if _, err := m.SetRole(ctx, bob.ID, acme.ID, cat.ID, directory.RoleAdmin); !isForbidden(err) {
		t.Errorf("member SetRole = %v, want ErrForbidden", err)
	}
	if _, err := m.RemoveMember(ctx, bob.ID, acme.ID, cat.ID); !isForbidden(err) {
		t.Errorf("member RemoveMember = %v, want ErrForbidden", err)
	}
	// The only admin can't leave others behind without an admin.
	if _, err := m.LeaveOrg(ctx, ann.ID); !errors.Is(err, directory.ErrLastAdmin) {
		t.Errorf("last admin leaves = %v, want ErrLastAdmin", err)
	}
	if mem, err := m.SetRole(ctx, ann.ID, acme.ID, bob.ID, directory.RoleAdmin); err != nil || mem.Role != directory.RoleAdmin {
		t.Fatalf("admin promotes bob = %+v, %v", mem, err)
	}
	if n, err := m.RemoveMember(ctx, bob.ID, acme.ID, cat.ID); err != nil || n != 2 {
		t.Errorf("new admin removes cat = %d, %v; want 2 left", n, err)
	}
	if n, err := m.LeaveOrg(ctx, ann.ID); err != nil || n != 1 {
		t.Errorf("ann leaves once bob is admin = %d, %v; want 1 left", n, err)
	}
	if _, err := m.LeaveOrg(ctx, ann.ID); !errors.Is(err, directory.ErrNotMember) {
		t.Errorf("leaving with no org = %v, want ErrNotMember", err)
	}
	if n, err := m.LeaveOrg(ctx, bob.ID); err != nil || n != 0 {
		t.Errorf("the last member leaves = %d, %v; want 0 left", n, err)
	}
	// Ann is free to start her own org now.
	if _, _, err := m.CreateOrg(ctx, ann.ID, "Ann's"); err != nil {
		t.Errorf("CreateOrg after leaving: %v", err)
	}
}
