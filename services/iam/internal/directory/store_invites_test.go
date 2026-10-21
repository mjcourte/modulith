package directory_test

import (
	"errors"
	"github.com/mjcourte/modulith/services/iam/internal/directory"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
)

// inviteIDs lists the IDs of invites, in order.
func inviteIDs(invs []directory.Invite) []string {
	out := make([]string, len(invs))
	for i, inv := range invs {
		out[i] = inv.ID
	}
	return out
}

func testCreateInvite(t *testing.T, s directory.Store) {
	ctx := t.Context()
	o := mustOrg(t, s, "Acme", mustUser(t, s, "ann").ID)
	expires := time.Now().Add(time.Hour).UTC().Truncate(time.Microsecond)
	inv, err := s.CreateInvite(ctx, directory.Invite{OrgID: o.ID, Email: " Bob@Example.com ", Role: directory.RoleMember, InvitedBy: "ann", ExpiresAt: expires})
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	if !isUUIDv7(inv.ID) || inv.CreatedAt.IsZero() || inv.Email != "bob@example.com" || !inv.ExpiresAt.Equal(expires) {
		t.Errorf("invite = %+v; want a UUIDv7, CreatedAt, lowercased email, the given expiry", inv)
	}
	if got, err := s.GetInvite(ctx, inv.ID); err != nil || got.ID != inv.ID || got.Email != inv.Email || got.Role != inv.Role || !got.ExpiresAt.Equal(inv.ExpiresAt) {
		t.Errorf("GetInvite = %+v, %v; want %+v", got, err, inv)
	}
	if _, err := s.GetInvite(ctx, uuid.NewString()); !errors.Is(err, directory.ErrInviteNotFound) {
		t.Errorf("GetInvite(missing) = %v, want ErrInviteNotFound", err)
	}
}

func testCreateInviteRejects(t *testing.T, s directory.Store) {
	ctx := t.Context()
	o := mustOrg(t, s, "Acme", mustUser(t, s, "ann").ID)
	valid := directory.Invite{OrgID: o.ID, Email: "bob@example.com", Role: directory.RoleMember, InvitedBy: "ann", ExpiresAt: time.Now().Add(time.Hour)}

	for name, change := range map[string]func(*directory.Invite){
		"bad email":  func(i *directory.Invite) { i.Email = "bob" },
		"bad role":   func(i *directory.Invite) { i.Role = "owner" },
		"no expiry":  func(i *directory.Invite) { i.ExpiresAt = time.Time{} },
		"bad org ID": func(i *directory.Invite) { i.OrgID = "acme" },
	} {
		bad := valid
		change(&bad)
		if _, err := s.CreateInvite(ctx, bad); !errors.Is(err, directory.ErrInvalidInvite) {
			t.Errorf("%s: %v, want ErrInvalidInvite", name, err)
		}
	}
	missingOrg := valid
	missingOrg.OrgID = uuid.NewString()
	if _, err := s.CreateInvite(ctx, missingOrg); !errors.Is(err, directory.ErrOrgNotFound) {
		t.Errorf("unknown org = %v, want ErrOrgNotFound", err)
	}
	if invs, _ := s.ListOrgInvites(ctx, o.ID); len(invs) != 0 {
		t.Errorf("refused invites were stored: %+v", invs)
	}
}

func testReinviteReplaces(t *testing.T, s directory.Store) {
	ctx := t.Context()
	o := mustOrg(t, s, "Acme", mustUser(t, s, "ann").ID)
	first := mustInvite(t, s, o.ID, "bob@example.com", directory.RoleMember, -time.Hour) // already expired
	// Let time pass, so a wrongly refreshed CreatedAt would show.
	letTimePass()

	again := mustInvite(t, s, o.ID, "BOB@example.com", directory.RoleAdmin, time.Hour)
	if again.ID != first.ID || again.Role != directory.RoleAdmin || !again.ExpiresAt.After(time.Now()) || !again.CreatedAt.Equal(first.CreatedAt) {
		t.Errorf("re-invite = %+v; want the same ID and CreatedAt, the new role and expiry", again)
	}
	if invs, _ := s.ListOrgInvites(ctx, o.ID); len(invs) != 1 || invs[0].Role != directory.RoleAdmin {
		t.Errorf("org invites after re-invite = %+v; want one, refreshed", invs)
	}

	// The same address invited to another org is a separate invite.
	other := mustOrg(t, s, "Other", mustUser(t, s, "eve").ID)
	elsewhere := mustInvite(t, s, other.ID, "bob@example.com", directory.RoleMember, time.Hour)
	if elsewhere.ID == first.ID {
		t.Error("an invite to another org replaced acme's")
	}
}

