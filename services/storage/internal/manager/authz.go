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

// ErrForkRequired means a project can't use an asset in place because it
// doesn't own it, through itself or its owner: it must fork it (ForkAsset).
// It's returned together with ErrForbidden.
var ErrForkRequired = errors.New("storage: fork required")

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
	// ProjectOwner returns who owns a project (a user, an org or gyoza). An
	// unknown project, or one being deleted, is an error wrapping ErrForbidden.
	ProjectOwner(ctx context.Context, projectID string) (ledger.Owner, error)
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

// ProjectOwner knows no projects, so it refuses: allowing everything can't
// make up who owns what.
func (AllowAll) ProjectOwner(ctx context.Context, projectID string) (ledger.Owner, error) {
	return ledger.Owner{}, fmt.Errorf("%w: AllowAll knows no project owners", ErrForbidden)
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
	mu            sync.Mutex
	owners        map[ruleKey]Access // actor + owner
	projects      map[ruleKey]Access // actor + project ID (sharing)
	projectOwners map[string]ledger.Owner
}

type ruleKey struct {
	actor string
	owner ledger.Owner // set for owner rules
	proj  string       // set for project rules
}

var _ Authorizer = (*RuleAuthorizer)(nil)

// NewRuleAuthorizer returns a RuleAuthorizer with no rules.
func NewRuleAuthorizer() *RuleAuthorizer {
	return &RuleAuthorizer{owners: make(map[ruleKey]Access), projects: make(map[ruleKey]Access), projectOwners: make(map[string]ledger.Owner)}
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
		p := r.projects[ruleKey{actor: actorID, proj: projectID}]
		// As in iam: a project grants write only to files it owns. Files
		// merely associated with it are read-only through it.
		if owner != (ledger.Owner{Kind: ledger.OwnerProject, ID: projectID}) {
			p = min(p, AccessRead)
		}
		level = max(level, p)
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

// SetProjectOwner records who owns projectID, for ProjectOwner.
func (r *RuleAuthorizer) SetProjectOwner(projectID string, owner ledger.Owner) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.projectOwners[projectID] = owner
}

func (r *RuleAuthorizer) ProjectOwner(ctx context.Context, projectID string) (ledger.Owner, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	o, ok := r.projectOwners[projectID]
	if !ok {
		return ledger.Owner{}, fmt.Errorf("%w: unknown project %q", ErrForbidden, projectID)
	}
	return o, nil
}
