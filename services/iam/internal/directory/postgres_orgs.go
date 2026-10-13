package directory

// Postgres's OrgStore methods (plumbing in postgres.go).

import (
	"context"

	"gyoza/services/iam/internal/db"
)

func (p *Postgres) CreateOrg(ctx context.Context, o Org, creatorID string) (Org, Member, error) {
	o, err := o.normalize()
	if err != nil {
		return Org{}, Member{}, err
	}
	o.ID, o.CreatedAt = newID(), now()
	o.UpdatedAt = o.CreatedAt
	admin := Member{OrgID: o.ID, UserID: creatorID, Role: RoleAdmin, JoinedAt: o.CreatedAt}
	err = p.inTx(ctx, func(q *db.Queries) error {
		if err := lockLiveUser(ctx, q, creatorID); err != nil {
			return err
		}
		if err := q.InsertOrg(ctx, db.InsertOrgParams{ID: o.ID, Name: o.Name, CreatedAt: o.CreatedAt}); err != nil {
			return err
		}
		return q.InsertMembership(ctx, db.InsertMembershipParams{
			UserID: creatorID, OrgID: o.ID, Role: string(RoleAdmin), JoinedAt: admin.JoinedAt,
		})
	})
	if violates(err, "memberships_pkey") {
		return Org{}, Member{}, ErrAlreadyMember
	}
	if err != nil {
		return Org{}, Member{}, err
	}
	return o, admin, nil
}

func (p *Postgres) GetOrg(ctx context.Context, id string) (Org, error) {
	if !validID(id) {
		return Org{}, ErrOrgNotFound
	}
	row, err := p.q.GetOrg(ctx, id)
	if noRows(err) {
		return Org{}, ErrOrgNotFound
	}
	if err != nil {
		return Org{}, err
	}
	return orgFromRow(row), nil
}

func (p *Postgres) RenameOrg(ctx context.Context, id, name string) (Org, error) {
	name, err := normalizeName(name, false, ErrInvalidOrg)
	if err != nil {
		return Org{}, err
	}
	if !validID(id) {
		return Org{}, ErrOrgNotFound
	}
	var o Org
	err = p.inTx(ctx, func(q *db.Queries) error {
		row, err := q.GetOrgForUpdate(ctx, id)
		if noRows(err) {
			return ErrOrgNotFound
		}
		if err != nil {
			return err
		}
		o = orgFromRow(row)
		if o.Name == name { // renaming to the same name changes nothing, UpdatedAt included
			return nil
		}
		o.Name, o.UpdatedAt = name, now()
		return q.RenameOrg(ctx, db.RenameOrgParams{ID: id, Name: name, UpdatedAt: o.UpdatedAt})
	})
	return o, err
}

func (p *Postgres) GetMembership(ctx context.Context, userID string) (Member, error) {
	row, err := p.q.GetMembership(ctx, userID)
	if noRows(err) {
		return Member{}, ErrNotMember
	}
	if err != nil {
		return Member{}, err
	}
	return memberFromRow(row), nil
}

func (p *Postgres) ListMembers(ctx context.Context, orgID string) ([]Member, error) {
	if _, err := p.GetOrg(ctx, orgID); err != nil {
		return nil, err
	}
	rows, err := p.q.ListMembers(ctx, orgID)
	if err != nil {
		return nil, err
	}
	return membersFromRows(rows), nil
}

// lockedMembers locks orgID's row for the rest of the transaction and returns
// its members, so the membership rules decide on a list that can't change
// underneath. A missing org is ErrOrgNotFound.
func lockedMembers(ctx context.Context, q *db.Queries, orgID string) ([]Member, error) {
	if !validID(orgID) {
		return nil, ErrOrgNotFound
	}
	if _, err := q.GetOrgForUpdate(ctx, orgID); noRows(err) {
		return nil, ErrOrgNotFound
	} else if err != nil {
		return nil, err
	}
	rows, err := q.ListMembers(ctx, orgID)
	if err != nil {
		return nil, err
	}
	return membersFromRows(rows), nil
}

func (p *Postgres) SetRole(ctx context.Context, orgID, userID string, role Role) (Member, error) {
	var mem Member
	err := p.inTx(ctx, func(q *db.Queries) error {
		ms, err := lockedMembers(ctx, q, orgID)
		if err != nil {
			return err
		}
		if err := checkRoleChange(ms, userID, role); err != nil {
			return err
		}
		mem = ms[indexOf(ms, userID)]
		mem.Role = role
		return q.SetRole(ctx, db.SetRoleParams{OrgID: orgID, UserID: userID, Role: string(role)})
	})
	return mem, err
}

func (p *Postgres) RemoveMember(ctx context.Context, orgID, userID string) (int, error) {
	var remaining int
	err := p.inTx(ctx, func(q *db.Queries) error {
		ms, err := lockedMembers(ctx, q, orgID)
		if err != nil {
			return err
		}
		if remaining, err = checkRemoval(ms, userID); err != nil {
			return err
		}
		return q.DeleteMembership(ctx, db.DeleteMembershipParams{OrgID: orgID, UserID: userID})
	})
	return remaining, err
}
