package manager

// Invites: admins invite an email address; whoever signs in with that
// (verified) address may accept or decline.

import (
	"context"
	"errors"
	"fmt"
	"gyoza/services/iam/internal/directory"
)

// InviteMember invites email to the org with role; admins only. Inviting
// someone who's already a member is ErrAlreadyMember; inviting the same
// address again refreshes the invite (new role, new expiry). Someone in
// another org can be invited: they can accept once they've left it.
func (m *Manager) InviteMember(ctx context.Context, actorID, orgID, email string, role directory.Role) (directory.Invite, error) {
	if _, err := m.admin(ctx, actorID, orgID); err != nil {
		return directory.Invite{}, err
	}
	if u, err := m.store.GetUserByEmail(ctx, email); err == nil {
		if mem, err := m.store.GetMembership(ctx, u.ID); err == nil && mem.OrgID == orgID {
			return directory.Invite{}, fmt.Errorf("%w: %s is already in this org", directory.ErrAlreadyMember, email)
		} else if err != nil && !errors.Is(err, directory.ErrNotMember) {
			return directory.Invite{}, err
		}
	} else if !errors.Is(err, directory.ErrUserNotFound) {
		return directory.Invite{}, err
	}
	return m.store.CreateInvite(ctx, directory.Invite{
		OrgID: orgID, Email: email, Role: role, InvitedBy: actorID,
		ExpiresAt: m.now().Add(m.cfg.InviteTTL),
	})
}

// ListOrgInvites lists an org's invites, expired ones too; admins only.
func (m *Manager) ListOrgInvites(ctx context.Context, actorID, orgID string) ([]directory.Invite, error) {
	if _, err := m.admin(ctx, actorID, orgID); err != nil {
		return nil, err
	}
	return m.store.ListOrgInvites(ctx, orgID)
}

// RevokeInvite withdraws an invite; admins of its org only.
func (m *Manager) RevokeInvite(ctx context.Context, actorID, inviteID string) error {
	inv, err := m.store.GetInvite(ctx, inviteID)
	if errors.Is(err, directory.ErrInviteNotFound) {
		return fmt.Errorf("%w: no such invite", ErrForbidden) // outsiders learn nothing
	}
	if err != nil {
		return err
	}
	if _, err := m.admin(ctx, actorID, inv.OrgID); err != nil {
		return err
	}
	return m.store.DeleteInvite(ctx, inviteID)
}

// ListMyInvites lists the acting user's pending invites. Only a verified
// email address can claim invites sent to it.
func (m *Manager) ListMyInvites(ctx context.Context, actorID string) ([]directory.Invite, error) {
	u, err := m.actor(ctx, actorID)
	if err != nil {
		return nil, err
	}
	if !u.EmailVerified {
		return nil, nil
	}
	return m.store.ListEmailInvites(ctx, u.Email)
}

// invitee returns the invite if it's addressed to the acting user's verified
// email. Someone else's invite is ErrInviteNotFound, as if it didn't exist.
func (m *Manager) invitee(ctx context.Context, actorID, inviteID string) (directory.User, directory.Invite, error) {
	u, err := m.actor(ctx, actorID)
	if err != nil {
		return directory.User{}, directory.Invite{}, err
	}
	inv, err := m.store.GetInvite(ctx, inviteID)
	if err != nil {
		return directory.User{}, directory.Invite{}, err
	}
	if inv.Email != u.Email {
		return directory.User{}, directory.Invite{}, directory.ErrInviteNotFound
	}
	if !u.EmailVerified {
		return directory.User{}, directory.Invite{}, fmt.Errorf("%w: verify your email address to accept invites", ErrForbidden)
	}
	return u, inv, nil
}

// AcceptInvite makes the acting user a member of the invite's org. They must
// be the invitee and in no org yet (ErrAlreadyMember: leave yours first; the
// invite stays until it expires).
func (m *Manager) AcceptInvite(ctx context.Context, actorID, inviteID string) (directory.Member, error) {
	if _, _, err := m.invitee(ctx, actorID, inviteID); err != nil {
		return directory.Member{}, err
	}
	return m.store.AcceptInvite(ctx, inviteID, actorID)
}

// DeclineInvite deletes an invite addressed to the acting user.
func (m *Manager) DeclineInvite(ctx context.Context, actorID, inviteID string) error {
	if _, _, err := m.invitee(ctx, actorID, inviteID); err != nil {
		return err
	}
	return m.store.DeleteInvite(ctx, inviteID)
}
