package directory

// Postgres's DeletionStore methods (plumbing in postgres.go).

import (
	"context"
	"fmt"
	"time"

	"gyoza/services/iam/internal/db"
	"gyoza/services/storage"
)

func (p *Postgres) MarkUserDeleting(ctx context.Context, userID, toOrgID string) (User, error) {
	if toOrgID != "" && !validID(toOrgID) {
		return User{}, fmt.Errorf("%w: destination org ID %q", ErrInvalidUser, toOrgID)
	}
	t := now()
	row, err := p.q.MarkUserDeleting(ctx, db.MarkUserDeletingParams{At: &t, ToOrg: optID(toOrgID), ID: userID})
	if noRows(err) {
		return User{}, ErrUserNotFound
	}
	if err != nil {
		return User{}, err
	}
	return userFromRow(row), nil
}

func (p *Postgres) MarkOrgDeleting(ctx context.Context, orgID string) (Org, error) {
	if !validID(orgID) {
		return Org{}, ErrOrgNotFound
	}
	var o Org
	err := p.inTx(ctx, func(q *db.Queries) error {
		t := now()
		row, err := q.MarkOrgDeleting(ctx, db.MarkOrgDeletingParams{At: &t, ID: orgID})
		if noRows(err) {
			return ErrOrgNotFound
		}
		if err != nil {
			return err
		}
		o = orgFromRow(row)
		// Its projects go with it, in the same step. Creating one in the org
		// takes a shared lock on the org row, which the UPDATE above waits
		// for, so none can slip in unmarked.
		return q.MarkOrgProjectsDeleting(ctx, db.MarkOrgProjectsDeletingParams{At: &t, OrgID: orgID})
	})
	return o, err
}

func (p *Postgres) MarkProjectDeleting(ctx context.Context, projectID string) (Project, error) {
	if !validID(projectID) {
		return Project{}, ErrProjectNotFound
	}
	t := now()
	row, err := p.q.MarkProjectDeleting(ctx, db.MarkProjectDeletingParams{At: &t, ID: projectID})
	if noRows(err) {
		return Project{}, ErrProjectNotFound
	}
	if err != nil {
		return Project{}, err
	}
	return projectFromRow(row), nil
}

func (p *Postgres) ListDeletingUsers(ctx context.Context, limit int) ([]User, error) {
	if err := checkLimit(limit); err != nil {
		return nil, err
	}
	rows, err := p.q.ListDeletingUsers(ctx, int32(limit))
	if err != nil {
		return nil, err
	}
	out := make([]User, len(rows))
	for i, r := range rows {
		out[i] = userFromRow(r)
	}
	return out, nil
}

func (p *Postgres) ListDeletingOrgs(ctx context.Context, limit int) ([]Org, error) {
	if err := checkLimit(limit); err != nil {
		return nil, err
	}
	rows, err := p.q.ListDeletingOrgs(ctx, int32(limit))
	if err != nil {
		return nil, err
	}
	out := make([]Org, len(rows))
	for i, r := range rows {
		out[i] = orgFromRow(r)
	}
	return out, nil
}

func (p *Postgres) ListDeletingProjects(ctx context.Context, limit int) ([]Project, error) {
	if err := checkLimit(limit); err != nil {
		return nil, err
	}
	rows, err := p.q.ListDeletingProjects(ctx, int32(limit))
	if err != nil {
		return nil, err
	}
	return projectsFromRows(rows), nil
}

func (p *Postgres) ListOwnedProjects(ctx context.Context, owner storage.Owner, limit int) ([]string, error) {
	if err := checkLimit(limit); err != nil {
		return nil, err
	}
	return p.q.ListOwnedProjects(ctx, db.ListOwnedProjectsParams{OwnerKind: string(owner.Kind), OwnerID: owner.ID, Limit: int32(limit)})
}

func (p *Postgres) TransferUserProjects(ctx context.Context, userID, toOrgID string) (int, error) {
	var n int64
	err := p.inTx(ctx, func(q *db.Queries) error {
		if err := lockLiveOrg(ctx, q, toOrgID); err != nil {
			return err
		}
		var err error
		n, err = q.TransferUserProjects(ctx, db.TransferUserProjectsParams{OrgID: toOrgID, At: now(), UserID: userID})
		return err
	})
	return int(n), err
}

