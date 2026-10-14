package manager

// Projects and sharing. Who may do what:
//   - create: for yourself, or for your org (any member; they choose the
//     org-lock). Templates (gyoza) only through CreateTemplate, service to service
//   - see: anyone with access (projectAccess: owner, org member, share, template)
//   - manage (rename, move to the org): the owning user, or admins of the
//     owning org; templates are never changed this way
//   - share and change or remove others' shares: anyone with editor access;
//     anyone can remove their own share (leave the project)
//   - org-lock: admins of the owning org
// Refusals are ErrForbidden, whether or not the project exists.

import (
	"context"
	"errors"
	"fmt"

	"gyoza/services/iam/internal/directory"
	"gyoza/services/storage"
)

// ProjectView is a project as one user sees it: with their access level, so
// the UI knows what to offer (share dialog for editors, read-only for viewers).
type ProjectView struct {
	Project directory.Project
	Access  directory.Access
}

// access returns the project and actorID's access to it, requiring at least
// want. No access, or no such project, is ErrForbidden either way.
func (m *Manager) access(ctx context.Context, actorID, projectID string, want directory.Access) (directory.Project, directory.Access, error) {
	// accessTo already gives unknown users no access, so no separate actor check.
	p, a, err := directory.AccessTo(ctx, m.store, actorID, projectID)
	if errors.Is(err, directory.ErrProjectNotFound) || (err == nil && !a.AtLeast(want)) {
		return directory.Project{}, directory.AccessNone, fmt.Errorf("%w: no %s access to this project", ErrForbidden, want)
	}
	return p, a, err
}

// manageable returns the project if actorID may manage it: the owning user,
// or an admin of the owning org.
func (m *Manager) manageable(ctx context.Context, actorID, projectID string) (directory.Project, error) {
	p, _, err := m.access(ctx, actorID, projectID, directory.AccessViewer)
	if err != nil {
		return directory.Project{}, err
	}
	switch p.Owner.Kind {
	case storage.OwnerUser:
		if p.Owner.ID == actorID {
			return p, nil
		}
	case storage.OwnerOrg:
		if _, err := m.admin(ctx, actorID, p.Owner.ID); err == nil {
			return p, nil
		} else if !errors.Is(err, ErrForbidden) {
			return directory.Project{}, err
		}
	}
	return directory.Project{}, fmt.Errorf("%w: only the project's owner (or an admin of its org) can do that", ErrForbidden)
}

// CreateProject creates a project owned by the acting user or by their org
// (any member may). orgLocked applies to org projects only (the UI offers it,
// on by default). Gyoza templates are made with CreateTemplate.
func (m *Manager) CreateProject(ctx context.Context, actorID string, owner storage.Owner, name string, orgLocked bool) (directory.Project, error) {
	if err := m.canCreateFor(ctx, actorID, owner); err != nil {
		return directory.Project{}, err
	}
	return m.store.CreateProject(ctx, directory.Project{Owner: owner, Name: name, OrgLocked: orgLocked, CreatedBy: actorID})
}

// canCreateFor checks actorID may create projects owned by owner: their own
// library, or the org they're a member of. Creating and cloning share it.
func (m *Manager) canCreateFor(ctx context.Context, actorID string, owner storage.Owner) error {
	if _, err := m.actor(ctx, actorID); err != nil {
		return err
	}
	switch owner.Kind {
	case storage.OwnerUser:
		if owner.ID == actorID {
			return nil
		}
	case storage.OwnerOrg:
		_, err := m.member(ctx, actorID, owner.ID)
		return err
	}
	return fmt.Errorf("%w: you can only create projects for yourself or your org", ErrForbidden)
}

// CreateTemplate creates a gyoza template: readable by every signed-in user,
// changed by nobody. Service to service only (seeding/admin tooling); the
// gateway must never expose it.
func (m *Manager) CreateTemplate(ctx context.Context, name string) (directory.Project, error) {
	return m.store.CreateProject(ctx, directory.Project{Owner: storage.Owner{Kind: storage.OwnerGyoza}, Name: name})
}

// GetProject returns a project the acting user can see, with their access.
func (m *Manager) GetProject(ctx context.Context, actorID, projectID string) (ProjectView, error) {
	p, a, err := m.access(ctx, actorID, projectID, directory.AccessViewer)
	if err != nil {
		return ProjectView{}, err
	}
	return ProjectView{Project: p, Access: a}, nil
}

// ListMyProjects lists the acting user's projects: theirs, their org's and
// those shared with them, oldest first, a page at a time.
func (m *Manager) ListMyProjects(ctx context.Context, actorID string, pageSize int, pageToken string) (directory.ProjectPage, error) {
	if _, err := m.actor(ctx, actorID); err != nil {
		return directory.ProjectPage{}, err
	}
	return m.store.ListProjects(ctx, directory.ProjectQuery{VisibleTo: actorID, PageSize: pageSize, PageToken: pageToken})
}

// ListTemplates lists the gyoza templates, for any signed-in user.
func (m *Manager) ListTemplates(ctx context.Context, actorID string, pageSize int, pageToken string) (directory.ProjectPage, error) {
	if _, err := m.actor(ctx, actorID); err != nil {
		return directory.ProjectPage{}, err
	}
	return m.store.ListProjects(ctx, directory.ProjectQuery{Owner: storage.Owner{Kind: storage.OwnerGyoza}, PageSize: pageSize, PageToken: pageToken})
}

