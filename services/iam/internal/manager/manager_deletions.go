package manager

// Deletions: a project, an org, an account. Each starts by marking the row
// (access stops at once, see directory.DeletionStore), then works through
// steps that are each safe to repeat, and ends by removing the row. A
// deletion cut off part-way stays marked, so the cleanup loop
// (manager_cleanup.go) finds it and finishes it from wherever it stopped.
//
// Who may:
//   - a project: its owner, or an admin of the owning org; templates only
//     through DeleteTemplate (service to service)
//   - an org: its admins, even with other members in it; or the last member
//     leaving, which deletes it
//   - an account: its user, unless they're their org's only admin and others
//     remain (ErrLastAdmin: promote someone first, as when leaving)

import (
	"context"
	"errors"
	"fmt"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"gyoza/platform/telemetry"
	"gyoza/services/iam/internal/directory"
	"gyoza/services/storage"
)

// cascadeBatch is how many rows one listing in a cascade or cleanup pass takes.
const cascadeBatch = 100

// DeleteProject deletes a project for good (there is no project trash) and
// trashes its files, which storage keeps restorable for its retention period.
func (m *Manager) DeleteProject(ctx context.Context, actorID, projectID string) error {
	if _, err := m.manageable(ctx, actorID, projectID); err != nil {
		return err
	}
	return m.deleteProject(ctx, projectID)
}

// DeleteTemplate deletes a gyoza template. Service to service only
// (seeding/admin tooling); the gateway must never expose it.
func (m *Manager) DeleteTemplate(ctx context.Context, projectID string) error {
	p, err := m.store.GetProject(ctx, projectID)
	if err != nil {
		return err
	}
	if p.Owner.Kind != storage.OwnerGyoza {
		return fmt.Errorf("%w: not a gyoza template", directory.ErrInvalidProject)
	}
	return m.deleteProject(ctx, projectID)
}

// DeleteOrg deletes an org with its projects and files; admins only. Its
// members stay, without an org, keeping their own files and projects.
func (m *Manager) DeleteOrg(ctx context.Context, actorID, orgID string) error {
	if _, err := m.admin(ctx, actorID, orgID); err != nil {
		return err
	}
	return m.deleteOrg(ctx, orgID)
}

// DeleteAccount deletes the acting user (GDPR). With an org, their projects
// and library move to it and they leave it (the last member leaving deletes
// it); without, their projects and library are deleted. Then iam forgets them
// (shares to them, invites to their email, their name on what they created or
// shared), their sign-in account is deleted, and finally their row, freeing
// their email.
func (m *Manager) DeleteAccount(ctx context.Context, actorID string) error {
	if _, err := m.actor(ctx, actorID); err != nil {
		return err
	}
	// Where their things go, decided once: a resumed deletion reads it back
	// rather than deciding again from a membership it may already have removed.
	toOrg, err := directory.ActorOrg(ctx, m.store, actorID) // "" for none, or one being deleted
	if err != nil {
		return err
	}
	if toOrg != "" {
		if err := m.checkNotLastAdmin(ctx, toOrg, actorID); err != nil {
			return err
		}
	}
	if _, err := m.store.MarkUserDeleting(ctx, actorID, toOrg); err != nil {
		return err
	}
	return m.finishUser(ctx, actorID)
}

// checkNotLastAdmin refuses when userID is orgID's only admin and others
// would remain without one.
func (m *Manager) checkNotLastAdmin(ctx context.Context, orgID, userID string) error {
	members, err := m.store.ListMembers(ctx, orgID)
	if err != nil {
		return err
	}
	admins, isAdmin := 0, false
	for _, mem := range members {
		if mem.Role == directory.RoleAdmin {
			admins++
			isAdmin = isAdmin || mem.UserID == userID
		}
	}
	if isAdmin && admins == 1 && len(members) > 1 {
		return fmt.Errorf("%w: promote someone else first", directory.ErrLastAdmin)
	}
	return nil
}

// deleteProject marks the project, trashes its files and removes it with its
// shares. A project already gone is done.
func (m *Manager) deleteProject(ctx context.Context, projectID string) (err error) {
	ctx, span := tracer.Start(ctx, "iam.delete_project", trace.WithAttributes(attribute.String("gyoza.project_id", projectID)))
	defer telemetry.End(span, &err)
	if _, err := m.store.MarkProjectDeleting(ctx, projectID); err != nil {
		if errors.Is(err, directory.ErrProjectNotFound) {
			return nil
		}
		return err
	}
	if _, err := m.storage.DeleteOwned(ctx, storage.Owner{Kind: storage.OwnerProject, ID: projectID}); err != nil {
		return fmt.Errorf("trash files of project %s: %w", projectID, err)
	}
	return m.store.DeleteProject(ctx, projectID)
}

// deleteOrg marks the org (and its projects, in the same step), deletes its
// projects, trashes its files and removes it with its memberships and
// invites. An org already gone is done.
func (m *Manager) deleteOrg(ctx context.Context, orgID string) (err error) {
	ctx, span := tracer.Start(ctx, "iam.delete_org", trace.WithAttributes(attribute.String("gyoza.org_id", orgID)))
	defer telemetry.End(span, &err)
	if _, err := m.store.MarkOrgDeleting(ctx, orgID); err != nil {
		if errors.Is(err, directory.ErrOrgNotFound) {
			return nil
		}
		return err
	}
	owner := storage.Owner{Kind: storage.OwnerOrg, ID: orgID}
	if err := m.deleteOwnedProjects(ctx, owner); err != nil {
		return err
	}
	if _, err := m.storage.DeleteOwned(ctx, owner); err != nil {
		return fmt.Errorf("trash files of org %s: %w", orgID, err)
	}
	return m.store.DeleteOrg(ctx, orgID)
}

