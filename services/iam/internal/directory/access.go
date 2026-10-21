package directory

// Access rules: who may do what with a project. The decision itself is a pure
// function (projectAccess) over the facts that matter; accessTo gathers those
// facts from a Store. The Manager (step 5) and StorageAuthorizer both use it,
// so a project can't be visible through one door and not the other.

import (
	"context"
	"errors"

	"github.com/mjcourte/modulith/services/storage"
)

// rank orders access levels, so the greatest of several can be picked.
func (a Access) rank() int {
	switch a {
	case AccessEditor:
		return 2
	case AccessViewer:
		return 1
	}
	return 0
}

// AtLeast reports whether a grants at least what want does.
func (a Access) AtLeast(want Access) bool { return a.rank() >= want.rank() }

// greatest returns the higher of two access levels.
func greatest(a, b Access) Access {
	if b.rank() > a.rank() {
		return b
	}
	return a
}

// projectAccess decides actorID's access to p. A user can reach a project
// through several routes at once; they get the greatest:
//   - owning user, or member of the owning org: editor
//   - modulith template: viewer for every signed-in user
//   - a share: its level, unless the project is org-locked and they're not in
//     its org (then the share is suspended)
//
// A project that's still cloning, or being deleted, grants nothing. actorOrg is
// the ID of the actor's org ("" if none); share is their share's level
// (AccessNone if none).
func projectAccess(p Project, actorID, actorOrg string, share Access) Access {
	if actorID == "" || p.State != ProjectReady || p.DeletingAt != nil {
		return AccessNone
	}
	access := AccessNone
	switch p.Owner.Kind {
	case storage.OwnerUser:
		if p.Owner.ID == actorID {
			access = AccessEditor
		}
	case storage.OwnerOrg:
		if actorOrg != "" && p.Owner.ID == actorOrg {
			access = AccessEditor
		}
	case storage.OwnerModulith:
		access = AccessViewer
	}
	if shareActive(p, actorOrg) {
		access = greatest(access, share)
	}
	return access
}

// ActorOrg returns the ID of actorID's org, or "" if they're in none. An org
// being deleted counts as none: membership of it grants nothing any more.
func ActorOrg(ctx context.Context, s Store, actorID string) (string, error) {
	m, err := s.GetMembership(ctx, actorID)
	if errors.Is(err, ErrNotMember) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	o, err := s.GetOrg(ctx, m.OrgID)
	if err != nil {
		return "", err
	}
	if o.DeletingAt != nil {
		return "", nil
	}
	return o.ID, nil
}

// AccessTo returns the project and actorID's access to it, from the Store.
// An unknown actor, or one whose account is being deleted, has no access; a
// missing project is ErrProjectNotFound.
// Store failures are returned as errors, never mistaken for "no access".
func AccessTo(ctx context.Context, s Store, actorID, projectID string) (Project, Access, error) {
	p, err := s.GetProject(ctx, projectID)
	if err != nil {
		return Project{}, AccessNone, err
	}
	if actorID == "" {
		return p, AccessNone, nil
	}
	u, err := s.GetUser(ctx, actorID)
	if errors.Is(err, ErrUserNotFound) || (err == nil && u.DeletingAt != nil) {
		return p, AccessNone, nil // unknown, or their account is being deleted
	}
	if err != nil {
		return Project{}, AccessNone, err
	}
	org, err := ActorOrg(ctx, s, actorID)
	if err != nil {
		return Project{}, AccessNone, err
	}
	share := AccessNone
	sh, err := s.GetShare(ctx, projectID, actorID)
	switch {
	case err == nil:
		share = sh.Access
	case !errors.Is(err, ErrShareNotFound):
		return Project{}, AccessNone, err
	}
	return p, projectAccess(p, actorID, org, share), nil
}
