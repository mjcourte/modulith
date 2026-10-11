package iam

import "sync"

// MemoryStore is an in-memory Store for tests and local runs. Its methods are
// split by area: users_memory.go, orgs_memory.go, invites_memory.go,
// projects_memory.go. Every
// method takes the one mutex for its whole duration, so each call is atomic,
// which is what makes the invariants (one org per user, an admin per org)
// hold under concurrent calls.
//
// The stored types have no slices or maps, so copying a value is a full copy:
// callers can never change what's stored through a returned value.
type MemoryStore struct {
	mu       sync.Mutex
	users    map[string]User    // by user ID
	emails   map[string]string  // email -> user ID, keeping emails unique
	orgs     map[string]Org     // by org ID
	members  map[string]Member  // by user ID: a user has at most one org
	invites  map[string]Invite  // by invite ID
	projects map[string]Project // by project ID
	shares   map[shareKey]Share // one per (project, user)
}

// NewMemoryStore returns an empty MemoryStore.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		users:    make(map[string]User),
		emails:   make(map[string]string),
		orgs:     make(map[string]Org),
		members:  make(map[string]Member),
		invites:  make(map[string]Invite),
		projects: make(map[string]Project),
		shares:   make(map[shareKey]Share),
	}
}

// shareKey identifies a share. Any comparable struct can be a map key, so no
// string concatenation (and no separator to get wrong) is needed.
type shareKey struct{ projectID, userID string }

// Compile-time check that *MemoryStore satisfies Store.
var _ Store = (*MemoryStore)(nil)

// liveUser, liveOrg and liveProject report whether a row exists and isn't
// being deleted: only live rows take new references (see DeletionStore).
// The caller must hold m.mu.
func (m *MemoryStore) liveUser(id string) bool {
	u, ok := m.users[id]
	return ok && u.DeletingAt == nil
}

func (m *MemoryStore) liveOrg(id string) bool {
	o, ok := m.orgs[id]
	return ok && o.DeletingAt == nil
}

func (m *MemoryStore) liveProject(id string) bool {
	p, ok := m.projects[id]
	return ok && p.DeletingAt == nil
}
