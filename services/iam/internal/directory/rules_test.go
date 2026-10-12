package directory

// Internal tests of the unexported rules every Store relies on.

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestNormalizeEmail(t *testing.T) {
	good := map[string]string{
		"ann@example.com":         "ann@example.com",
		"  Ann.Lee@Example.COM  ": "ann.lee@example.com",
		"a+tag@sub.example.co.uk": "a+tag@sub.example.co.uk",
	}
	for in, want := range good {
		if got, err := normalizeEmail(in, ErrInvalidUser); err != nil || got != want {
			t.Errorf("normalizeEmail(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	bad := []string{
		"", "   ", "ann", "ann@", "@example.com", "ann@@example.com",
		"Ann <ann@example.com>", "ann@example.com (work)", "a b@example.com",
		strings.Repeat("a", MaxEmailLen) + "@example.com",
	}
	for _, in := range bad {
		if _, err := normalizeEmail(in, ErrInvalidInvite); !errors.Is(err, ErrInvalidInvite) {
			t.Errorf("normalizeEmail(%q) = %v, want the given sentinel", in, err)
		}
	}
}

func TestCheckUserID(t *testing.T) {
	for _, id := range []string{"a1b2c3", "8f3e2a10-1c2d-4e5f-9a8b-7c6d5e4f3a2b", strings.Repeat("x", MaxUserIDLen)} {
		if err := checkUserID(id); err != nil {
			t.Errorf("checkUserID(%q) = %v", id, err)
		}
	}
	for _, id := range []string{"", "has space", "tab\t", "ünï", strings.Repeat("x", MaxUserIDLen+1)} {
		if err := checkUserID(id); !errors.Is(err, ErrInvalidUser) {
			t.Errorf("checkUserID(%q) = %v, want ErrInvalidUser", id, err)
		}
	}
}

func TestCheckAvatarURL(t *testing.T) {
	for _, u := range []string{"", "https://lh3.googleusercontent.com/a/xyz=s96-c"} {
		if err := checkAvatarURL(u); err != nil {
			t.Errorf("checkAvatarURL(%q) = %v", u, err)
		}
	}
	bad := []string{
		"http://example.com/a.png", "javascript:alert(1)", "data:image/png;base64,AAAA",
		"https:///nohost", "/relative.png", "https://user:pw@example.com/a.png",
		"https://example.com/" + strings.Repeat("a", MaxURLLen),
	}
	for _, u := range bad {
		if err := checkAvatarURL(u); !errors.Is(err, ErrInvalidUser) {
			t.Errorf("checkAvatarURL(%q) = %v, want ErrInvalidUser", u, err)
		}
	}
}

func TestNormalizeName(t *testing.T) {
	if got, err := normalizeName("  Acme Ltd  ", false, ErrInvalidOrg); err != nil || got != "Acme Ltd" {
		t.Errorf("normalizeName trims: %q, %v", got, err)
	}
	if got, err := normalizeName("  ", true, ErrInvalidUser); err != nil || got != "" {
		t.Errorf("optional empty name = %q, %v; want \"\", nil", got, err)
	}
	// Length counts characters, not bytes: 200 Japanese characters are fine.
	if _, err := normalizeName(strings.Repeat("寿", MaxNameLen), false, ErrInvalidOrg); err != nil {
		t.Errorf("%d multibyte characters rejected: %v", MaxNameLen, err)
	}
	for _, name := range []string{"", "\t", "bell\a", "line\nbreak", "\xff\xfe", strings.Repeat("a", MaxNameLen+1)} {
		if _, err := normalizeName(name, false, ErrInvalidOrg); !errors.Is(err, ErrInvalidOrg) {
			t.Errorf("normalizeName(%q) = %v, want ErrInvalidOrg", name, err)
		}
	}
}

func TestIdentityNormalize(t *testing.T) {
	id, err := Identity{UserID: "st-1", Email: " Ann@Example.com", Name: " Ann ", ProviderAvatarURL: " https://example.com/a.png "}.normalize()
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	want := Identity{UserID: "st-1", Email: "ann@example.com", Name: "Ann", ProviderAvatarURL: "https://example.com/a.png"}
	if id != want {
		t.Errorf("normalize = %+v, want %+v", id, want)
	}
	for name, bad := range map[string]Identity{
		"no user ID": {Email: "a@b.co"},
		"no email":   {UserID: "st-1"},
		"bad avatar": {UserID: "st-1", Email: "a@b.co", ProviderAvatarURL: "http://x.co/a.png"},
		"bad name":   {UserID: "st-1", Email: "a@b.co", Name: "a\x00b"},
	} {
		if _, err := bad.normalize(); !errors.Is(err, ErrInvalidUser) {
			t.Errorf("%s: %v, want ErrInvalidUser", name, err)
		}
	}
}

// members builds a member list from "user:role" pairs.
func members(pairs ...string) []Member {
	var ms []Member
	for i, p := range pairs {
		user, role, _ := strings.Cut(p, ":")
		ms = append(ms, Member{OrgID: "o", UserID: user, Role: Role(role), JoinedAt: time.Unix(int64(i), 0)})
	}
	return ms
}

func TestCheckRoleChange(t *testing.T) {
	tests := []struct {
		name    string
		members []Member
		user    string
		role    Role
		want    error
	}{
		{"promote", members("a:admin", "b:member"), "b", RoleAdmin, nil},
		{"demote one of two admins", members("a:admin", "b:admin"), "a", RoleMember, nil},
		{"demote the last admin", members("a:admin", "b:member"), "a", RoleMember, ErrLastAdmin},
		{"demote the only member", members("a:admin"), "a", RoleMember, ErrLastAdmin},
		{"same role again", members("a:admin"), "a", RoleAdmin, nil},
		{"not a member", members("a:admin"), "z", RoleAdmin, ErrNotMember},
		{"unknown role", members("a:admin", "b:member"), "b", Role("owner"), ErrInvalidOrg},
	}
	for _, tt := range tests {
		err := checkRoleChange(tt.members, tt.user, tt.role)
		if (tt.want == nil && err != nil) || (tt.want != nil && !errors.Is(err, tt.want)) {
			t.Errorf("%s: %v, want %v", tt.name, err, tt.want)
		}
	}
}

func TestCheckRemoval(t *testing.T) {
	tests := []struct {
		name          string
		members       []Member
		user          string
		wantRemaining int
		want          error
	}{
		{"a member leaves", members("a:admin", "b:member"), "b", 1, nil},
		{"one of two admins leaves", members("a:admin", "b:admin", "c:member"), "a", 2, nil},
		{"last admin leaves others behind", members("a:admin", "b:member"), "a", 0, ErrLastAdmin},
		{"the very last member leaves", members("a:admin"), "a", 0, nil},
		{"not a member", members("a:admin"), "z", 0, ErrNotMember},
	}
	for _, tt := range tests {
		remaining, err := checkRemoval(tt.members, tt.user)
		if (tt.want == nil && err != nil) || (tt.want != nil && !errors.Is(err, tt.want)) || remaining != tt.wantRemaining {
			t.Errorf("%s: %d, %v; want %d, %v", tt.name, remaining, err, tt.wantRemaining, tt.want)
		}
	}
	// The caller's slice is untouched.
	ms := members("a:admin", "b:member")
	checkRemoval(ms, "b")
	if len(ms) != 2 || ms[1].UserID != "b" {
		t.Errorf("checkRemoval modified its input: %+v", ms)
	}
}

func TestSortMembers(t *testing.T) {
	// a2 comes first in the input, so only the user ID tie-break can put a1 first.
	ms := members("m1:member", "a2:admin", "m2:member", "a1:admin")
	ms[3].JoinedAt = ms[1].JoinedAt // tie on join time: broken by user ID
	sortMembers(ms)
	var got []string
	for _, m := range ms {
		got = append(got, m.UserID)
	}
	if want := []string{"a1", "a2", "m1", "m2"}; !slices.Equal(got, want) {
		t.Errorf("sortMembers = %v, want %v", got, want)
	}
}

func TestInviteNormalize(t *testing.T) {
	valid := Invite{OrgID: newID(), Email: " Bob@Example.com ", Role: RoleMember, InvitedBy: "st-1", ExpiresAt: time.Now()}
	inv, err := valid.normalize()
	if err != nil || inv.Email != "bob@example.com" {
		t.Fatalf("normalize = %+v, %v", inv, err)
	}
	for name, change := range map[string]func(*Invite){
		"bad org ID": func(i *Invite) { i.OrgID = "acme" },
		"bad email":  func(i *Invite) { i.Email = "bob" },
		"bad role":   func(i *Invite) { i.Role = "owner" },
		"no inviter": func(i *Invite) { i.InvitedBy = "" },
		"no expiry":  func(i *Invite) { i.ExpiresAt = time.Time{} },
	} {
		bad := valid
		change(&bad)
		if _, err := bad.normalize(); !errors.Is(err, ErrInvalidInvite) {
			t.Errorf("%s: %v, want ErrInvalidInvite", name, err)
		}
	}
}

func TestInviteExpired(t *testing.T) {
	at := time.Now()
	inv := Invite{ExpiresAt: at}
	if inv.expired(at.Add(-time.Second)) || !inv.expired(at) || !inv.expired(at.Add(time.Second)) {
		t.Error("an invite expires exactly at ExpiresAt")
	}
}

func TestSortInvites(t *testing.T) {
	first, second, third := newID(), newID(), newID() // UUIDv7: increasing
	invs := []Invite{{ID: second}, {ID: first}, {ID: third}}
	sortInvites(invs)
	if invs[0].ID != third || invs[1].ID != second || invs[2].ID != first {
		t.Errorf("sortInvites not newest first: %v", invs)
	}
}

func TestValidID(t *testing.T) {
	id := newID()
	if !validID(id) {
		t.Errorf("validID(%q) = false", id)
	}
	for _, bad := range []string{"", "acme", strings.ToUpper(id), "{" + id + "}"} {
		if validID(bad) {
			t.Errorf("validID(%q) = true, want only canonical UUIDs", bad)
		}
	}
}
