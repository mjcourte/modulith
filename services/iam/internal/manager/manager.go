package manager

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gyoza/services/iam/internal/directory"
	"gyoza/services/storage"
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

// Storage is what iam needs from storage: for the deletion cascades, trashing
// what an owner owns and handing a departing user's library to their org (or
// trashing it); for clones, copying a project's files. storage.Service satisfies it, in-process or over gRPC.
// Declared here, by the consumer, so iam asks for no more than it uses.
type Storage interface {
	DeleteOwned(ctx context.Context, owner storage.Owner) (storage.OwnerDeletion, error)
	RemoveUser(ctx context.Context, userID, orgID string) (storage.UserRemoval, error)
	// CopyProjectFiles copies a project's files into a clone (resumable).
	CopyProjectFiles(ctx context.Context, actorID, from, to string) (storage.CopyReport, error)
}

// IdentityProvider deletes a user's sign-in account (SuperTokens, through
// the gateway) at the end of an account deletion, so iam never talks to
// SuperTokens itself. Deleting a missing account must succeed, so a resumed
// deletion can repeat it.
type IdentityProvider interface {
	DeleteUser(ctx context.Context, userID string) error
}

// NoIdentityProvider is the IdentityProvider until the gateway (and with it
// SuperTokens) exists: there are no sign-in accounts to delete yet.
type NoIdentityProvider struct{}

func (NoIdentityProvider) DeleteUser(context.Context, string) error { return nil }

// Manager is iam's business logic: it decides who may do what, then asks the
// Store, which keeps the data rules. Every method that acts for a user takes
// their ID (actorID) as authenticated by the gateway. Safe for concurrent use.
type Manager struct {
	store   directory.Store
	storage Storage
	idp     IdentityProvider
	cfg     Config
	now     func() time.Time // the clock; tests replace it (export_test.go)
}

// NewManager wires a Manager on store, calling files and idp in the
// deletion cascades.
func NewManager(store directory.Store, files Storage, idp IdentityProvider, cfg Config) (*Manager, error) {
	switch {
	case store == nil:
		return nil, errors.New("iam: NewManager needs a store")
	case files == nil:
		return nil, errors.New("iam: NewManager needs storage")
	case idp == nil:
		return nil, errors.New("iam: NewManager needs an identity provider")
	}
	cfg, err := cfg.withDefaults()
	if err != nil {
		return nil, err
	}
	return &Manager{store: store, storage: files, idp: idp, cfg: cfg, now: time.Now}, nil
}

// Config returns the effective configuration, with defaults filled in.
func (m *Manager) Config() Config { return m.cfg }

// actor returns the acting user. Calls for an unknown or missing user ("" is
// never a user) are forbidden: every real call comes through the gateway after
// sign-in. It's the one place that decides "is this a live user", which step 6
// extends to users whose account is being deleted.
func (m *Manager) actor(ctx context.Context, actorID string) (directory.User, error) {
	u, err := m.store.GetUser(ctx, actorID)
	if errors.Is(err, directory.ErrUserNotFound) {
		return directory.User{}, fmt.Errorf("%w: unknown user", ErrForbidden)
	}
	if err == nil && u.DeletingAt != nil {
		return directory.User{}, fmt.Errorf("%w: account being deleted", ErrForbidden)
	}
	return u, err
}

// member returns actorID's membership of orgID. Not being in that org (or the
// org not existing) is ErrForbidden either way, so outsiders learn nothing
// about which orgs exist.
func (m *Manager) member(ctx context.Context, actorID, orgID string) (directory.Member, error) {
	if _, err := m.actor(ctx, actorID); err != nil {
		return directory.Member{}, err
	}
	mem, err := m.store.GetMembership(ctx, actorID)
	if errors.Is(err, directory.ErrNotMember) || (err == nil && mem.OrgID != orgID) {
		return directory.Member{}, fmt.Errorf("%w: not a member of this org", ErrForbidden)
	}
	if err != nil {
		return directory.Member{}, err
	}
	// An org being deleted is gone as far as its members are concerned.
	if o, err := m.store.GetOrg(ctx, orgID); err != nil {
		return directory.Member{}, err
	} else if o.DeletingAt != nil {
		return directory.Member{}, fmt.Errorf("%w: org being deleted", ErrForbidden)
	}
	return mem, nil
}

// admin returns actorID's membership of orgID if they're one of its admins.
func (m *Manager) admin(ctx context.Context, actorID, orgID string) (directory.Member, error) {
	mem, err := m.member(ctx, actorID, orgID)
	if err != nil {
		return directory.Member{}, err
	}
	if mem.Role != directory.RoleAdmin {
		return directory.Member{}, fmt.Errorf("%w: only org admins can do that", ErrForbidden)
	}
	return mem, nil
}
