package iam

// User rules shared by every UserStore implementation.

import (
	"fmt"
	"net/mail"
	"net/url"
	"strings"
)

// emailKey is how emails are compared and stored: trimmed and lowercased.
// Strictly, the part before the @ may be case-sensitive, but no mainstream
// provider treats it so, and people type addresses in any case. Lookups use
// it directly; storing goes through normalizeEmail, which also validates.
func emailKey(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// normalizeEmail returns the stored form of an email address, or an error
// wrapping invalid if it isn't a plain address ("a@example.com", not
// "Ann <a@example.com>").
func normalizeEmail(email string, invalid error) (string, error) {
	email = emailKey(email)
	if email == "" {
		return "", fmt.Errorf("%w: email is required", invalid)
	}
	if len(email) > MaxEmailLen {
		return "", fmt.Errorf("%w: email longer than %d bytes", invalid, MaxEmailLen)
	}
	// ParseAddress accepts display names and comments too; requiring the
	// parsed address to be the whole input rules those out.
	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Address != email || addr.Name != "" {
		return "", fmt.Errorf("%w: %q is not an email address", invalid, email)
	}
	return email, nil
}

// checkUserID checks a SuperTokens user ID: present, short, printable ASCII
// without spaces (the same shape as the ledger's chosen object IDs).
func checkUserID(id string) error {
	if id == "" {
		return fmt.Errorf("%w: user ID is required", ErrInvalidUser)
	}
	if len(id) > MaxUserIDLen {
		return fmt.Errorf("%w: user ID longer than %d bytes", ErrInvalidUser, MaxUserIDLen)
	}
	for _, r := range id {
		if r <= ' ' || r > '~' {
			return fmt.Errorf("%w: user ID %q must be printable ASCII without spaces", ErrInvalidUser, id)
		}
	}
	return nil
}

// checkAvatarURL accepts "" (no picture) or an absolute https URL. Browsers
// load it directly, so nothing else (http, javascript:, data:) gets through.
func checkAvatarURL(raw string) error {
	if raw == "" {
		return nil
	}
	if len(raw) > MaxURLLen {
		return fmt.Errorf("%w: avatar URL longer than %d bytes", ErrInvalidUser, MaxURLLen)
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return fmt.Errorf("%w: avatar URL must be an https URL", ErrInvalidUser)
	}
	return nil
}

// normalize validates an identity for UpsertUser: user ID, email, an optional
// name (the UI falls back to the email) and avatar URL.
func (id Identity) normalize() (Identity, error) {
	if err := checkUserID(id.UserID); err != nil {
		return id, err
	}
	var err error
	if id.Email, err = normalizeEmail(id.Email, ErrInvalidUser); err != nil {
		return id, err
	}
	if id.Name, err = normalizeName(id.Name, true, ErrInvalidUser); err != nil {
		return id, err
	}
	id.ProviderAvatarURL = strings.TrimSpace(id.ProviderAvatarURL)
	if err := checkAvatarURL(id.ProviderAvatarURL); err != nil {
		return id, err
	}
	return id, nil
}
