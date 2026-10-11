package iam

// MemoryStore's InviteStore methods (struct and lock in memory.go).

import (
	"context"
	"fmt"
	"time"
)

func (m *MemoryStore) CreateInvite(ctx context.Context, inv Invite) (Invite, error) {
	inv, err := inv.normalize()
	if err != nil {
		return Invite{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.orgs[inv.OrgID]; !ok {
		return Invite{}, ErrOrgNotFound
	}
	// Inviting the same address again refreshes the existing invite (role,
	// inviter, expiry) and keeps its ID and creation time.
	for _, existing := range m.invites {
		if existing.OrgID == inv.OrgID && existing.Email == inv.Email {
			inv.ID, inv.CreatedAt = existing.ID, existing.CreatedAt
			m.invites[inv.ID] = inv
			return inv, nil
		}
	}
	inv.ID, inv.CreatedAt = newID(), now()
	m.invites[inv.ID] = inv
	return inv, nil
}

func (m *MemoryStore) GetInvite(ctx context.Context, id string) (Invite, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	inv, ok := m.invites[id]
	if !ok {
		return Invite{}, ErrInviteNotFound
	}
	return inv, nil
}

func (m *MemoryStore) ListOrgInvites(ctx context.Context, orgID string) ([]Invite, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.orgs[orgID]; !ok {
		return nil, ErrOrgNotFound
	}
	var out []Invite
	for _, inv := range m.invites {
		if inv.OrgID == orgID {
			out = append(out, inv)
		}
	}
	sortInvites(out)
	return out, nil
}

func (m *MemoryStore) ListEmailInvites(ctx context.Context, email string) ([]Invite, error) {
	key, t := emailKey(email), now()
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Invite
	for _, inv := range m.invites {
		if inv.Email == key && !inv.expired(t) {
			out = append(out, inv)
		}
	}
	sortInvites(out)
	return out, nil
}

func (m *MemoryStore) AcceptInvite(ctx context.Context, inviteID, userID string) (Member, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	inv, ok := m.invites[inviteID]
	switch {
	case !ok:
		return Member{}, ErrInviteNotFound
	case inv.expired(now()):
		return Member{}, fmt.Errorf("%w: expired at %s", ErrInviteExpired, inv.ExpiresAt.Format(time.RFC3339))
	}
	if _, ok := m.orgs[inv.OrgID]; !ok {
		return Member{}, ErrOrgNotFound
	}
	if _, ok := m.users[userID]; !ok {
		return Member{}, ErrUserNotFound
	}
	if _, ok := m.members[userID]; ok {
		return Member{}, ErrAlreadyMember
	}
	mem := Member{OrgID: inv.OrgID, UserID: userID, Role: inv.Role, JoinedAt: now()}
	m.members[userID] = mem
	delete(m.invites, inviteID)
	return mem, nil
}

func (m *MemoryStore) DeleteInvite(ctx context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.invites, id) // missing is fine: safe to retry
	return nil
}