// RenameProject renames a project; its owner (or an admin of the owning org).
func (m *Manager) RenameProject(ctx context.Context, actorID, projectID, name string) (directory.Project, error) {
	if _, err := m.manageable(ctx, actorID, projectID); err != nil {
		return directory.Project{}, err
	}
	return m.store.RenameProject(ctx, projectID, name)
}

// MoveProjectToOrg gives the acting user's own project to their org. Shares
// are kept; the project starts unlocked (an org admin can lock it).
func (m *Manager) MoveProjectToOrg(ctx context.Context, actorID, projectID string) (directory.Project, error) {
	p, err := m.manageable(ctx, actorID, projectID)
	if err != nil {
		return directory.Project{}, err
	}
	if p.Owner.Kind != storage.OwnerUser {
		return directory.Project{}, fmt.Errorf("%w: only personal projects can move to an org", directory.ErrInvalidProject)
	}
	mem, err := m.store.GetMembership(ctx, actorID)
	if err != nil {
		return directory.Project{}, err // ErrNotMember: they have no org to move it to
	}
	return m.store.TransferProject(ctx, projectID, storage.Owner{Kind: storage.OwnerOrg, ID: mem.OrgID})
}

// SetOrgLocked locks or unlocks an org project; admins of the owning org only.
func (m *Manager) SetOrgLocked(ctx context.Context, actorID, projectID string, locked bool) (directory.Project, error) {
	p, _, err := m.access(ctx, actorID, projectID, directory.AccessViewer)
	if err != nil {
		return directory.Project{}, err
	}
	if p.Owner.Kind != storage.OwnerOrg {
		return directory.Project{}, fmt.Errorf("%w: only org projects can be org-locked", directory.ErrInvalidProject)
	}
	if _, err := m.admin(ctx, actorID, p.Owner.ID); err != nil {
		return directory.Project{}, err
	}
	return m.store.SetOrgLocked(ctx, projectID, locked)
}

// ShareProject gives the user with this email access to the project, or
// changes their level; anyone with editor access may. An address with no
// verified account gets a pending share instead, claimed when someone signs
// in with it (SignIn), so sharing never says whether an address has an
// account. Sharing with someone who already has more access changes nothing
// they can do (they keep the greater). A locked project refuses people
// outside its org; templates can't be shared.
func (m *Manager) ShareProject(ctx context.Context, actorID, projectID, email string, access directory.Access) (directory.Share, error) {
	if _, _, err := m.access(ctx, actorID, projectID, directory.AccessEditor); err != nil {
		return directory.Share{}, err
	}
	u, err := m.store.GetUserByEmail(ctx, email)
	switch {
	case err == nil && u.EmailVerified && u.DeletingAt == nil:
		sh, err := m.store.ShareProject(ctx, directory.Share{ProjectID: projectID, UserID: u.ID, Access: access, SharedBy: actorID})
		if !errors.Is(err, directory.ErrUserNotFound) {
			return sh, err
		} // deleted meanwhile: a pending share, like an unknown address
	case err != nil && !errors.Is(err, directory.ErrUserNotFound):
		return directory.Share{}, err
	}
	// An unverified claim to an address proves nothing (as with invites):
	// the share waits for a verified sign-in.
	return m.store.SharePending(ctx, directory.Share{
		ProjectID: projectID, Email: email, Access: access, SharedBy: actorID,
		ExpiresAt: m.now().Add(m.cfg.ShareTTL),
	})
}

// ListShares lists a project's shares, suspended ones included, then its
// pending shares, expired ones included (to share again); for anyone with
// editor access (who manage them).
func (m *Manager) ListShares(ctx context.Context, actorID, projectID string) ([]directory.Share, error) {
	if _, _, err := m.access(ctx, actorID, projectID, directory.AccessEditor); err != nil {
		return nil, err
	}
	shares, err := m.store.ListShares(ctx, projectID)
	if err != nil {
		return nil, err
	}
	pending, err := m.store.ListPendingShares(ctx, projectID)
	if err != nil {
		return nil, err
	}
	return append(shares, pending...), nil
}

// RevokePendingShare removes the pending share to an email, for anyone with
// editor access. Revoking a missing one is not an error.
func (m *Manager) RevokePendingShare(ctx context.Context, actorID, projectID, email string) error {
	if _, _, err := m.access(ctx, actorID, projectID, directory.AccessEditor); err != nil {
		return err
	}
	return m.store.DeletePendingShare(ctx, projectID, email)
}

// Unshare removes userID's share: anyone with editor access may remove any
// share, and anyone may remove their own (leaving a project shared with them).
func (m *Manager) Unshare(ctx context.Context, actorID, projectID, userID string) error {
	if actorID == userID {
		// Leaving: anyone may remove their own share.
		if _, err := m.actor(ctx, actorID); err != nil {
			return err
		}
	} else if _, _, err := m.access(ctx, actorID, projectID, directory.AccessEditor); err != nil {
		return err
	}
	return m.store.Unshare(ctx, projectID, userID)
}
