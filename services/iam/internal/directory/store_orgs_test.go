package directory_test

import (
	"errors"
	"github.com/mjcourte/modulith/services/iam/internal/directory"
	"slices"
	"testing"

	"github.com/google/uuid"
)

func testCreateOrg(t *testing.T, s directory.Store) {
	ctx := t.Context()
	ann := mustUser(t, s, "ann")
	o, admin, err := s.CreateOrg(ctx, directory.Org{Name: "  Acme  "}, ann.ID)
	if err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}
	if !isUUIDv7(o.ID) || o.Name != "Acme" || o.CreatedAt.IsZero() || !o.UpdatedAt.Equal(o.CreatedAt) {
		t.Errorf("org = %+v; want a UUIDv7, trimmed name, timestamps", o)
	}
	want := directory.Member{OrgID: o.ID, UserID: ann.ID, Role: directory.RoleAdmin, JoinedAt: o.CreatedAt}
	if admin != want {
		t.Errorf("creator = %+v, want %+v", admin, want)
	}
	if got, err := s.GetOrg(ctx, o.ID); err != nil || got != o {
		t.Errorf("GetOrg = %+v, %v; want %+v", got, err, o)
	}
	if got, err := s.GetMembership(ctx, ann.ID); err != nil || got != want {
		t.Errorf("GetMembership = %+v, %v; want %+v", got, err, want)
	}
	if ms, err := s.ListMembers(ctx, o.ID); err != nil || len(ms) != 1 || ms[0] != want {
		t.Errorf("ListMembers = %+v, %v", ms, err)
	}
}

func testCreateOrgRejects(t *testing.T, s directory.Store) {
	ctx := t.Context()
	ann := mustUser(t, s, "ann")
	if _, _, err := s.CreateOrg(ctx, directory.Org{Name: "  "}, ann.ID); !errors.Is(err, directory.ErrInvalidOrg) {
		t.Errorf("empty name = %v, want ErrInvalidOrg", err)
	}
	if _, _, err := s.CreateOrg(ctx, directory.Org{Name: "Ghost Co"}, "nobody"); !errors.Is(err, directory.ErrUserNotFound) {
		t.Errorf("unknown creator = %v, want ErrUserNotFound", err)
	}
	first := mustOrg(t, s, "Acme", ann.ID)
	if _, _, err := s.CreateOrg(ctx, directory.Org{Name: "Second"}, ann.ID); !errors.Is(err, directory.ErrAlreadyMember) {
		t.Errorf("second org for the same user = %v, want ErrAlreadyMember", err)
	}
	if m, _ := s.GetMembership(ctx, ann.ID); m.OrgID != first.ID {
		t.Errorf("ann's membership moved to %s", m.OrgID)
	}

	for _, id := range []string{uuid.NewString(), "not-a-uuid"} {
		if _, err := s.GetOrg(ctx, id); !errors.Is(err, directory.ErrOrgNotFound) {
			t.Errorf("GetOrg(%s) = %v, want ErrOrgNotFound", id, err)
		}
	}
	if _, err := s.GetMembership(ctx, "bob"); !errors.Is(err, directory.ErrNotMember) {
		t.Errorf("GetMembership(no org) = %v, want ErrNotMember", err)
	}
}

func testRenameOrg(t *testing.T, s directory.Store) {
	ctx := t.Context()
	o := mustOrg(t, s, "Acme", mustUser(t, s, "ann").ID)

	letTimePass()
	renamed, err := s.RenameOrg(ctx, o.ID, " Acme Ltd ")
	if err != nil || renamed.Name != "Acme Ltd" || !renamed.UpdatedAt.After(o.UpdatedAt) || !renamed.CreatedAt.Equal(o.CreatedAt) {
		t.Fatalf("RenameOrg = %+v, %v", renamed, err)
	}
	letTimePass()
	if same, err := s.RenameOrg(ctx, o.ID, "Acme Ltd"); err != nil || same != renamed {
		t.Errorf("renaming to the same name = %+v, %v; want no change", same, err)
	}
	if _, err := s.RenameOrg(ctx, o.ID, ""); !errors.Is(err, directory.ErrInvalidOrg) {
		t.Errorf("empty name = %v, want ErrInvalidOrg", err)
	}
	if got, _ := s.GetOrg(ctx, o.ID); got != renamed {
		t.Errorf("stored org = %+v, want %+v", got, renamed)
	}
	if _, err := s.RenameOrg(ctx, uuid.NewString(), "X"); !errors.Is(err, directory.ErrOrgNotFound) {
		t.Errorf("missing org = %v, want ErrOrgNotFound", err)
	}
}

