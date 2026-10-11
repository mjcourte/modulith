package iam

// Invite rules shared by every InviteStore implementation.

import (
	"cmp"
	"fmt"
	"slices"
	"time"
)

// normalize validates an invite for CreateInvite. Whether the org exists is
// the Store's check; whether the inviter may invite is the Manager's.
func (inv Invite) normalize() (Invite, error) {
	if !validID(inv.OrgID) {
		return inv, fmt.Errorf("%w: org ID %q", ErrInvalidInvite, inv.OrgID)
	}
	var err error
	if inv.Email, err = normalizeEmail(inv.Email, ErrInvalidInvite); err != nil {
		return inv, err
	}
	if err := checkRole(inv.Role, ErrInvalidInvite); err != nil {
		return inv, err
	}
	switch {
	case inv.InvitedBy == "":
		return inv, fmt.Errorf("%w: inviter is required", ErrInvalidInvite)
	case inv.ExpiresAt.IsZero():
		return inv, fmt.Errorf("%w: expiry is required", ErrInvalidInvite)
	}
	return inv, nil
}

// expired reports whether the invite can no longer be accepted at the given time.
func (inv Invite) expired(at time.Time) bool {
	return !at.Before(inv.ExpiresAt)
}

// sortInvites puts invites newest first. UUIDv7 IDs sort by creation time, so
// the ID alone gives the order (and re-inviting, which keeps the ID, keeps
// the invite's place).
func sortInvites(invites []Invite) {
	slices.SortFunc(invites, func(a, b Invite) int { return cmp.Compare(b.ID, a.ID) })
}
