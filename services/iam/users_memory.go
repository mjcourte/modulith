package iam

// MemoryStore's UserStore methods (struct and lock in memory.go).

import "context"

func (m *MemoryStore) UpsertUser(ctx context.Context, id Identity) (User, bool, error) {
	id, err := id.normalize() // no shared state needed, so before taking the lock
	if err != nil {
		return User{}, false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	if owner, ok := m.emails[id.Email]; ok && owner != id.UserID {
		return User{}, false, ErrEmailTaken
	}
	u, exists := m.users[id.UserID]
	profile := User{
		ID: id.UserID, Email: id.Email, EmailVerified: id.EmailVerified,
		Name: id.Name, ProviderAvatarURL: id.ProviderAvatarURL,
	}
	if !exists {
		profile.CreatedAt = now()
		profile.UpdatedAt = profile.CreatedAt
		m.users[id.UserID] = profile
		m.emails[id.Email] = id.UserID
		return profile, true, nil
	}

	// A returning user: refresh the profile, but only a real change moves
	// UpdatedAt (every sign-in calls this).
	profile.CreatedAt, profile.UpdatedAt = u.CreatedAt, u.UpdatedAt
	if profile != u {
		profile.UpdatedAt = now()
		if u.Email != id.Email {
			delete(m.emails, u.Email) // the old address is free for others
			m.emails[id.Email] = id.UserID
		}
		m.users[id.UserID] = profile
	}
	return profile, false, nil
}

func (m *MemoryStore) GetUser(ctx context.Context, id string) (User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.users[id]
	if !ok {
		return User{}, ErrUserNotFound
	}
	return u, nil
}

func (m *MemoryStore) GetUserByEmail(ctx context.Context, email string) (User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id, ok := m.emails[emailKey(email)]
	if !ok {
		return User{}, ErrUserNotFound
	}
	return m.users[id], nil
}