func testListInvites(t *testing.T, s directory.Store) {
	ctx := t.Context()
	o := mustOrg(t, s, "Acme", mustUser(t, s, "ann").ID)
	other := mustOrg(t, s, "Other", mustUser(t, s, "eve").ID)
	expired := mustInvite(t, s, o.ID, "bob@example.com", directory.RoleMember, -time.Minute)
	cat := mustInvite(t, s, o.ID, "cat@example.com", directory.RoleMember, time.Hour)
	bobElsewhere := mustInvite(t, s, other.ID, "bob@example.com", directory.RoleAdmin, time.Hour)

	// An org's admins see all its invites, expired ones too, newest first.
	invs, err := s.ListOrgInvites(ctx, o.ID)
	if err != nil {
		t.Fatalf("ListOrgInvites: %v", err)
	}
	if got, want := inviteIDs(invs), []string{cat.ID, expired.ID}; !slices.Equal(got, want) {
		t.Errorf("ListOrgInvites = %v, want %v", got, want)
	}
	if _, err := s.ListOrgInvites(ctx, uuid.NewString()); !errors.Is(err, directory.ErrOrgNotFound) {
		t.Errorf("missing org = %v, want ErrOrgNotFound", err)
	}

	// Someone signing in sees their live invites from every org, any case.
	invs, err = s.ListEmailInvites(ctx, "Bob@Example.com")
	if err != nil {
		t.Fatalf("ListEmailInvites: %v", err)
	}
	if got, want := inviteIDs(invs), []string{bobElsewhere.ID}; !slices.Equal(got, want) {
		t.Errorf("ListEmailInvites = %v, want %v (the expired one hidden)", got, want)
	}
	if invs, err := s.ListEmailInvites(ctx, "nobody@example.com"); err != nil || len(invs) != 0 {
		t.Errorf("no invites = %v, %v", invs, err)
	}
}

func testAcceptInvite(t *testing.T, s directory.Store) {
	ctx := t.Context()
	o := mustOrg(t, s, "Acme", mustUser(t, s, "ann").ID)
	bob := mustUser(t, s, "bob")
	inv := mustInvite(t, s, o.ID, bob.Email, directory.RoleAdmin, time.Hour)

	m, err := s.AcceptInvite(ctx, inv.ID, bob.ID)
	if err != nil {
		t.Fatalf("AcceptInvite: %v", err)
	}
	if m.OrgID != o.ID || m.UserID != bob.ID || m.Role != directory.RoleAdmin || m.JoinedAt.IsZero() {
		t.Errorf("membership = %+v; want bob, admin of acme", m)
	}
	if got, err := s.GetMembership(ctx, bob.ID); err != nil || got != m {
		t.Errorf("GetMembership = %+v, %v; want %+v", got, err, m)
	}
	if _, err := s.GetInvite(ctx, inv.ID); !errors.Is(err, directory.ErrInviteNotFound) {
		t.Errorf("accepted invite still there: %v", err)
	}
	if _, err := s.AcceptInvite(ctx, inv.ID, bob.ID); !errors.Is(err, directory.ErrInviteNotFound) {
		t.Errorf("accepting twice = %v, want ErrInviteNotFound", err)
	}
}

func testAcceptInviteRejects(t *testing.T, s directory.Store) {
	ctx := t.Context()
	o := mustOrg(t, s, "Acme", mustUser(t, s, "ann").ID)
	bob := mustUser(t, s, "bob")

	if _, err := s.AcceptInvite(ctx, uuid.NewString(), bob.ID); !errors.Is(err, directory.ErrInviteNotFound) {
		t.Errorf("missing invite = %v, want ErrInviteNotFound", err)
	}

	expired := mustInvite(t, s, o.ID, bob.Email, directory.RoleMember, -time.Minute)
	if _, err := s.AcceptInvite(ctx, expired.ID, bob.ID); !errors.Is(err, directory.ErrInviteExpired) {
		t.Errorf("expired invite = %v, want ErrInviteExpired", err)
	}
	if _, err := s.GetMembership(ctx, bob.ID); !errors.Is(err, directory.ErrNotMember) {
		t.Errorf("bob joined through an expired invite: %v", err)
	}
	if _, err := s.GetInvite(ctx, expired.ID); err != nil {
		t.Errorf("refused invite was deleted: %v", err)
	}

	// Already in an org: refused, and the invite stays for after they leave.
	eve := mustUser(t, s, "eve")
	mustOrg(t, s, "Other", eve.ID)
	toEve := mustInvite(t, s, o.ID, eve.Email, directory.RoleMember, time.Hour)
	if _, err := s.AcceptInvite(ctx, toEve.ID, eve.ID); !errors.Is(err, directory.ErrAlreadyMember) {
		t.Errorf("user already in an org = %v, want ErrAlreadyMember", err)
	}
	if _, err := s.GetInvite(ctx, toEve.ID); err != nil {
		t.Errorf("refused invite was deleted: %v", err)
	}

	live := mustInvite(t, s, o.ID, "ghost@example.com", directory.RoleMember, time.Hour)
	if _, err := s.AcceptInvite(ctx, live.ID, "ghost"); !errors.Is(err, directory.ErrUserNotFound) {
		t.Errorf("unknown user = %v, want ErrUserNotFound", err)
	}
}

func testDeleteInvite(t *testing.T, s directory.Store) {
	ctx := t.Context()
	o := mustOrg(t, s, "Acme", mustUser(t, s, "ann").ID)
	inv := mustInvite(t, s, o.ID, "bob@example.com", directory.RoleMember, time.Hour)

	if err := s.DeleteInvite(ctx, inv.ID); err != nil {
		t.Fatalf("DeleteInvite: %v", err)
	}
	if _, err := s.GetInvite(ctx, inv.ID); !errors.Is(err, directory.ErrInviteNotFound) {
		t.Errorf("deleted invite still there: %v", err)
	}
	for _, id := range []string{inv.ID, uuid.NewString(), "not-a-uuid"} {
		if err := s.DeleteInvite(ctx, id); err != nil {
			t.Errorf("DeleteInvite(%s) again = %v, want nil (safe to retry)", id, err)
		}
	}
}
