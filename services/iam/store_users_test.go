package iam_test

import (
	"errors"
	"testing"

	"gyoza/services/iam"
)

func testUpsertUserCreatesThenUpdates(t *testing.T, s iam.Store) {
	ctx := t.Context()
	id := iam.Identity{UserID: "st-ann", Email: "ann@example.com", EmailVerified: true, Name: "Ann", ProviderAvatarURL: "https://example.com/ann.png"}

	first, created, err := s.UpsertUser(ctx, id)
	if err != nil || !created {
		t.Fatalf("first UpsertUser = %v, created %v; want created", err, created)
	}
	if first.CreatedAt.IsZero() || !first.UpdatedAt.Equal(first.CreatedAt) {
		t.Errorf("new user timestamps: created %v, updated %v", first.CreatedAt, first.UpdatedAt)
	}
	if got, err := s.GetUser(ctx, "st-ann"); err != nil || got != first {
		t.Errorf("GetUser = %+v, %v; want %+v", got, err, first)
	}

	// Signing in again with the same profile changes nothing.
	letTimePass()
	again, created, err := s.UpsertUser(ctx, id)
	if err != nil || created || again != first {
		t.Errorf("same sign-in again = %+v, created %v, %v; want the unchanged user", again, created, err)
	}

	// A changed profile is saved, and only UpdatedAt moves.
	id.Name, id.ProviderAvatarURL = "Ann Lee", ""
	letTimePass()
	changed, created, err := s.UpsertUser(ctx, id)
	if err != nil || created {
		t.Fatalf("changed sign-in = %v, created %v", err, created)
	}
	if changed.Name != "Ann Lee" || changed.ProviderAvatarURL != "" || !changed.CreatedAt.Equal(first.CreatedAt) || !changed.UpdatedAt.After(first.UpdatedAt) {
		t.Errorf("changed user = %+v; want new name, no avatar, same CreatedAt, later UpdatedAt", changed)
	}
	if got, _ := s.GetUser(ctx, "st-ann"); got != changed {
		t.Errorf("stored user = %+v, want %+v", got, changed)
	}
}

func testUpsertUserNormalises(t *testing.T, s iam.Store) {
	ctx := t.Context()
	u, _, err := s.UpsertUser(ctx, iam.Identity{UserID: "st-bob", Email: "  Bob@Example.COM ", Name: "  Bob  "})
	if err != nil {
		t.Fatalf("UpsertUser: %v", err)
	}
	if u.Email != "bob@example.com" || u.Name != "Bob" {
		t.Errorf("stored %q / %q, want bob@example.com / Bob", u.Email, u.Name)
	}
	for _, email := range []string{"bob@example.com", "BOB@example.com", " bob@EXAMPLE.com "} {
		if got, err := s.GetUserByEmail(ctx, email); err != nil || got.ID != "st-bob" {
			t.Errorf("GetUserByEmail(%q) = %+v, %v", email, got, err)
		}
	}
}

func testUpsertUserRejectsInvalid(t *testing.T, s iam.Store) {
	ctx := t.Context()
	for name, id := range map[string]iam.Identity{
		"no user ID":  {Email: "a@example.com"},
		"bad email":   {UserID: "st-a", Email: "not an email"},
		"http avatar": {UserID: "st-a", Email: "a@example.com", ProviderAvatarURL: "http://example.com/a.png"},
	} {
		if _, _, err := s.UpsertUser(ctx, id); !errors.Is(err, iam.ErrInvalidUser) {
			t.Errorf("%s: %v, want ErrInvalidUser", name, err)
		}
	}
	if _, err := s.GetUser(ctx, "st-a"); !errors.Is(err, iam.ErrUserNotFound) {
		t.Errorf("a rejected user was stored: %v", err)
	}
}

func testEmailsUnique(t *testing.T, s iam.Store) {
	ctx := t.Context()
	ann := mustUser(t, s, "ann")

	if _, _, err := s.UpsertUser(ctx, iam.Identity{UserID: "bob", Email: "ANN@example.com"}); !errors.Is(err, iam.ErrEmailTaken) {
		t.Fatalf("second user with ann's email (other case) = %v, want ErrEmailTaken", err)
	}
	if _, err := s.GetUser(ctx, "bob"); !errors.Is(err, iam.ErrUserNotFound) {
		t.Errorf("refused user was stored: %v", err)
	}

	// Ann changes address: the old one is free for someone else.
	if _, _, err := s.UpsertUser(ctx, iam.Identity{UserID: ann.ID, Email: "ann@new.example.com"}); err != nil {
		t.Fatalf("ann changes email: %v", err)
	}
	if got, err := s.GetUserByEmail(ctx, "ann@new.example.com"); err != nil || got.ID != ann.ID {
		t.Errorf("lookup by new email = %+v, %v", got, err)
	}
	if _, _, err := s.UpsertUser(ctx, iam.Identity{UserID: "bob", Email: "ann@example.com"}); err != nil {
		t.Fatalf("bob takes ann's old email: %v", err)
	}
	if got, err := s.GetUserByEmail(ctx, "ann@example.com"); err != nil || got.ID != "bob" {
		t.Errorf("lookup by old email = %+v, %v; want bob", got, err)
	}
}

func testGetUserMissing(t *testing.T, s iam.Store) {
	ctx := t.Context()
	if _, err := s.GetUser(ctx, "nobody"); !errors.Is(err, iam.ErrUserNotFound) {
		t.Errorf("GetUser = %v, want ErrUserNotFound", err)
	}
	for _, email := range []string{"nobody@example.com", "not an email", ""} {
		if _, err := s.GetUserByEmail(ctx, email); !errors.Is(err, iam.ErrUserNotFound) {
			t.Errorf("GetUserByEmail(%q) = %v, want ErrUserNotFound", email, err)
		}
	}
}
