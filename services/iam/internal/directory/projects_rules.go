package directory

// Project and share rules shared by every ProjectStore implementation.

import (
	"cmp"
	"encoding/base64"
	"fmt"
	"slices"
	"time"

	"github.com/mjcourte/modulith/services/storage"
)

// checkProjectOwner checks that o can own a project: a well-formed user, org
// or modulith owner. A project can't own a project.
func checkProjectOwner(o storage.Owner, invalid error) error {
	if err := o.Validate(); err != nil {
		return fmt.Errorf("%w: %w", invalid, err)
	}
	if o.Kind == storage.OwnerProject {
		return fmt.Errorf("%w: a project can't own a project", invalid)
	}
	return nil
}

// valid reports whether s is a known state.
func (s ProjectState) valid() bool { return s == ProjectReady || s == ProjectCloning }

// normalize validates a project for CreateProject. Whether the owner exists is
// the Store's check; who may create it is the Manager's.
func (p Project) normalize() (Project, error) {
	if err := checkProjectOwner(p.Owner, ErrInvalidProject); err != nil {
		return p, err
	}
	var err error
	if p.Name, err = normalizeName(p.Name, false, ErrInvalidProject); err != nil {
		return p, err
	}
	if p.State == "" {
		p.State = ProjectReady
	}
	switch {
	case !p.State.valid():
		return p, fmt.Errorf("%w: unknown state %q", ErrInvalidProject, p.State)
	case p.ClonedFrom != "" && !validID(p.ClonedFrom):
		return p, fmt.Errorf("%w: cloned-from ID %q", ErrInvalidProject, p.ClonedFrom)
	case p.State == ProjectCloning && p.ClonedFrom == "":
		return p, fmt.Errorf("%w: a clone must name its source", ErrInvalidProject)
	case p.CreatedBy != "" && checkUserID(p.CreatedBy) != nil:
		return p, fmt.Errorf("%w: creator ID %q", ErrInvalidProject, p.CreatedBy)
	case p.OrgLocked:
		if err := checkOrgLock(p); err != nil {
			return p, err
		}
	}
	return p, nil
}

// checkTransfer validates TransferProject: the new owner must be able to own a
// project, and templates neither come from nor go to users and orgs (they're
// made by tooling).
func checkTransfer(p Project, to storage.Owner) error {
	if err := checkProjectOwner(to, ErrInvalidProject); err != nil {
		return err
	}
	if p.Owner.Kind == storage.OwnerModulith || to.Kind == storage.OwnerModulith {
		return fmt.Errorf("%w: modulith templates can't change owner", ErrInvalidProject)
	}
	return nil
}

// checkOrgLock refuses locking a project its org doesn't own: the lock is an
// org's policy for its own work.
func checkOrgLock(p Project) error {
	if p.Owner.Kind != storage.OwnerOrg {
		return fmt.Errorf("%w: only org projects can be org-locked", ErrInvalidProject)
	}
	return nil
}

// shareActive reports whether a share of p for a user in org userOrgID ("" if
// none) grants anything: shares are suspended on a locked project unless the
// user belongs to its org.
func shareActive(p Project, userOrgID string) bool {
	return !p.OrgLocked || (p.Owner.Kind == storage.OwnerOrg && p.Owner.ID == userOrgID)
}

// sortShares puts shares oldest first, ties by user ID.
func sortShares(shares []Share) {
	slices.SortFunc(shares, func(a, b Share) int {
		if c := a.CreatedAt.Compare(b.CreatedAt); c != 0 {
			return c
		}
		return cmp.Compare(a.UserID, b.UserID)
	})
}

// valid reports whether a is a level a share can carry: viewer or editor
// (AccessNone is "no share", not a share).
func (a Access) valid() bool { return a == AccessViewer || a == AccessEditor }

