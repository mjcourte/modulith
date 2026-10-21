package directory

// Postgres's ProjectStore methods (plumbing in postgres.go).

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mjcourte/modulith/services/iam/internal/db"
	"github.com/mjcourte/modulith/services/storage"
)

// zeroID is the smallest UUID: the cursor for a first page.
const zeroID = "00000000-0000-0000-0000-000000000000"

func (p *Postgres) CreateProject(ctx context.Context, pr Project) (Project, error) {
	pr, err := pr.normalize()
	if err != nil {
		return Project{}, err
	}
	pr.ID, pr.CreatedAt = newID(), now()
	pr.UpdatedAt = pr.CreatedAt
	err = p.inTx(ctx, func(q *db.Queries) error {
		if err := lockLiveOwner(ctx, q, pr.Owner); err != nil {
			return err
		}
		if pr.ClonedFrom != "" {
			src, err := q.GetProjectForShare(ctx, pr.ClonedFrom)
			if noRows(err) || (err == nil && src.DeletingAt != nil) {
				return fmt.Errorf("clone source: %w", ErrProjectNotFound)
			}
			if err != nil {
				return err
			}
		}
		return q.InsertProject(ctx, db.InsertProjectParams{
			ID: pr.ID, OwnerKind: string(pr.Owner.Kind), OwnerID: pr.Owner.ID, Name: pr.Name,
			State: string(pr.State), ClonedFrom: optID(pr.ClonedFrom), OrgLocked: pr.OrgLocked,
			CreatedBy: pr.CreatedBy, CreatedAt: pr.CreatedAt,
		})
	})
	if err != nil {
		return Project{}, err
	}
	return pr, nil
}

func (p *Postgres) GetProject(ctx context.Context, id string) (Project, error) {
	if !validID(id) {
		return Project{}, ErrProjectNotFound
	}
	row, err := p.q.GetProject(ctx, id)
	if noRows(err) {
		return Project{}, ErrProjectNotFound
	}
	if err != nil {
		return Project{}, err
	}
	return projectFromRow(row), nil
}

func (p *Postgres) ListProjects(ctx context.Context, q ProjectQuery) (ProjectPage, error) {
	q, err := q.normalize()
	if err != nil {
		return ProjectPage{}, err
	}
	after, err := q.afterID()
	if err != nil {
		return ProjectPage{}, err
	}
	if after == "" {
		after = zeroID
	}
	lim := int32(q.PageSize + 1) // one extra says another page exists
	var rows []db.IamProject
	if q.VisibleTo != "" {
		org := ""
		switch m, err := p.GetMembership(ctx, q.VisibleTo); {
		case err == nil:
			org = m.OrgID
		case !errors.Is(err, ErrNotMember):
			return ProjectPage{}, err
		}
		rows, err = p.q.ListVisibleProjects(ctx, db.ListVisibleProjectsParams{After: after, UserID: q.VisibleTo, OrgID: org, Lim: lim})
	} else {
		rows, err = p.q.ListProjectsByOwner(ctx, db.ListProjectsByOwnerParams{
			OwnerKind: string(q.Owner.Kind), OwnerID: q.Owner.ID, After: after, Lim: lim,
		})
	}
	if err != nil {
		return ProjectPage{}, err
	}
	return projectPageOf(projectsFromRows(rows), q.PageSize), nil
}

// update is the memory store's update: it applies change to the locked row
// and saves the result if it differs, moving UpdatedAt. A change that leaves
// the project as it was changes nothing, so repeating a call is harmless.
func (p *Postgres) update(ctx context.Context, id string, change func(q *db.Queries, pr *Project) error) (Project, error) {
	if !validID(id) {
		return Project{}, ErrProjectNotFound
	}
	var out Project
	err := p.inTx(ctx, func(q *db.Queries) error {
		row, err := q.GetProjectForUpdate(ctx, id)
		if noRows(err) {
			return ErrProjectNotFound
		}
		if err != nil {
			return err
		}
		pr := projectFromRow(row)
		changed := pr
		if err := change(q, &changed); err != nil {
			return err
		}
		if changed == pr {
			out = pr
			return nil
		}
		changed.UpdatedAt = now()
		out = changed
		return q.SaveProject(ctx, db.SaveProjectParams{
			ID: id, OwnerKind: string(changed.Owner.Kind), OwnerID: changed.Owner.ID, Name: changed.Name,
			State: string(changed.State), OrgLocked: changed.OrgLocked, UpdatedAt: changed.UpdatedAt,
		})
	})
	return out, err
}

func (p *Postgres) RenameProject(ctx context.Context, id, name string) (Project, error) {
	name, err := normalizeName(name, false, ErrInvalidProject)
	if err != nil {
		return Project{}, err
	}
	return p.update(ctx, id, func(_ *db.Queries, pr *Project) error {
		pr.Name = name
		return nil
	})
}

