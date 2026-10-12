package iam

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrForbidden means the acting user may not do what they asked. Manager
// methods return it (wrapped with details) instead of revealing whether the
// thing they asked about exists.
var ErrForbidden = errors.New("iam: forbidden")

// Defaults for Config.
const DefaultInviteTTL = 14 * 24 * time.Hour

// Config is the Manager's policy. Zero values mean "use the default".
type Config struct {
	InviteTTL time.Duration // how long an invite can be accepted; default 14 days
}

func (c Config) withDefaults() (Config, error) {
	if c.InviteTTL == 0 {
		c.InviteTTL = DefaultInviteTTL
	}
	if c.InviteTTL < 0 {
		return c, fmt.Errorf("iam config: InviteTTL %s must be positive", c.InviteTTL)
	}
	return c, nil
}

// Manager is iam's business logic: it decides who may do what, then asks the
// Store, which keeps the data rules. Every method that acts for a user takes
// their ID (actorID) as authenticated by the gateway. Safe for concurrent use.
//
// Later steps add projects (step 5) and the cascades that need storage
// and the identity provider (step 6).
type Manager struct {
	store Store
	cfg   Config
	now   func() time.Time // the clock; tests replace it (export_test.go)
}

// NewManager wires a Manager on store.
func NewManager(store Store, cfg Config) (*Manager, error) {
	if store == nil {
		return nil, errors.New("iam: NewManager needs a store")
	}
	cfg, err := cfg.withDefaults()
	if err != nil {
		return nil, err
	}
	return &Manager{store: store, cfg: cfg, now: time.Now}, nil
}

// Config returns the effective configuration, with defaults filled in.
func (m *Manager) Config() Config { return m.cfg }

// actor returns the acting user. Calls for an unknown or missing user ("" is
// never a user) are forbidden: every real call comes through the gateway after
// sign-in. It's the one place that decides "is this a live user", which step 6
// extends to users whose account is being deleted.
func (m *Manager) actor(ctx context.Context, actorID string) (User, error) {
	u, err := m.store.GetUser(ctx, actorID)
	if errors.Is(err, ErrUserNotFound) {
		return User{}, fmt.Errorf("%w: unknown user", ErrForbidden)
	}
	if err == nil && u.DeletingAt != nil {
		return User{}, fmt.Errorf("%w: account being deleted", ErrForbidden)
	}
	return u, err
}

// member returns actorID's membership of orgID. Not being in that org (or the
// org not existing) is ErrForbidden either way, so outsiders learn nothing
// about which orgs exist.
func (m *Manager) member(ctx context.Context, actorID, orgID string) (Member, error) {
	if _, err := m.actor(ctx, actorID); err != nil {
		return Member{}, err
	}
	mem, err := m.store.GetMembership(ctx, actorID)
	if errors.Is(err, ErrNotMember) || (err == nil && mem.OrgID != orgID) {
		return Member{}, fmt.Errorf("%w: not a member of this org", ErrForbidden)
	}
	if err != nil {
		return Member{}, err
	}
	// An org being deleted is gone as far as its members are concerned.
	if o, err := m.store.GetOrg(ctx, orgID); err != nil {
		return Member{}, err
	} else if o.DeletingAt != nil {
		return Member{}, fmt.Errorf("%w: org being deleted", ErrForbidden)
	}
	return mem, nil
}

// admin returns actorID's membership of orgID if they're one of its admins.
func (m *Manager) admin(ctx context.Context, actorID, orgID string) (Member, error) {
	mem, err := m.member(ctx, actorID, orgID)
	if err != nil {
		return Member{}, err
	}
	if mem.Role != RoleAdmin {
		return Member{}, fmt.Errorf("%w: only org admins can do that", ErrForbidden)
	}
	return mem, nil
}
