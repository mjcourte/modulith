// Package session carries the signed-in user through a request's context:
// auth's middleware sets it from the SuperTokens session, handlers read it.
// It keeps the handlers free of SuperTokens, so their tests set it directly.
package session

import (
	"context"
	"time"
)

type actorKey struct{}

// WithActor returns ctx carrying the signed-in user's ID.
func WithActor(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, actorKey{}, userID)
}

// Actor returns the signed-in user's ID, or "" outside a session. Every
// service refuses the empty actor, so a route mounted without a session by
// mistake fails closed.
func Actor(ctx context.Context) string {
	id, _ := ctx.Value(actorKey{}).(string)
	return id
}

type expiryKey struct{}

// WithExpiry returns ctx carrying when the session's access token expires.
// Long-lived connections (the WebSocket) close then; a short request doesn't
// need it.
func WithExpiry(ctx context.Context, t time.Time) context.Context {
	return context.WithValue(ctx, expiryKey{}, t)
}

// Expiry returns when the session's access token expires, if known.
func Expiry(ctx context.Context) (time.Time, bool) {
	t, ok := ctx.Value(expiryKey{}).(time.Time)
	return t, ok
}