func (p *Postgres) ForgetUser(ctx context.Context, userID string) (int, error) {
	var n int64
	err := p.inTx(ctx, func(q *db.Queries) error {
		row, err := q.GetUserForUpdate(ctx, userID)
		if noRows(err) {
			return nil // already deleted: nothing left to forget
		}
		if err != nil {
			return err
		}
		if row.DeletingAt == nil {
			return fmt.Errorf("%w: user %s", ErrNotDeleting, userID)
		}
		// Deletions before clears, so a row that's both (a share to them that
		// they made themselves) is counted once, as in the memory store.
		steps := []func() (int64, error){
			func() (int64, error) { return q.DeleteSharesTo(ctx, userID) },
			func() (int64, error) { return q.ClearSharedBy(ctx, userID) },
			func() (int64, error) { return q.DeleteInvitesTo(ctx, row.Email) },
			func() (int64, error) { return q.ClearInvitedBy(ctx, userID) },
			func() (int64, error) { return q.DeletePendingSharesTo(ctx, row.Email) },
			func() (int64, error) { return q.ClearPendingSharedBy(ctx, userID) },
			func() (int64, error) { return q.ClearProjectCreator(ctx, userID) },
		}
		for _, step := range steps {
			changed, err := step()
			if err != nil {
				return err
			}
			n += changed
		}
		return nil
	})
	return int(n), err
}

func (p *Postgres) DeleteProject(ctx context.Context, projectID string) error {
	if !validID(projectID) {
		return nil
	}
	return p.inTx(ctx, func(q *db.Queries) error {
		row, err := q.GetProjectForUpdate(ctx, projectID)
		switch {
		case noRows(err):
			return nil // already gone
		case err != nil:
			return err
		case row.DeletingAt == nil:
			return fmt.Errorf("%w: project %s", ErrNotDeleting, projectID)
		}
		if err := q.DeleteProjectPendingShares(ctx, projectID); err != nil {
			return err
		}
		if err := q.DeleteProjectShares(ctx, projectID); err != nil {
			return err
		}
		return q.DeleteProject(ctx, projectID)
	})
}

func (p *Postgres) DeleteOrg(ctx context.Context, orgID string) error {
	if !validID(orgID) {
		return nil
	}
	return p.inTx(ctx, func(q *db.Queries) error {
		row, err := q.GetOrgForUpdate(ctx, orgID)
		switch {
		case noRows(err):
			return nil
		case err != nil:
			return err
		case row.DeletingAt == nil:
			return fmt.Errorf("%w: org %s", ErrNotDeleting, orgID)
		}
		owned, err := q.CountOwnedProjects(ctx, db.CountOwnedProjectsParams{OwnerKind: string(storage.OwnerOrg), OwnerID: orgID})
		if err != nil {
			return err
		}
		if owned > 0 {
			return fmt.Errorf("%w: org %s still owns projects", ErrInvalidOrg, orgID)
		}
		if err := q.DeleteOrgMemberships(ctx, orgID); err != nil {
			return err
		}
		if err := q.DeleteOrgInvites(ctx, orgID); err != nil {
			return err
		}
		return q.DeleteOrg(ctx, orgID)
	})
}

func (p *Postgres) DeleteUser(ctx context.Context, userID string) error {
	return p.inTx(ctx, func(q *db.Queries) error {
		row, err := q.GetUserForUpdate(ctx, userID)
		switch {
		case noRows(err):
			return nil
		case err != nil:
			return err
		case row.DeletingAt == nil:
			return fmt.Errorf("%w: user %s", ErrNotDeleting, userID)
		}
		if _, err := q.GetMembership(ctx, userID); err == nil {
			return fmt.Errorf("%w: user %s still belongs to an org", ErrInvalidUser, userID)
		} else if !noRows(err) {
			return err
		}
		owned, err := q.CountOwnedProjects(ctx, db.CountOwnedProjectsParams{OwnerKind: string(storage.OwnerUser), OwnerID: userID})
		if err != nil {
			return err
		}
		if owned > 0 {
			return fmt.Errorf("%w: user %s still owns projects", ErrInvalidUser, userID)
		}
		if _, err := q.DeleteSharesTo(ctx, userID); err != nil {
			return err
		}
		return q.DeleteUser(ctx, userID)
	})
}

func (p *Postgres) DeleteExpiredInvites(ctx context.Context, before time.Time, limit int) (int, error) {
	if err := checkLimit(limit); err != nil {
		return 0, err
	}
	n, err := p.q.DeleteExpiredInvites(ctx, db.DeleteExpiredInvitesParams{ExpiresAt: before, Limit: int32(limit)})
	return int(n), err
}

func (p *Postgres) DeleteExpiredPendingShares(ctx context.Context, before time.Time, limit int) (int, error) {
	if err := checkLimit(limit); err != nil {
		return 0, err
	}
	n, err := p.q.DeleteExpiredPendingShares(ctx, db.DeleteExpiredPendingSharesParams{ExpiresAt: before, Limit: int32(limit)})
	return int(n), err
}
