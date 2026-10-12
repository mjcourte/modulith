// Package directory is iam's data: users, orgs and members, invites, projects
// and shares, and the access rules over them (access.go). The Store checks
// data shape only; permissions are the manager's.
package directory

// Store persists iam's data in its own schema. It is pure data: it
// validates shape and keeps the invariants listed on each part, but does no
// authorization; the Manager checks permissions before calling it. Every
// implementation must pass the shared store test suite.
//
// The parts are embedded, so one Store value has all their methods.
type Store interface {
	UserStore     // users.go
	OrgStore      // orgs.go
	InviteStore   // invites.go
	ProjectStore  // projects.go: projects and their shares
	DeletionStore // deletions.go: markers and the steps of the deletion cascades
}