// deleteOwnedProjects deletes every project owner owns. Each deletion removes
// the project, so the listing shrinks until it's empty.
func (m *Manager) deleteOwnedProjects(ctx context.Context, owner storage.Owner) error {
	for {
		ids, err := m.store.ListOwnedProjects(ctx, owner, cascadeBatch)
		if err != nil {
			return err
		}
		for _, id := range ids {
			if err := m.deleteProject(ctx, id); err != nil {
				return err
			}
		}
		if len(ids) < cascadeBatch {
			return nil
		}
	}
}

// removeMember takes userID out of orgID and returns how many members
// remain; the last member leaving deletes the org. The org is marked before
// its last member goes, so a failure part-way leaves it to the cleanup loop
// instead of as an empty org nobody can reach.
func (m *Manager) removeMember(ctx context.Context, orgID, userID string) (int, error) {
	members, err := m.store.ListMembers(ctx, orgID)
	if err != nil {
		return 0, err
	}
	if len(members) == 1 && members[0].UserID == userID {
		if _, err := m.store.MarkOrgDeleting(ctx, orgID); err != nil {
			return 0, err
		}
	}
	remaining, err := m.store.RemoveMember(ctx, orgID, userID)
	if err != nil {
		return 0, err
	}
	if remaining == 0 {
		if err := m.deleteOrg(ctx, orgID); err != nil {
			return 0, err
		}
	}
	return remaining, nil
}

// finishUser runs (or resumes) the deletion of a user already marked
// deleting. A user already gone is done.
func (m *Manager) finishUser(ctx context.Context, userID string) (err error) {
	ctx, span := tracer.Start(ctx, "iam.delete_user", trace.WithAttributes(attribute.String("enduser.id", userID)))
	defer telemetry.End(span, &err)
	u, err := m.store.GetUser(ctx, userID)
	if errors.Is(err, directory.ErrUserNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if u.DeletingAt == nil {
		return fmt.Errorf("%w: user %s", directory.ErrNotDeleting, userID)
	}

	toOrg, err := m.liveOrgOrNone(ctx, u.DeletingToOrg)
	if err != nil {
		return err
	}
	if toOrg != "" {
		if _, err := m.store.TransferUserProjects(ctx, userID, toOrg); err != nil {
			return err
		}
		if _, err := m.storage.RemoveUser(ctx, userID, toOrg); err != nil {
			return fmt.Errorf("hand files of user %s to org %s: %w", userID, toOrg, err)
		}
		if err := m.leaveForDeletion(ctx, toOrg, userID); err != nil {
			return err
		}
	} else {
		if err := m.deleteOwnedProjects(ctx, storage.Owner{Kind: storage.OwnerUser, ID: userID}); err != nil {
			return err
		}
		if _, err := m.storage.RemoveUser(ctx, userID, ""); err != nil {
			return fmt.Errorf("trash files of user %s: %w", userID, err)
		}
		// A membership left here can only be of an org being deleted (a
		// live one would have been the destination): finishing that
		// deletion ends it.
		if mem, err := m.store.GetMembership(ctx, userID); err == nil {
			if err := m.deleteOrg(ctx, mem.OrgID); err != nil {
				return err
			}
		} else if !errors.Is(err, directory.ErrNotMember) {
			return err
		}
	}

	if _, err := m.store.ForgetUser(ctx, userID); err != nil {
		return err
	}
	if err := m.idp.DeleteUser(ctx, userID); err != nil {
		return fmt.Errorf("delete sign-in account of user %s: %w", userID, err)
	}
	return m.store.DeleteUser(ctx, userID) // last: the marked row is what lets a cut-off deletion resume
}

// liveOrgOrNone returns orgID if it still exists and isn't being deleted,
// otherwise "". An account deletion whose org went (or is going) meanwhile
// finishes that org's deletion first, which ends the membership, and then
// carries on as if the user had no org.
func (m *Manager) liveOrgOrNone(ctx context.Context, orgID string) (string, error) {
	if orgID == "" {
		return "", nil
	}
	o, err := m.store.GetOrg(ctx, orgID)
	switch {
	case errors.Is(err, directory.ErrOrgNotFound):
		return "", nil
	case err != nil:
		return "", err
	case o.DeletingAt != nil:
		return "", m.deleteOrg(ctx, orgID)
	}
	return orgID, nil
}

// leaveForDeletion takes a user being deleted out of orgID, if they're still
// in it. If the org's other admins have stepped down since the deletion
// began, its longest-standing member is promoted, so the org keeps an admin
// and the deletion can't get stuck.
func (m *Manager) leaveForDeletion(ctx context.Context, orgID, userID string) error {
	if _, err := m.store.GetMembership(ctx, userID); errors.Is(err, directory.ErrNotMember) {
		return nil // left on an earlier run
	} else if err != nil {
		return err
	}
	_, err := m.removeMember(ctx, orgID, userID)
	if !errors.Is(err, directory.ErrLastAdmin) {
		return err
	}
	members, err := m.store.ListMembers(ctx, orgID)
	if err != nil {
		return err
	}
	for _, mem := range members { // admins first, then by join time: the first non-admin is the longest-standing
		if mem.UserID != userID {
			if _, err := m.store.SetRole(ctx, orgID, mem.UserID, directory.RoleAdmin); err != nil {
				return err
			}
			break
		}
	}
	_, err = m.removeMember(ctx, orgID, userID)
	return err
}
