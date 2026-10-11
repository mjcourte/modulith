package iam

// Rules shared by every Store implementation and by more than one area:
// IDs, timestamps and names. Area-specific rules are in users_rules.go,
// orgs_rules.go and invites_rules.go.

import (
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

// newID returns a new UUIDv7 as a string. UUIDv7 starts with a timestamp, so
// IDs sort in creation order (see storagemanager's README, "IDs").
func newID() string {
	return uuid.Must(uuid.NewV7()).String()
}

// validID reports whether id is a UUID in canonical form. Lookups use it to
// answer "not found" for IDs that can't exist, without asking the database.
func validID(id string) bool {
	u, err := uuid.Parse(id)
	return err == nil && u.String() == id
}

// now matches what Postgres stores: UTC, microsecond precision.
func now() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }

// normalizeName trims a display name (a user's or an org's) and checks it:
// valid UTF-8, no control characters, at most MaxNameLen characters. Empty is
// allowed only if optional. Errors wrap invalid.
func normalizeName(name string, optional bool, invalid error) (string, error) {
	name = strings.TrimSpace(name)
	switch {
	case name == "" && !optional:
		return "", fmt.Errorf("%w: name is required", invalid)
	case !utf8.ValidString(name):
		return "", fmt.Errorf("%w: name is not valid UTF-8", invalid)
	case utf8.RuneCountInString(name) > MaxNameLen:
		return "", fmt.Errorf("%w: name longer than %d characters", invalid, MaxNameLen)
	case strings.ContainsFunc(name, unicode.IsControl):
		return "", fmt.Errorf("%w: name contains control characters", invalid)
	}
	return name, nil
}

// checkLimit validates the batch size of the cleanup listings.
func checkLimit(limit int) error {
	if limit < 1 || limit > MaxPageSize {
		return fmt.Errorf("%w: limit %d, want 1 to %d", ErrInvalidQuery, limit, MaxPageSize)
	}
	return nil
}
