package manager

// Orgs and their members. Members may look; admins may change things.

import (
	"context"
	"gyoza/services/iam/internal/directory"
)

// CreateOrg creates an org with the acting user as its first admin. A user
// already in an org can't create another (ErrAlreadyMember).
func (m *Manager) CreateOrg(ctx context.Context, actorID, name string) (directory.Org, directory.Member, error) {
	if _, err := m.actor(ctx, actorID); err != nil {
		return directory.Org{}, directory.Member{}, err
	}
	return m.store.CreateOrg(ctx, directory.Org{Name: name}, actorID)
}

// GetOrg returns an org the acting user belongs to.
func (m *Manager) GetOrg(ctx context.Context, actorID, orgID string) (directory.Org, error) {
	if _, err := m.member(ctx, actorID, orgID); err != nil {
		return directory.Org{}, err
	}
	return m.store.GetOrg(ctx, orgID)
}

// RenameOrg renames an org; admins only.
func (m *Manager) RenameOrg(ctx context.Context, actorID, orgID, name string) (directory.Org, error) {
	if _, err := m.admin(ctx, actorID, orgID); err != nil {
		return directory.Org{}, err
	}
	return m.store.RenameOrg(ctx, orgID, name)
}

// ListMembers lists an org's members (admins first) for any of its members.
func (m *Manager) ListMembers(ctx context.Context, actorID, orgID string) ([]directory.Member, error) {
	if _, err := m.member(ctx, actorID, orgID); err != nil {
		return nil, err
	}
	return m.store.ListMembers(ctx, orgID)
}

// SetRole makes a member an admin or a member; admins only. Demoting the last
// admin is ErrLastAdmin (an admin can demote themselves if another admin remains).
func (m *Manager) SetRole(ctx context.Context, actorID, orgID, userID string, role directory.Role) (directory.Member, error) {
	if _, err := m.admin(ctx, actorID, orgID); err != nil {
		return directory.Member{}, err
	}
	return m.store.SetRole(ctx, orgID, userID, role)
}

// RemoveMember takes someone out of the org; admins only (members leave with
// LeaveOrg). It returns how many members remain. The last admin can't be
// removed while others remain (ErrLastAdmin).
//
// Their own files and projects stay theirs, and the org's stay the org's
// (see PLAN.md). When the last member goes, the org itself is deleted with
// its files and projects.
func (m *Manager) RemoveMember(ctx context.Context, actorID, orgID, userID string) (int, error) {
	if _, err := m.admin(ctx, actorID, orgID); err != nil {
		return 0, err
	}
	return m.removeMember(ctx, orgID, userID)
}

// LeaveOrg takes the acting user out of their org and returns how many
// members remain. The last admin must promote someone first (ErrLastAdmin),
// unless they're the very last member, whose leaving deletes the org.
func (m *Manager) LeaveOrg(ctx context.Context, actorID string) (int, error) {
	if _, err := m.actor(ctx, actorID); err != nil {
		return 0, err
	}
	mem, err := m.store.GetMembership(ctx, actorID)
	if err != nil {
		return 0, err // ErrNotMember: they're in no org
	}
	return m.removeMember(ctx, mem.OrgID, actorID)
}
