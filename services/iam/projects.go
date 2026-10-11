package iam

import (
	"context"
	"errors"
	"time"

	"gyoza/services/storageledger"
)

// Project and share errors returned by every Store (see ProjectStore).
var (
	ErrProjectNotFound = errors.New("iam: project not found")
	ErrInvalidProject  = errors.New("iam: invalid project")
	ErrShareNotFound   = errors.New("iam: share not found")
	ErrInvalidShare    = errors.New("iam: invalid share")
	ErrInvalidQuery    = errors.New("iam: invalid query")
)

// Paging limits for project listings.
const (
	DefaultPageSize = 50
	MaxPageSize     = 500
)

// ProjectState says whether a project is usable yet.
type ProjectState string

const (
	// ProjectReady is a normal, usable project.
	ProjectReady ProjectState = "ready"
	// ProjectCloning is a clone still being copied: invisible in listings and
	// granting no access until it's marked ready.
	ProjectCloning ProjectState = "cloning"
)

// Project is a unit of work with exactly one document (the document itself
// is documentmanager's). Its files are owned by the project in the ledger
// (owner kind project, ID = the project's ID).
type Project struct {
	ID         string              // UUIDv7
	Owner      storageledger.Owner // a user, an org, or gyoza (templates); never a project
	Name       string              // not unique
	State      ProjectState
	ClonedFrom string // the source project's ID, for a clone; "" otherwise
	// OrgLocked keeps an org project inside its org: no shares with outsiders
	// (existing ones are suspended: they grant nothing while locked) and clones
	// only into the org. Only org-owned projects can be locked.
	OrgLocked bool
	CreatedBy string // user ID; "" for gyoza projects made by tooling
	CreatedAt time.Time
	UpdatedAt time.Time // last rename, transfer, lock or state change
	// DeletingAt is set when the project's deletion starts (see DeletionStore):
	// from then on it's hidden and grants nothing.
	DeletingAt *time.Time
}

// Access is a level of access to a project. Shares carry one; step 3's
// access rules compute one for any user and project.
type Access string

const (
	AccessNone   Access = ""       // the zero value: no access
	AccessViewer Access = "viewer" // open, read and download; clone
	AccessEditor Access = "editor" // change the document and the project's files
)

// Share gives one user access to a project they don't otherwise reach.
type Share struct {
	ProjectID string
	UserID    string
	Access    Access // viewer or editor
	SharedBy  string // user ID of whoever shared it
	CreatedAt time.Time
	UpdatedAt time.Time // last change of Access
}

// ProjectQuery selects projects within exactly one scope. Listings never
// include projects still cloning.
type ProjectQuery struct {
	// Scope: set exactly one.
	//
	// VisibleTo lists a user's projects: those they own, those their org owns,
	// and those shared with them, each once. Gyoza templates aren't included:
	// list them with Owner gyoza.
	VisibleTo string
	// Owner lists one owner's projects: an org's, a user's, or the gyoza
	// templates.
	Owner storageledger.Owner

	// Pagination, oldest first (UUIDv7 order). PageSize 0 means
	// DefaultPageSize; larger than MaxPageSize is capped. PageToken is the
	// previous page's NextPageToken, or "" for the first page.
	PageSize  int
	PageToken string
}

// ProjectPage is one page of projects. NextPageToken is "" on the last page.
type ProjectPage struct {
	Projects      []Project
	NextPageToken string
}

// ProjectStore persists projects and their shares. Pure data, no
// authorization; it enforces:
//   - a project's owner exists (ErrUserNotFound, ErrOrgNotFound) and is a
//     user, an org or gyoza (ErrInvalidProject)
//   - gyoza projects (templates) are never shared: everyone can already see
//     them (ErrInvalidShare)
//   - only org-owned projects can be org-locked (ErrInvalidProject), and a locked
//     project takes no new or changed shares for people outside its org
//     (ErrInvalidShare)
//   - a share's user exists (ErrUserNotFound); one share per user and project
type ProjectStore interface {
	// CreateProject stores a project, assigning its ID and timestamps. State
	// "" means ProjectReady; a clone is created as ProjectCloning.
	CreateProject(ctx context.Context, p Project) (Project, error)
	// GetProject returns the project, whatever its state, or ErrProjectNotFound.
	GetProject(ctx context.Context, id string) (Project, error)
	// ListProjects returns one page of ready projects matching q.
	ListProjects(ctx context.Context, q ProjectQuery) (ProjectPage, error)
	// RenameProject changes the project's name.
	RenameProject(ctx context.Context, id, name string) (Project, error)
	// TransferProject gives the project to another owner (e.g. a user moving
	// a personal project to their org). Shares are kept; the org-lock is
	// cleared, since it was the old owner's policy. Transferring to or from
	// gyoza is ErrInvalidProject (templates are made by tooling).
	TransferProject(ctx context.Context, id string, to storageledger.Owner) (Project, error)
	// MarkProjectReady ends a clone: ProjectCloning becomes ProjectReady.
	// Marking a ready project again changes nothing, so a resumed clone job
	// can repeat it.
	MarkProjectReady(ctx context.Context, id string) (Project, error)
	// SetOrgLocked locks or unlocks an org project. Setting the current value
	// again changes nothing.
	SetOrgLocked(ctx context.Context, id string, locked bool) (Project, error)

	// ShareProject gives a user access to a project, or changes the access
	// of an existing share (keeping its CreatedAt).
	ShareProject(ctx context.Context, s Share) (Share, error)
	// GetShare returns one user's share of a project, or ErrShareNotFound.
	GetShare(ctx context.Context, projectID, userID string) (Share, error)
	// ListShares returns a project's shares, oldest first, suspended ones
	// included (the UI shows them as suspended).
	ListShares(ctx context.Context, projectID string) ([]Share, error)
	// Unshare removes a user's share. Removing a missing share is not an
	// error, so it's safe to retry.
	Unshare(ctx context.Context, projectID, userID string) error
}