func (p *Postgres) TransferProject(ctx context.Context, id string, to storage.Owner) (Project, error) {
	return p.update(ctx, id, func(q *db.Queries, pr *Project) error {
		if err := checkTransfer(*pr, to); err != nil {
			return err
		}
		if err := lockLiveOwner(ctx, q, to); err != nil {
			return err
		}
		if pr.Owner != to {
			pr.Owner, pr.OrgLocked = to, false // the lock was the old owner's policy
		}
		return nil
	})
}

func (p *Postgres) MarkProjectReady(ctx context.Context, id string) (Project, error) {
	return p.update(ctx, id, func(_ *db.Queries, pr *Project) error {
		pr.State = ProjectReady
		return nil
	})
}

func (p *Postgres) SetOrgLocked(ctx context.Context, id string, locked bool) (Project, error) {
	return p.update(ctx, id, func(_ *db.Queries, pr *Project) error {
		if err := checkOrgLock(*pr); err != nil {
			return err
		}
		pr.OrgLocked = locked
		return nil
	})
}

func (p *Postgres) ShareProject(ctx context.Context, s Share) (Share, error) {
	s, err := s.normalize()
	if err != nil {
		return Share{}, err
	}
	if !validID(s.ProjectID) {
		return Share{}, ErrProjectNotFound
	}
	err = p.inTx(ctx, func(q *db.Queries) error {
		// Locking the project for update also makes shares of one project
		// take turns, so two first shares for a user can't both insert.
		row, err := q.GetProjectForUpdate(ctx, s.ProjectID)
		if noRows(err) || (err == nil && row.DeletingAt != nil) {
			return ErrProjectNotFound
		}
		if err != nil {
			return err
		}
		pr := projectFromRow(row)
		if err := checkShareable(pr); err != nil {
			return err
		}
		if err := lockLiveUser(ctx, q, s.UserID); err != nil {
			return err
		}
		userOrg := ""
		if m, err := q.GetMembership(ctx, s.UserID); err == nil {
			userOrg = m.OrgID
		} else if !noRows(err) {
			return err
		}
		if !shareActive(pr, userOrg) {
			return fmt.Errorf("%w: the project is locked to its org", ErrInvalidShare)
		}

		existing, err := q.GetShare(ctx, db.GetShareParams{ProjectID: s.ProjectID, UserID: s.UserID})
		switch {
		case noRows(err):
			s.CreatedAt = now()
			s.UpdatedAt = s.CreatedAt
			return q.InsertShare(ctx, db.InsertShareParams{
				ProjectID: s.ProjectID, UserID: s.UserID, Access: string(s.Access), SharedBy: s.SharedBy, CreatedAt: s.CreatedAt,
			})
		case err != nil:
			return err
		case Access(existing.Access) == s.Access:
			s = shareFromRow(existing) // same level again: nothing changes
			return nil
		}
		s.CreatedAt, s.UpdatedAt = existing.CreatedAt.UTC(), now()
		return q.UpdateShare(ctx, db.UpdateShareParams{
			ProjectID: s.ProjectID, UserID: s.UserID, Access: string(s.Access), SharedBy: s.SharedBy, UpdatedAt: s.UpdatedAt,
		})
	})
	if err != nil {
		return Share{}, err
	}
	return s, nil
}

func (p *Postgres) GetShare(ctx context.Context, projectID, userID string) (Share, error) {
	if !validID(projectID) {
		return Share{}, ErrShareNotFound
	}
	row, err := p.q.GetShare(ctx, db.GetShareParams{ProjectID: projectID, UserID: userID})
	if noRows(err) {
		return Share{}, ErrShareNotFound
	}
	if err != nil {
		return Share{}, err
	}
	return shareFromRow(row), nil
}

func (p *Postgres) ListShares(ctx context.Context, projectID string) ([]Share, error) {
	if _, err := p.GetProject(ctx, projectID); err != nil {
		return nil, err
	}
	rows, err := p.q.ListShares(ctx, projectID)
	if err != nil {
		return nil, err
	}
	out := make([]Share, len(rows))
	for i, r := range rows {
		out[i] = shareFromRow(r)
	}
	return out, nil
}

func (p *Postgres) Unshare(ctx context.Context, projectID, userID string) error {
	if !validID(projectID) {
		return nil // can't exist, so it's already gone
	}
	return p.q.DeleteShare(ctx, db.DeleteShareParams{ProjectID: projectID, UserID: userID})
}

