// Package usermanager owns users, orgs, membership, invites, projects and
// sharing, and answers other services' "may this user...?" questions.
// Identity itself (sign-in, sessions) is the gateway's, through SuperTokens.
package usermanager

// Store persists usermanager's data in its own schema. It is pure data: it
// validates shape and keeps the invariants listed on each part, but does no
// authorization; the Manager checks permissions before calling it. Every
// implementation must pass the shared store test suite.
//
// The parts are embedded, so one Store value has all their methods.
type Store interface {
	UserStore   // users.go
	OrgStore    // orgs.go
	InviteStore // invites.go
}
