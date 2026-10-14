package manager

// Users: signing in, and a user's view of themselves.

import (
	"context"
	"errors"
	"gyoza/services/iam/internal/directory"
)

// SignInResult is what the gateway gets back from SignIn.
type SignInResult struct {
	User    directory.User
	Created bool               // true on the user's first sign-in
	Invites []directory.Invite // pending invites for their (verified) email, to accept or decline
}

// SignIn records a sign-in the gateway has authenticated: it creates the user
// the first time and refreshes their profile after that, claims the pending
// shares to their email, and returns their pending org invites. It's a
// service-to-service call (the gateway's sign-in hook), so it takes no acting
// user.
//
// Invites are only offered, and pending shares only claimed, for verified
// email addresses: both are addressed to an email, so an unverified claim to
// it proves nothing.
func (m *Manager) SignIn(ctx context.Context, id directory.Identity) (SignInResult, error) {
	u, created, err := m.store.UpsertUser(ctx, id)
	if err != nil {
		return SignInResult{}, err
	}
	r := SignInResult{User: u, Created: created}
	if u.EmailVerified {
		// Shares made before they had an account become theirs.
		if _, err := m.store.ClaimPendingShares(ctx, u.ID, u.Email, m.now()); err != nil {
			return SignInResult{}, err
		}
		if r.Invites, err = m.store.ListEmailInvites(ctx, u.Email); err != nil {
			return SignInResult{}, err
		}
	}
	return r, nil
}

// Me is a user's view of themselves: their profile and their org, if any.
type Me struct {
	User       directory.User
	Membership *directory.Member // nil if they're in no org
	Org        *directory.Org    // nil if they're in no org
}

// GetMe returns the acting user's profile and org.
func (m *Manager) GetMe(ctx context.Context, actorID string) (Me, error) {
	u, err := m.actor(ctx, actorID)
	if err != nil {
		return Me{}, err
	}
	me := Me{User: u}
	mem, err := m.store.GetMembership(ctx, actorID)
	switch {
	case errors.Is(err, directory.ErrNotMember):
		return me, nil
	case err != nil:
		return Me{}, err
	}
	o, err := m.store.GetOrg(ctx, mem.OrgID)
	if err != nil {
		return Me{}, err
	}
	me.Membership, me.Org = &mem, &o
	return me, nil
}