func (p *Postgres) ListCloningProjects(ctx context.Context, before time.Time, limit int) ([]Project, error) {
	if err := checkLimit(limit); err != nil {
		return nil, err
	}
	rows, err := p.q.ListCloningProjects(ctx, db.ListCloningProjectsParams{Before: before, RowLimit: int32(limit)})
	if err != nil {
		return nil, err
	}
	return projectsFromRows(rows), nil
}

func (p *Postgres) SharePending(ctx context.Context, s Share) (Share, error) {
	s, err := s.normalizePending()
	if err != nil {
		return Share{}, err
	}
	if !validID(s.ProjectID) {
		return Share{}, ErrProjectNotFound
	}
	err = p.inTx(ctx, func(q *db.Queries) error {
		// The project's lock makes pending shares take turns with its
		// shares, its lock and its deletion.
		row, err := q.GetProjectForUpdate(ctx, s.ProjectID)
		if noRows(err) || (err == nil && row.DeletingAt != nil) {
			return ErrProjectNotFound
		}
		if err != nil {
			return err
		}
		pr := projectFromRow(row)
		if err := checkShareable(pr); err != nil {
			return err
		}
		if !shareActive(pr, "") {
			return fmt.Errorf("%w: the project is locked to its org", ErrInvalidShare)
		}
		saved, err := q.UpsertPendingShare(ctx, db.UpsertPendingShareParams{
			ProjectID: s.ProjectID, Email: s.Email, Access: string(s.Access), SharedBy: s.SharedBy,
			At: now(), ExpiresAt: s.ExpiresAt,
		})
		s = pendingShareFromRow(saved)
		return err
	})
	if err != nil {
		return Share{}, err
	}
	return s, nil
}

func (p *Postgres) ListPendingShares(ctx context.Context, projectID string) ([]Share, error) {
	if _, err := p.GetProject(ctx, projectID); err != nil {
		return nil, err
	}
	rows, err := p.q.ListPendingShares(ctx, projectID)
	if err != nil {
		return nil, err
	}
	out := make([]Share, len(rows))
	for i, r := range rows {
		out[i] = pendingShareFromRow(r)
	}
	return out, nil
}

func (p *Postgres) DeletePendingShare(ctx context.Context, projectID, email string) error {
	if !validID(projectID) {
		return nil // can't exist, so it's already gone
	}
	_, err := p.q.DeletePendingShare(ctx, db.DeletePendingShareParams{ProjectID: projectID, Email: emailKey(email)})
	return err
}

func (p *Postgres) ClaimPendingShares(ctx context.Context, userID, email string, at time.Time) (int, error) {
	n := 0
	err := p.inTx(ctx, func(q *db.Queries) error {
		pending, err := q.ListPendingSharesTo(ctx, db.ListPendingSharesToParams{Email: emailKey(email), ExpiresAt: at})
		if err != nil {
			return err
		}
		// Projects first (in ID order), then the user: ShareProject's order.
		projects := make(map[string]Project, len(pending))
		for _, ps := range pending {
			row, err := q.GetProjectForUpdate(ctx, ps.ProjectID)
			if noRows(err) {
				continue
			}
			if err != nil {
				return err
			}
			projects[ps.ProjectID] = projectFromRow(row)
		}
		if err := lockLiveUser(ctx, q, userID); err != nil {
			return err
		}
		userOrg := ""
		if m, err := q.GetMembership(ctx, userID); err == nil {
			userOrg = m.OrgID
		} else if !noRows(err) {
			return err
		}

		for _, ps := range pending {
			pr, ok := projects[ps.ProjectID]
			if !ok || pr.DeletingAt != nil || !shareActive(pr, userOrg) {
				continue // can't apply yet: stays pending
			}
			// Only the transaction that deletes the pending share uses it:
			// one revoked meanwhile grants nothing.
			deleted, err := q.DeletePendingShare(ctx, db.DeletePendingShareParams{ProjectID: ps.ProjectID, Email: ps.Email})
			if err != nil {
				return err
			}
			if deleted == 0 {
				continue
			}
			n++
			existing, err := q.GetShare(ctx, db.GetShareParams{ProjectID: ps.ProjectID, UserID: userID})
			switch {
			case noRows(err):
				err = q.InsertShare(ctx, db.InsertShareParams{
					ProjectID: ps.ProjectID, UserID: userID, Access: ps.Access, SharedBy: ps.SharedBy, CreatedAt: now(),
				})
			case err != nil:
			case Access(existing.Access).AtLeast(Access(ps.Access)):
			default:
				err = q.UpdateShare(ctx, db.UpdateShareParams{
					ProjectID: ps.ProjectID, UserID: userID, Access: ps.Access, SharedBy: ps.SharedBy, UpdatedAt: now(),
				})
			}
			if err != nil {
				return err
			}
		}
		return nil
	})
	return n, err
}
