package directory

// Postgres's InviteStore methods (plumbing in postgres.go).

import (
	"context"
	"fmt"
	"time"

	"gyoza/services/iam/internal/db"
)

func (p *Postgres) CreateInvite(ctx context.Context, inv Invite) (Invite, error) {
	inv, err := inv.normalize()
	if err != nil {
		return Invite{}, err
	}
	err = p.inTx(ctx, func(q *db.Queries) error {
		if err := lockLiveOrg(ctx, q, inv.OrgID); err != nil {
			return err
		}
		// A new ID and time only count if no invite for this address exists;
		// otherwise the existing invite keeps its own.
		row, err := q.UpsertInvite(ctx, db.UpsertInviteParams{
			ID: newID(), OrgID: inv.OrgID, Email: inv.Email, Role: string(inv.Role),
			InvitedBy: inv.InvitedBy, CreatedAt: now(), ExpiresAt: inv.ExpiresAt,
		})
		if err != nil {
			return err
		}
		inv = inviteFromRow(row)
		return nil
	})
	return inv, err
}

func (p *Postgres) GetInvite(ctx context.Context, id string) (Invite, error) {
	if !validID(id) {
		return Invite{}, ErrInviteNotFound
	}
	row, err := p.q.GetInvite(ctx, id)
	if noRows(err) {
		return Invite{}, ErrInviteNotFound
	}
	if err != nil {
		return Invite{}, err
	}
	return inviteFromRow(row), nil
}

func (p *Postgres) ListOrgInvites(ctx context.Context, orgID string) ([]Invite, error) {
	if _, err := p.GetOrg(ctx, orgID); err != nil {
		return nil, err
	}
	rows, err := p.q.ListOrgInvites(ctx, orgID)
	if err != nil {
		return nil, err
	}
	return invitesFromRows(rows), nil
}

func (p *Postgres) ListEmailInvites(ctx context.Context, email string) ([]Invite, error) {
	rows, err := p.q.ListEmailInvites(ctx, db.ListEmailInvitesParams{Email: emailKey(email), ExpiresAt: now()})
	if err != nil {
		return nil, err
	}
	return invitesFromRows(rows), nil
}

func (p *Postgres) AcceptInvite(ctx context.Context, inviteID, userID string) (Member, error) {
	if !validID(inviteID) {
		return Member{}, ErrInviteNotFound
	}
	var mem Member
	err := p.inTx(ctx, func(q *db.Queries) error {
		row, err := q.GetInviteForUpdate(ctx, inviteID)
		if noRows(err) {
			return ErrInviteNotFound
		}
		if err != nil {
			return err
		}
		inv := inviteFromRow(row)
		if inv.expired(now()) {
			return fmt.Errorf("%w: expired at %s", ErrInviteExpired, inv.ExpiresAt.Format(time.RFC3339))
		}
		if err := lockLiveOrg(ctx, q, inv.OrgID); err != nil {
			return err
		}
		if err := lockLiveUser(ctx, q, userID); err != nil {
			return err
		}
		mem = Member{OrgID: inv.OrgID, UserID: userID, Role: inv.Role, JoinedAt: now()}
		if err := q.InsertMembership(ctx, db.InsertMembershipParams{
			UserID: userID, OrgID: inv.OrgID, Role: string(inv.Role), JoinedAt: mem.JoinedAt,
		}); err != nil {
			return err
		}
		return q.DeleteInvite(ctx, inviteID)
	})
	if violates(err, "memberships_pkey") {
		return Member{}, ErrAlreadyMember
	}
	return mem, err
}

func (p *Postgres) DeleteInvite(ctx context.Context, id string) error {
	if !validID(id) {
		return nil // can't exist, so it's already gone
	}
	return p.q.DeleteInvite(ctx, id)
}
