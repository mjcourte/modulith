// Package iam (identity and access management) owns users, orgs, membership,
// invites, projects and sharing, and answers other services' "may this
// user...?" questions. Identity itself (sign-in, sessions) is the gateway's,
// through SuperTokens.
//
// This file is the public surface; the code is under internal/: directory
// (the data and the access rules) and manager (the logic). The names below
// are aliases (type X = Y), so values flow between them without conversion.
package iam

import (
	"gyoza/services/iam/internal/directory"
	"gyoza/services/iam/internal/manager"
)

// The API: Service and the types its calls take and return.
type (
	Service           = manager.Service
	Manager           = manager.Manager
	Config            = manager.Config
	StorageAuthorizer = manager.StorageAuthorizer
	Store             = directory.Store

	Identity     = directory.Identity
	User         = directory.User
	SignInResult = manager.SignInResult
	Me           = manager.Me
	Org          = directory.Org
	Member       = directory.Member
	Role         = directory.Role
	Invite       = directory.Invite
	Project      = directory.Project
	ProjectView  = manager.ProjectView
	ProjectPage  = directory.ProjectPage
	ProjectState = directory.ProjectState
	Share        = directory.Share
	Access       = directory.Access
)

const (
	RoleAdmin  = directory.RoleAdmin
	RoleMember = directory.RoleMember

	AccessNone   = directory.AccessNone
	AccessViewer = directory.AccessViewer
	AccessEditor = directory.AccessEditor

	ProjectReady   = directory.ProjectReady
	ProjectCloning = directory.ProjectCloning

	DefaultInviteTTL = manager.DefaultInviteTTL
)

// Errors callers can test for with errors.Is.
var (
	ErrForbidden       = manager.ErrForbidden
	ErrUserNotFound    = directory.ErrUserNotFound
	ErrInvalidUser     = directory.ErrInvalidUser
	ErrEmailTaken      = directory.ErrEmailTaken
	ErrOrgNotFound     = directory.ErrOrgNotFound
	ErrInvalidOrg      = directory.ErrInvalidOrg
	ErrAlreadyMember   = directory.ErrAlreadyMember
	ErrNotMember       = directory.ErrNotMember
	ErrLastAdmin       = directory.ErrLastAdmin
	ErrInviteNotFound  = directory.ErrInviteNotFound
	ErrInvalidInvite   = directory.ErrInvalidInvite
	ErrInviteExpired   = directory.ErrInviteExpired
	ErrProjectNotFound = directory.ErrProjectNotFound
	ErrInvalidProject  = directory.ErrInvalidProject
	ErrShareNotFound   = directory.ErrShareNotFound
	ErrInvalidShare    = directory.ErrInvalidShare
	ErrInvalidQuery    = directory.ErrInvalidQuery
)

// NewMemoryStore returns an in-memory Store, for tests and local runs.
func NewMemoryStore() Store { return directory.NewMemoryStore() }

// NewManager builds iam's logic on store.
func NewManager(store Store, cfg Config) (*Manager, error) { return manager.NewManager(store, cfg) }

// NewStorageAuthorizer answers storage's permission questions from store
// alone, so the binaries can build it before storage and iam's Manager.
func NewStorageAuthorizer(store Store) *StorageAuthorizer {
	return manager.NewStorageAuthorizer(store)
}
