package nats

import (
	"errors"
	"fmt"
	"strings"
)

// ErrInvalidToken is returned when an ID can't be used as one subject token.
var ErrInvalidToken = errors.New("nats: invalid subject token")

// Subject patterns, for stream configs and subscriptions. Publish with the
// builders below, which fill in the wildcards.
const (
	// Document transport: both patterns are in the one DOCUMENTS stream
	// (streams.go).
	// Deltas are applied to every replica: documents and each frontend.
	// Snapshots are pointers to a snapshot stored in S3; documents is
	// the only publisher.
	PatternDocumentDelta    = "document.*.delta"    // document.{projectID}.delta
	PatternDocumentSnapshot = "document.*.snapshot" // document.{projectID}.snapshot

	// User state: who is viewing a project and where their cursor is. Core
	// NATS only, no stream: it changes constantly and goes stale in seconds,
	// so "viewing" is a heartbeat that subscribers expire when it stops.
	PatternUserStateViewing = "userstate.*.*.viewing"   // userstate.{projectID}.{userID}.viewing
	PatternUserStateCursor  = "userstate.*.*.cursorpos" // userstate.{projectID}.{userID}.cursorpos

	// TODO - job queue subjects
	// if we use NATS to communicate async job and/or generative process completion

	// TODO - the google docs CRDT layers inside the canvas
	// this is a FE multiplayer/P2P feature (unless agent MCP text editing?)
	// document.{projectID}.{nodeID}.<>: awareness + text cursor position,
	// the data exchange can transport over the delta subject
)

// DocumentDelta returns the delta subject for a project.
func DocumentDelta(projectID string) (string, error) {
	return subject("document", projectID, "delta")
}

// DocumentSnapshot returns the snapshot subject for a project.
func DocumentSnapshot(projectID string) (string, error) {
	return subject("document", projectID, "snapshot")
}

// UserStateViewing returns the subject a user's viewing heartbeat goes to.
func UserStateViewing(projectID, userID string) (string, error) {
	return subject("userstate", projectID, userID, "viewing")
}

// UserStateCursor returns the subject a user's cursor position goes to.
func UserStateCursor(projectID, userID string) (string, error) {
	return subject("userstate", projectID, userID, "cursorpos")
}

// UserStateAll returns a subscription subject matching every user state
// message in a project.
func UserStateAll(projectID string) (string, error) {
	return subject("userstate", projectID, ">")
}

// subject joins tokens with dots. Every token except the first and last is
// an ID, checked so it can't add a level or act as a wildcard: a project ID of
// "*" would otherwise publish into, or subscribe to, every project.
func subject(tokens ...string) (string, error) {
	for _, id := range tokens[1 : len(tokens)-1] {
		if err := checkToken(id); err != nil {
			return "", err
		}
	}
	return strings.Join(tokens, "."), nil
}

func checkToken(id string) error {
	if id == "" || strings.ContainsAny(id, ".*> \t\r\n") {
		return fmt.Errorf("%w: %q", ErrInvalidToken, id)
	}
	return nil
}
