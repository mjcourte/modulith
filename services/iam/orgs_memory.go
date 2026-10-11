package iam

// MemoryStore's OrgStore methods (struct and lock in memory.go).

import "context"

func (m *MemoryStore) CreateOrg(ctx context.Context, o Org, creatorID string) (Org, Member, error) {
	o, err := o.normalize()
	if err != nil {
		return Org{}, Member{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.users[creatorID]; !ok {
		return Org{}, Member{}, ErrUserNotFound
	}
	if _, ok := m.members[creatorID]; ok {
		return Org{}, Member{}, ErrAlreadyMember
	}
	o.ID, o.CreatedAt = newID(), now()
	o.UpdatedAt = o.CreatedAt
	admin := Member{OrgID: o.ID, UserID: creatorID, Role: RoleAdmin, JoinedAt: o.CreatedAt}
	m.orgs[o.ID] = o
	m.members[creatorID] = admin
	return o, admin, nil
}

func (m *MemoryStore) GetOrg(ctx context.Context, id string) (Org, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	o, ok := m.orgs[id]
	if !ok {
		return Org{}, ErrOrgNotFound
	}
	return o, nil
}

func (m *MemoryStore) RenameOrg(ctx context.Context, id, name string) (Org, error) {
	name, err := normalizeName(name, false, ErrInvalidOrg)
	if err != nil {
		return Org{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	o, ok := m.orgs[id]
	if !ok {
		return Org{}, ErrOrgNotFound
	}
	if o.Name != name { // renaming to the same name changes nothing, UpdatedAt included
		o.Name, o.UpdatedAt = name, now()
		m.orgs[id] = o
	}
	return o, nil
}

func (m *MemoryStore) GetMembership(ctx context.Context, userID string) (Member, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	mem, ok := m.members[userID]
	if !ok {
		return Member{}, ErrNotMember
	}
	return mem, nil
}

// membersOf returns orgID's members in ListMembers order, or ErrOrgNotFound.
// It scans every membership: fine for the fake (Postgres has an index).
// The caller must hold m.mu.
func (m *MemoryStore) membersOf(orgID string) ([]Member, error) {
	if _, ok := m.orgs[orgID]; !ok {
		return nil, ErrOrgNotFound
	}
	var ms []Member
	for _, mem := range m.members {
		if mem.OrgID == orgID {
			ms = append(ms, mem)
		}
	}
	sortMembers(ms)
	return ms, nil
}

func (m *MemoryStore) ListMembers(ctx context.Context, orgID string) ([]Member, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.membersOf(orgID)
}

func (m *MemoryStore) SetRole(ctx context.Context, orgID, userID string, role Role) (Member, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ms, err := m.membersOf(orgID)
	if err != nil {
		return Member{}, err
	}
	if err := checkRoleChange(ms, userID, role); err != nil {
		return Member{}, err
	}
	mem := m.members[userID]
	mem.Role = role
	m.members[userID] = mem
	return mem, nil
}

func (m *MemoryStore) RemoveMember(ctx context.Context, orgID, userID string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ms, err := m.membersOf(orgID)
	if err != nil {
		return 0, err
	}
	remaining, err := checkRemoval(ms, userID)
	if err != nil {
		return 0, err
	}
	delete(m.members, userID)
	return remaining, nil
}
