package manager

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"gyoza/services/storage/internal/ledger"
)

// ErrForbidden means the acting user may not do what they asked. Manager
// methods return it (wrapped with details) after asking the Authorizer.
var ErrForbidden = errors.New("storage: forbidden")

// Authorizer decides whether a user may read or write files that belong to
// an owner, or are associated with a project. It's declared here, where it's
// used; iam implements it (org existence, membership, project
// sharing), so storage never reads iam's tables.
//
// Both methods get the owner and the project: a file can be reachable through
// either, e.g. an org's asset used in a project shared with a guest. Each
// returns nil if allowed, or an error wrapping ErrForbidden.
type Authorizer interface {
	CanRead(ctx context.Context, actorID string, owner ledger.Owner, projectID string) error
	CanWrite(ctx context.Context, actorID string, owner ledger.Owner, projectID string) error
}

// errNoActor is returned for calls without an acting user. Every request
// reaching storage should carry one (the gateway authenticates), so a
// missing ID is a wiring bug, not a guest.
func errNoActor() error {
	return fmt.Errorf("%w: no acting user", ErrForbidden)
}

// AllowAll is the stand-in Authorizer until iam implements the real
// one: every identified user may do anything. Never deploy it where real
// users can reach it.
type AllowAll struct{}

var _ Authorizer = AllowAll{}

func (AllowAll) CanRead(ctx context.Context, actorID string, owner ledger.Owner, projectID string) error {
	if actorID == "" {
		return errNoActor()
	}
	return nil
}

func (AllowAll) CanWrite(ctx context.Context, actorID string, owner ledger.Owner, projectID string) error {
	if actorID == "" {
		return errNoActor()
	}
	return nil
}

// Access is a permission level. Write includes read.
type Access int

const (
	AccessNone Access = iota // the zero value: no access
	AccessRead
	AccessWrite
)

func (a Access) String() string {
	switch a {
	case AccessRead:
		return "read"
	case AccessWrite:
		return "write"
	}
	return "none"
}

// RuleAuthorizer is an Authorizer driven by explicit rules, for tests and
// local runs: "u1 may write org acme", "u2 may read project p1". It also
// has the one rule every real policy will have: users may write their own
// library (an owner of kind user with their ID). Safe for concurrent use.
type RuleAuthorizer struct {
	mu       sync.Mutex
	owners   map[ruleKey]Access // actor + owner
	projects map[ruleKey]Access // actor + project ID (sharing)
}

type ruleKey struct {
	actor string
	owner ledger.Owner // set for owner rules
	proj  string       // set for project rules
}

var _ Authorizer = (*RuleAuthorizer)(nil)

// NewRuleAuthorizer returns a RuleAuthorizer with no rules.
func NewRuleAuthorizer() *RuleAuthorizer {
	return &RuleAuthorizer{owners: make(map[ruleKey]Access), projects: make(map[ruleKey]Access)}
}

// AllowOwner gives actor access to everything owner owns, e.g. org members.
func (r *RuleAuthorizer) AllowOwner(actorID string, owner ledger.Owner, access Access) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.owners[ruleKey{actor: actorID, owner: owner}] = access
}

// AllowProject gives actor access to everything associated with a project,
// whoever owns it, e.g. a guest the project was shared with.
func (r *RuleAuthorizer) AllowProject(actorID, projectID string, access Access) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.projects[ruleKey{actor: actorID, proj: projectID}] = access
}

// access returns the best level actor has through the owner or the project.
func (r *RuleAuthorizer) access(actorID string, owner ledger.Owner, projectID string) Access {
	if owner.Kind == ledger.OwnerUser && owner.ID == actorID {
		return AccessWrite // a user's own library
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	level := r.owners[ruleKey{actor: actorID, owner: owner}] // missing key: AccessNone
	if projectID != "" {
		level = max(level, r.projects[ruleKey{actor: actorID, proj: projectID}])
	}
	return level
}

func (r *RuleAuthorizer) check(actorID string, owner ledger.Owner, projectID string, need Access) error {
	if actorID == "" {
		return errNoActor()
	}
	if r.access(actorID, owner, projectID) < need {
		return fmt.Errorf("%w: %s may not %s files of %s %s (project %q)",
			ErrForbidden, actorID, need, owner.Kind, owner.ID, projectID)
	}
	return nil
}

func (r *RuleAuthorizer) CanRead(ctx context.Context, actorID string, owner ledger.Owner, projectID string) error {
	return r.check(actorID, owner, projectID, AccessRead)
}

func (r *RuleAuthorizer) CanWrite(ctx context.Context, actorID string, owner ledger.Owner, projectID string) error {
	return r.check(actorID, owner, projectID, AccessWrite)
}
