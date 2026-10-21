package directory

// MemoryStore's DeletionStore methods (struct and lock in memory.go).

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/mjcourte/modulith/services/storage"
)

func (m *MemoryStore) MarkUserDeleting(ctx context.Context, userID, toOrgID string) (User, error) {
	if toOrgID != "" && !validID(toOrgID) {
		return User{}, fmt.Errorf("%w: destination org ID %q", ErrInvalidUser, toOrgID)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.users[userID]
	if !ok {
		return User{}, ErrUserNotFound
	}
	if u.DeletingAt == nil { // the first mark decides; later ones change nothing
		t := now()
		u.DeletingAt, u.DeletingToOrg = &t, toOrgID
		m.users[userID] = u
	}
	return u, nil
}

func (m *MemoryStore) MarkOrgDeleting(ctx context.Context, orgID string) (Org, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	o, ok := m.orgs[orgID]
	if !ok {
		return Org{}, ErrOrgNotFound
	}
	t := now()
	if o.DeletingAt == nil {
		o.DeletingAt = &t
		m.orgs[orgID] = o
	}
	// Its projects go with it, in the same step: nothing can reach them
	// through the org meanwhile, and none can be shared or created there.
	owner := storage.Owner{Kind: storage.OwnerOrg, ID: orgID}
	for id, p := range m.projects {
		if p.Owner == owner && p.DeletingAt == nil {
			p.DeletingAt = &t
			m.projects[id] = p
		}
	}
	return o, nil
}

func (m *MemoryStore) MarkProjectDeleting(ctx context.Context, projectID string) (Project, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.projects[projectID]
	if !ok {
		return Project{}, ErrProjectNotFound
	}
	if p.DeletingAt == nil {
		t := now()
		p.DeletingAt = &t
		m.projects[projectID] = p
	}
	return p, nil
}

// oldestDeletingFirst orders rows by when their deletion started, then by ID.
func oldestDeletingFirst(aAt, bAt *time.Time, aID, bID string) int {
	if c := aAt.Compare(*bAt); c != 0 {
		return c
	}
	return cmp.Compare(aID, bID)
}

func (m *MemoryStore) ListDeletingUsers(ctx context.Context, limit int) ([]User, error) {
	if err := checkLimit(limit); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []User
	for _, u := range m.users {
		if u.DeletingAt != nil {
			out = append(out, u)
		}
	}
	slices.SortFunc(out, func(a, b User) int { return oldestDeletingFirst(a.DeletingAt, b.DeletingAt, a.ID, b.ID) })
	return out[:min(limit, len(out))], nil
}

func (m *MemoryStore) ListDeletingOrgs(ctx context.Context, limit int) ([]Org, error) {
	if err := checkLimit(limit); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Org
	for _, o := range m.orgs {
		if o.DeletingAt != nil {
			out = append(out, o)
		}
	}
	slices.SortFunc(out, func(a, b Org) int { return oldestDeletingFirst(a.DeletingAt, b.DeletingAt, a.ID, b.ID) })
	return out[:min(limit, len(out))], nil
}

func (m *MemoryStore) ListDeletingProjects(ctx context.Context, limit int) ([]Project, error) {
	if err := checkLimit(limit); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Project
	for _, p := range m.projects {
		if p.DeletingAt != nil {
			out = append(out, p)
		}
	}
	slices.SortFunc(out, func(a, b Project) int { return oldestDeletingFirst(a.DeletingAt, b.DeletingAt, a.ID, b.ID) })
	return out[:min(limit, len(out))], nil
}

func (m *MemoryStore) ListOwnedProjects(ctx context.Context, owner storage.Owner, limit int) ([]string, error) {
	if err := checkLimit(limit); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var ids []string
	for id, p := range m.projects {
		if p.Owner == owner {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	return ids[:min(limit, len(ids))], nil
}

func (m *MemoryStore) TransferUserProjects(ctx context.Context, userID, toOrgID string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.liveOrg(toOrgID) {
		return 0, ErrOrgNotFound
	}
	from := storage.Owner{Kind: storage.OwnerUser, ID: userID}
	to := storage.Owner{Kind: storage.OwnerOrg, ID: toOrgID}
	t, n := now(), 0
	for id, p := range m.projects {
		if p.Owner == from {
			p.Owner, p.UpdatedAt = to, t // user projects are never locked, so none is
			m.projects[id] = p
			n++
		}
	}
	return n, nil
}

func (m *MemoryStore) ForgetUser(ctx context.Context, userID string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.users[userID]
	switch {
	case !ok:
		return 0, nil // already deleted: nothing left to forget
	case u.DeletingAt == nil:
		return 0, fmt.Errorf("%w: user %s", ErrNotDeleting, userID)
	}
	n := 0
	for key, s := range m.shares {
		switch {
		case key.userID == userID:
			delete(m.shares, key)
			n++
		case s.SharedBy == userID:
			s.SharedBy = ""
			m.shares[key] = s
			n++
		}
	}
	for id, inv := range m.invites {
		switch {
		case inv.Email == u.Email:
			delete(m.invites, id)
			n++
		case inv.InvitedBy == userID:
			inv.InvitedBy = ""
			m.invites[id] = inv
			n++
		}
	}
	for key, s := range m.pending {
		switch {
		case key.email == u.Email:
			delete(m.pending, key)
			n++
		case s.SharedBy == userID:
			s.SharedBy = ""
			m.pending[key] = s
			n++
		}
	}
	for id, p := range m.projects {
		if p.CreatedBy == userID {
			p.CreatedBy = ""
			m.projects[id] = p
			n++
		}
	}
	return n, nil
}

func (m *MemoryStore) DeleteProject(ctx context.Context, projectID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.projects[projectID]
	switch {
	case !ok:
		return nil // already gone
	case p.DeletingAt == nil:
		return fmt.Errorf("%w: project %s", ErrNotDeleting, projectID)
	}
	for key := range m.shares {
		if key.projectID == projectID {
			delete(m.shares, key)
		}
	}
	for key := range m.pending {
		if key.projectID == projectID {
			delete(m.pending, key)
		}
	}
	delete(m.projects, projectID)
	return nil
}

func (m *MemoryStore) DeleteOrg(ctx context.Context, orgID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	o, ok := m.orgs[orgID]
	switch {
	case !ok:
		return nil
	case o.DeletingAt == nil:
		return fmt.Errorf("%w: org %s", ErrNotDeleting, orgID)
	}
	owner := storage.Owner{Kind: storage.OwnerOrg, ID: orgID}
	for _, p := range m.projects {
		if p.Owner == owner {
			return fmt.Errorf("%w: org %s still owns projects", ErrInvalidOrg, orgID)
		}
	}
	for userID, mem := range m.members {
		if mem.OrgID == orgID {
			delete(m.members, userID)
		}
	}
	for id, inv := range m.invites {
		if inv.OrgID == orgID {
			delete(m.invites, id)
		}
	}
	delete(m.orgs, orgID)
	return nil
}

func (m *MemoryStore) DeleteUser(ctx context.Context, userID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.users[userID]
	switch {
	case !ok:
		return nil
	case u.DeletingAt == nil:
		return fmt.Errorf("%w: user %s", ErrNotDeleting, userID)
	}
	if _, member := m.members[userID]; member {
		return fmt.Errorf("%w: user %s still belongs to an org", ErrInvalidUser, userID)
	}
	owner := storage.Owner{Kind: storage.OwnerUser, ID: userID}
	for _, p := range m.projects {
		if p.Owner == owner {
			return fmt.Errorf("%w: user %s still owns projects", ErrInvalidUser, userID)
		}
	}
	for key := range m.shares {
		if key.userID == userID {
			delete(m.shares, key) // normally gone already (ForgetUser)
		}
	}
	delete(m.emails, u.Email)
	delete(m.users, userID)
	return nil
}

func (m *MemoryStore) DeleteExpiredInvites(ctx context.Context, before time.Time, limit int) (int, error) {
	if err := checkLimit(limit); err != nil {
		return 0, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var due []Invite
	for _, inv := range m.invites {
		if inv.expired(before) {
			due = append(due, inv)
		}
	}
	slices.SortFunc(due, func(a, b Invite) int { return a.ExpiresAt.Compare(b.ExpiresAt) })
	due = due[:min(limit, len(due))]
	for _, inv := range due {
		delete(m.invites, inv.ID)
	}
	return len(due), nil
}

func (m *MemoryStore) DeleteExpiredPendingShares(ctx context.Context, before time.Time, limit int) (int, error) {
	if err := checkLimit(limit); err != nil {
		return 0, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var due []pendingKey
	for key, s := range m.pending {
		if s.expired(before) {
			due = append(due, key)
		}
	}
	slices.SortFunc(due, func(a, b pendingKey) int { return m.pending[a].ExpiresAt.Compare(m.pending[b].ExpiresAt) })
	due = due[:min(limit, len(due))]
	for _, key := range due {
		delete(m.pending, key)
	}
	return len(due), nil
}
