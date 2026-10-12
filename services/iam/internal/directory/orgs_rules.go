package directory

// Org and membership rules shared by every OrgStore implementation. The
// membership rules are pure functions over an org's member list, so the
// memory store and Postgres (which loads the list under a lock) decide the
// same way.

import (
	"cmp"
	"fmt"
	"slices"
)

// valid reports whether r is a known role.
func (r Role) valid() bool { return r == RoleAdmin || r == RoleMember }

// checkRole validates a role, wrapping invalid.
func checkRole(r Role, invalid error) error {
	if !r.valid() {
		return fmt.Errorf("%w: unknown role %q", invalid, r)
	}
	return nil
}

// normalize validates an org for CreateOrg: a required name.
func (o Org) normalize() (Org, error) {
	var err error
	o.Name, err = normalizeName(o.Name, false, ErrInvalidOrg)
	return o, err
}

// hasAdmin reports whether any of members is an admin.
func hasAdmin(members []Member) bool {
	return slices.ContainsFunc(members, func(m Member) bool { return m.Role == RoleAdmin })
}

// indexOf returns the position of userID in members, or -1.
func indexOf(members []Member, userID string) int {
	return slices.IndexFunc(members, func(m Member) bool { return m.UserID == userID })
}

// checkRoleChange decides SetRole: userID must be a member (ErrNotMember), and
// the org must still have an admin afterwards (ErrLastAdmin).
func checkRoleChange(members []Member, userID string, role Role) error {
	if err := checkRole(role, ErrInvalidOrg); err != nil {
		return err
	}
	i := indexOf(members, userID)
	if i < 0 {
		return ErrNotMember
	}
	after := slices.Clone(members)
	after[i].Role = role
	if !hasAdmin(after) {
		return fmt.Errorf("%w: promote someone else first", ErrLastAdmin)
	}
	return nil
}

// checkRemoval decides RemoveMember and returns how many members would
// remain: userID must be a member (ErrNotMember), and an org that still has
// members afterwards must still have an admin (ErrLastAdmin). Removing the
// very last member is fine: the org is then deleted.
func checkRemoval(members []Member, userID string) (remaining int, err error) {
	i := indexOf(members, userID)
	if i < 0 {
		return 0, ErrNotMember
	}
	after := slices.Delete(slices.Clone(members), i, i+1)
	if len(after) > 0 && !hasAdmin(after) {
		return 0, fmt.Errorf("%w: promote someone else first", ErrLastAdmin)
	}
	return len(after), nil
}

// sortMembers puts members in ListMembers order: admins first, then by join
// time, then by user ID so equal join times still have one order.
func sortMembers(members []Member) {
	slices.SortFunc(members, func(a, b Member) int {
		if a.Role != b.Role {
			if a.Role == RoleAdmin {
				return -1
			}
			return 1
		}
		if c := a.JoinedAt.Compare(b.JoinedAt); c != 0 {
			return c
		}
		return cmp.Compare(a.UserID, b.UserID)
	})
}