func testListMembersOrder(t *testing.T, s directory.Store) {
	ctx := t.Context()
	ann := mustUser(t, s, "ann")
	o := mustOrg(t, s, "Acme", ann.ID)
	letTimePass()
	mustJoin(t, s, o.ID, mustUser(t, s, "bob"), directory.RoleMember)
	letTimePass()
	mustJoin(t, s, o.ID, mustUser(t, s, "cat"), directory.RoleAdmin)
	letTimePass()
	mustJoin(t, s, o.ID, mustUser(t, s, "dan"), directory.RoleMember)
	// Someone in another org isn't listed.
	mustOrg(t, s, "Other", mustUser(t, s, "eve").ID)

	ms, err := s.ListMembers(ctx, o.ID)
	if err != nil {
		t.Fatalf("ListMembers: %v", err)
	}
	// Admins first, then by join time.
	if got, want := memberIDs(ms), []string{"ann", "cat", "bob", "dan"}; !slices.Equal(got, want) {
		t.Errorf("ListMembers = %v, want %v", got, want)
	}
	if _, err := s.ListMembers(ctx, uuid.NewString()); !errors.Is(err, directory.ErrOrgNotFound) {
		t.Errorf("missing org = %v, want ErrOrgNotFound", err)
	}
}

func testSetRole(t *testing.T, s directory.Store) {
	ctx := t.Context()
	ann := mustUser(t, s, "ann")
	o := mustOrg(t, s, "Acme", ann.ID)
	bob := mustUser(t, s, "bob")
	mustJoin(t, s, o.ID, bob, directory.RoleMember)

	if _, err := s.SetRole(ctx, o.ID, ann.ID, directory.RoleMember); !errors.Is(err, directory.ErrLastAdmin) {
		t.Fatalf("demote the only admin = %v, want ErrLastAdmin", err)
	}
	if m, _ := s.GetMembership(ctx, ann.ID); m.Role != directory.RoleAdmin {
		t.Errorf("refused demotion still changed ann to %s", m.Role)
	}

	promoted, err := s.SetRole(ctx, o.ID, bob.ID, directory.RoleAdmin)
	if err != nil || promoted.Role != directory.RoleAdmin || promoted.UserID != bob.ID {
		t.Fatalf("promote bob = %+v, %v", promoted, err)
	}
	if _, err := s.SetRole(ctx, o.ID, ann.ID, directory.RoleMember); err != nil {
		t.Errorf("demote ann once bob is admin = %v", err)
	}
	if m, _ := s.GetMembership(ctx, ann.ID); m.Role != directory.RoleMember {
		t.Errorf("ann's role = %s, want member", m.Role)
	}

	eve := mustUser(t, s, "eve")
	other := mustOrg(t, s, "Other", eve.ID)
	if _, err := s.SetRole(ctx, o.ID, eve.ID, directory.RoleAdmin); !errors.Is(err, directory.ErrNotMember) {
		t.Errorf("member of another org = %v, want ErrNotMember", err)
	}
	if m, _ := s.GetMembership(ctx, eve.ID); m.OrgID != other.ID || m.Role != directory.RoleAdmin {
		t.Errorf("eve's membership changed: %+v", m)
	}
	if _, err := s.SetRole(ctx, o.ID, bob.ID, "owner"); !errors.Is(err, directory.ErrInvalidOrg) {
		t.Errorf("unknown role = %v, want ErrInvalidOrg", err)
	}
	if _, err := s.SetRole(ctx, uuid.NewString(), bob.ID, directory.RoleMember); !errors.Is(err, directory.ErrOrgNotFound) {
		t.Errorf("missing org = %v, want ErrOrgNotFound", err)
	}
}

func testRemoveMember(t *testing.T, s directory.Store) {
	ctx := t.Context()
	ann := mustUser(t, s, "ann")
	o := mustOrg(t, s, "Acme", ann.ID)
	bob := mustUser(t, s, "bob")
	mustJoin(t, s, o.ID, bob, directory.RoleMember)

	if _, err := s.RemoveMember(ctx, o.ID, ann.ID); !errors.Is(err, directory.ErrLastAdmin) {
		t.Fatalf("remove the only admin while bob remains = %v, want ErrLastAdmin", err)
	}
	if _, err := s.GetMembership(ctx, ann.ID); err != nil {
		t.Errorf("refused removal still removed ann: %v", err)
	}

	if n, err := s.RemoveMember(ctx, o.ID, bob.ID); err != nil || n != 1 {
		t.Fatalf("remove bob = %d, %v; want 1 remaining", n, err)
	}
	if _, err := s.GetMembership(ctx, bob.ID); !errors.Is(err, directory.ErrNotMember) {
		t.Errorf("bob after removal = %v, want ErrNotMember", err)
	}
	if _, err := s.RemoveMember(ctx, o.ID, bob.ID); !errors.Is(err, directory.ErrNotMember) {
		t.Errorf("removing bob again = %v, want ErrNotMember", err)
	}
	// Free again: bob can start his own org.
	if _, _, err := s.CreateOrg(ctx, directory.Org{Name: "Bob's"}, bob.ID); err != nil {
		t.Errorf("bob creates an org after leaving: %v", err)
	}

	// The very last member may go; the org is then empty.
	if n, err := s.RemoveMember(ctx, o.ID, ann.ID); err != nil || n != 0 {
		t.Errorf("remove the last member = %d, %v; want 0 remaining", n, err)
	}
	if ms, err := s.ListMembers(ctx, o.ID); err != nil || len(ms) != 0 {
		t.Errorf("members of the emptied org = %v, %v", memberIDs(ms), err)
	}
	if _, err := s.RemoveMember(ctx, uuid.NewString(), ann.ID); !errors.Is(err, directory.ErrOrgNotFound) {
		t.Errorf("missing org = %v, want ErrOrgNotFound", err)
	}
}