// normalize validates a share for ShareProject. Whether the project and the
// user exist is the Store's check (with checkShareable); who may share is the
// Manager's.
func (s Share) normalize() (Share, error) {
	if !s.Access.valid() {
		return s, fmt.Errorf("%w: access must be viewer or editor, got %q", ErrInvalidShare, s.Access)
	}
	if checkUserID(s.UserID) != nil {
		return s, fmt.Errorf("%w: user ID %q", ErrInvalidShare, s.UserID)
	}
	if s.SharedBy == "" {
		return s, fmt.Errorf("%w: sharer is required", ErrInvalidShare)
	}
	return s, nil
}

// normalizePending validates a pending share for SharePending, lowercasing
// its email.
func (s Share) normalizePending() (Share, error) {
	if !s.Access.valid() {
		return s, fmt.Errorf("%w: access must be viewer or editor, got %q", ErrInvalidShare, s.Access)
	}
	var err error
	if s.Email, err = normalizeEmail(s.Email, ErrInvalidShare); err != nil {
		return s, err
	}
	switch {
	case s.UserID != "":
		return s, fmt.Errorf("%w: a pending share names an email, not a user", ErrInvalidShare)
	case s.SharedBy == "":
		return s, fmt.Errorf("%w: sharer is required", ErrInvalidShare)
	case s.ExpiresAt.IsZero():
		return s, fmt.Errorf("%w: expiry is required", ErrInvalidShare)
	}
	return s, nil
}

// expired reports whether a pending share can no longer be claimed at the
// given time.
func (s Share) expired(at time.Time) bool { return !at.Before(s.ExpiresAt) }

// sortPendingShares puts pending shares oldest first, ties by email.
func sortPendingShares(shares []Share) {
	slices.SortFunc(shares, func(a, b Share) int {
		if c := a.CreatedAt.Compare(b.CreatedAt); c != 0 {
			return c
		}
		return cmp.Compare(a.Email, b.Email)
	})
}

// checkShareable refuses shares on modulith templates: every signed-in user can
// already see them.
func checkShareable(p Project) error {
	if p.Owner.Kind == storage.OwnerModulith {
		return fmt.Errorf("%w: modulith templates are visible to everyone and can't be shared", ErrInvalidShare)
	}
	return nil
}

// normalize validates a listing: exactly one scope, a sane page size.
func (q ProjectQuery) normalize() (ProjectQuery, error) {
	hasUser, hasOwner := q.VisibleTo != "", !q.Owner.IsZero()
	if hasUser == hasOwner {
		return q, fmt.Errorf("%w: set exactly one of VisibleTo or Owner", ErrInvalidQuery)
	}
	if hasUser && checkUserID(q.VisibleTo) != nil {
		return q, fmt.Errorf("%w: user ID %q", ErrInvalidQuery, q.VisibleTo)
	}
	if hasOwner {
		if err := checkProjectOwner(q.Owner, ErrInvalidQuery); err != nil {
			return q, err
		}
	}
	switch {
	case q.PageSize < 0:
		return q, fmt.Errorf("%w: negative PageSize", ErrInvalidQuery)
	case q.PageSize == 0:
		q.PageSize = DefaultPageSize
	case q.PageSize > MaxPageSize:
		q.PageSize = MaxPageSize
	}
	return q, nil
}

// Paging: projects are ordered by ID. UUIDv7 strings sort in creation order,
// so a page token only needs the last ID seen. It's base64-encoded so callers
// treat it as opaque and don't build tokens themselves (as in storage).

// afterID returns the ID the requested page starts after, or "" for the first page.
func (q ProjectQuery) afterID() (string, error) {
	if q.PageToken == "" {
		return "", nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(q.PageToken)
	if err != nil || !validID(string(raw)) {
		return "", fmt.Errorf("%w: bad page token", ErrInvalidQuery)
	}
	return string(raw), nil
}

// projectPageOf turns up to pageSize+1 matches, sorted by ID, into a page; the
// extra match only signals that another page exists.
func projectPageOf(matches []Project, pageSize int) ProjectPage {
	if len(matches) <= pageSize {
		return ProjectPage{Projects: matches}
	}
	matches = matches[:pageSize]
	last := matches[pageSize-1].ID
	return ProjectPage{Projects: matches, NextPageToken: base64.RawURLEncoding.EncodeToString([]byte(last))}
}
