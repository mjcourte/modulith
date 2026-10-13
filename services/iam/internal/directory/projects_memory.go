package directory

// MemoryStore's ProjectStore methods (struct and lock in memory.go).

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"gyoza/services/storage"
)

// ownerExists reports whether o names a live (existing, not being deleted)
// user or org; gyoza always exists. The caller must hold m.mu.
func (m *MemoryStore) ownerExists(o storage.Owner) error {
	switch o.Kind {
	case storage.OwnerUser:
		if !m.liveUser(o.ID) {
			return ErrUserNotFound
		}
	case storage.OwnerOrg:
		if !m.liveOrg(o.ID) {
			return ErrOrgNotFound
		}
	}
	return nil
}

// orgOf returns the ID of userID's org, or "" if they have none. The caller
// must hold m.mu.
func (m *MemoryStore) orgOf(userID string) string {
	return m.members[userID].OrgID // a missing entry's zero value has OrgID ""
}

func (m *MemoryStore) CreateProject(ctx context.Context, p Project) (Project, error) {
	p, err := p.normalize()
	if err != nil {
		return Project{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.ownerExists(p.Owner); err != nil {
		return Project{}, err
	}
	if p.ClonedFrom != "" {
		if !m.liveProject(p.ClonedFrom) {
			return Project{}, fmt.Errorf("clone source: %w", ErrProjectNotFound)
		}
	}
	p.ID, p.CreatedAt = newID(), now()
	p.UpdatedAt = p.CreatedAt
	m.projects[p.ID] = p
	return p, nil
}

func (m *MemoryStore) GetProject(ctx context.Context, id string) (Project, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.projects[id]
	if !ok {
		return Project{}, ErrProjectNotFound
	}
	return p, nil
}

func (m *MemoryStore) ListProjects(ctx context.Context, q ProjectQuery) (ProjectPage, error) {
	q, err := q.normalize()
	if err != nil {
		return ProjectPage{}, err
	}
	after, err := q.afterID()
	if err != nil {
		return ProjectPage{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	// Which projects are in scope: a user's own, their org's and those
	// actively shared with them; or one owner's. Scanning everything is fine
	// for the fake; Postgres uses indexes.
	inScope := func(p Project) bool { return p.Owner == q.Owner }
	if q.VisibleTo != "" {
		userOrg := m.orgOf(q.VisibleTo)
		inScope = func(p Project) bool {
			switch {
			case p.Owner == storage.Owner{Kind: storage.OwnerUser, ID: q.VisibleTo}:
				return true
			case userOrg != "" && p.Owner == storage.Owner{Kind: storage.OwnerOrg, ID: userOrg}:
				return true
			}
			_, shared := m.shares[shareKey{p.ID, q.VisibleTo}]
			return shared && shareActive(p, userOrg)
		}
	}
	var matches []Project
	for _, p := range m.projects {
		if p.State == ProjectReady && p.DeletingAt == nil && p.ID > after && inScope(p) {
			matches = append(matches, p)
		}
	}
	slices.SortFunc(matches, func(a, b Project) int { return strings.Compare(a.ID, b.ID) })
	if len(matches) > q.PageSize+1 {
		matches = matches[:q.PageSize+1] // one extra says another page exists
	}
	return projectPageOf(matches, q.PageSize), nil
}

// update applies change to the project under the lock and stores the result
// if it differs, moving UpdatedAt. A change that leaves the project as it was
// changes nothing, UpdatedAt included, so repeating a call is harmless.
func (m *MemoryStore) update(id string, change func(*Project) error) (Project, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.projects[id]
	if !ok {
		return Project{}, ErrProjectNotFound
	}
	changed := p
	if err := change(&changed); err != nil {
		return Project{}, err
	}
	if changed == p {
		return p, nil
	}
	changed.UpdatedAt = now()
	m.projects[id] = changed
	return changed, nil
}

func (m *MemoryStore) RenameProject(ctx context.Context, id, name string) (Project, error) {
	name, err := normalizeName(name, false, ErrInvalidProject)
	if err != nil {
		return Project{}, err
	}
	return m.update(id, func(p *Project) error {
		p.Name = name
		return nil
	})
}

func (m *MemoryStore) TransferProject(ctx context.Context, id string, to storage.Owner) (Project, error) {
	return m.update(id, func(p *Project) error {
		if err := checkTransfer(*p, to); err != nil {
			return err
		}
		if err := m.ownerExists(to); err != nil {
			return err
		}
		if p.Owner != to {
			p.Owner, p.OrgLocked = to, false // the lock was the old owner's policy
		}
		return nil
	})
}

func (m *MemoryStore) MarkProjectReady(ctx context.Context, id string) (Project, error) {
	return m.update(id, func(p *Project) error {
		p.State = ProjectReady
		return nil
	})
}

func (m *MemoryStore) SetOrgLocked(ctx context.Context, id string, locked bool) (Project, error) {
	return m.update(id, func(p *Project) error {
		if err := checkOrgLock(*p); err != nil {
			return err
		}
		p.OrgLocked = locked
		return nil
	})
}

func (m *MemoryStore) ShareProject(ctx context.Context, s Share) (Share, error) {
	s, err := s.normalize()
	if err != nil {
		return Share{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.projects[s.ProjectID]
	if !ok || p.DeletingAt != nil {
		return Share{}, ErrProjectNotFound
	}
	if err := checkShareable(p); err != nil {
		return Share{}, err
	}
	if !m.liveUser(s.UserID) {
		return Share{}, ErrUserNotFound
	}
	if !shareActive(p, m.orgOf(s.UserID)) {
		return Share{}, fmt.Errorf("%w: the project is locked to its org", ErrInvalidShare)
	}

	key := shareKey{s.ProjectID, s.UserID}
	existing, ok := m.shares[key]
	switch {
	case !ok:
		s.CreatedAt = now()
		s.UpdatedAt = s.CreatedAt
	case existing.Access == s.Access:
		return existing, nil // same level again: nothing changes
	default:
		s.CreatedAt, s.UpdatedAt = existing.CreatedAt, now()
	}
	m.shares[key] = s
	return s, nil
}

func (m *MemoryStore) GetShare(ctx context.Context, projectID, userID string) (Share, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.shares[shareKey{projectID, userID}]
	if !ok {
		return Share{}, ErrShareNotFound
	}
	return s, nil
}

func (m *MemoryStore) ListShares(ctx context.Context, projectID string) ([]Share, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.projects[projectID]; !ok {
		return nil, ErrProjectNotFound
	}
	var out []Share
	for key, s := range m.shares {
		if key.projectID == projectID {
			out = append(out, s)
		}
	}
	sortShares(out)
	return out, nil
}

func (m *MemoryStore) Unshare(ctx context.Context, projectID, userID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.shares, shareKey{projectID, userID}) // missing is fine: safe to retry
	return nil
}

func (m *MemoryStore) ListCloningProjects(ctx context.Context, before time.Time, limit int) ([]Project, error) {
	if err := checkLimit(limit); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Project
	for _, p := range m.projects {
		if p.State == ProjectCloning && p.DeletingAt == nil && p.CreatedAt.Before(before) {
			out = append(out, p)
		}
	}
	slices.SortFunc(out, func(a, b Project) int { return cmp.Or(a.CreatedAt.Compare(b.CreatedAt), cmp.Compare(a.ID, b.ID)) })
	return out[:min(limit, len(out))], nil
}
