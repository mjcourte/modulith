package iam

import (
	"context"
	"errors"
	"time"
)

// User errors returned by every Store (see UserStore).
var (
	ErrUserNotFound = errors.New("iam: user not found")
	ErrInvalidUser  = errors.New("iam: invalid user")
	// ErrEmailTaken: another user already signed in with this email address.
	// Emails are unique, so sharing and invites can find people by email.
	ErrEmailTaken = errors.New("iam: email already used by another user")
)

// Limits on user fields.
const (
	MaxUserIDLen = 128  // SuperTokens IDs are UUIDs; this leaves room
	MaxEmailLen  = 254  // the longest address SMTP can carry
	MaxNameLen   = 200  // display names, org names
	MaxURLLen    = 2048 // provider avatar URLs
)

// User is a person who has signed in. Users are created on their first
// sign-in; the gateway authenticates them (SuperTokens, Google) and
// iam only keeps their profile.
type User struct {
	ID                string // the SuperTokens user ID
	Email             string // lowercased; unique among users
	EmailVerified     bool   // as reported by the identity provider
	Name              string
	ProviderAvatarURL string // the identity provider's picture (Google's), https; "" if none
	CreatedAt         time.Time
	UpdatedAt         time.Time // last profile change
	// DeletingAt is set when account deletion starts (see DeletionStore); from
	// then on the user counts as gone. DeletingToOrg is the org their files
	// and projects move to ("" if they had none), fixed at that moment so a
	// resumed deletion makes the same choice.
	DeletingAt    *time.Time
	DeletingToOrg string
}

// Identity is what the gateway learns about a user at sign-in.
type Identity struct {
	UserID            string
	Email             string
	EmailVerified     bool
	Name              string
	ProviderAvatarURL string
}

// UserStore persists users. Like the rest of Store it is pure data: shape and
// consistency only, no authorization.
type UserStore interface {
	// UpsertUser records a sign-in: it creates the user the first time and
	// updates their profile (email, verified flag, name, provider avatar URL) afterwards.
	// created reports which. An email already used by another user is
	// ErrEmailTaken; a malformed identity is ErrInvalidUser.
	UpsertUser(ctx context.Context, id Identity) (u User, created bool, err error)
	// GetUser returns the user, or ErrUserNotFound.
	GetUser(ctx context.Context, id string) (User, error)
	// GetUserByEmail finds a user by email address (any letter case), or
	// ErrUserNotFound.
	GetUserByEmail(ctx context.Context, email string) (User, error)
}
