// Package iam (identity and access management) owns users, orgs, membership,
// invites, projects and sharing, and answers other services' "may this
// user...?" questions. Identity itself (sign-in, sessions) is the gateway's,
// through SuperTokens.
//
// This file is the public surface; the code is under internal/: directory
// (the data and the access rules), manager (the logic), grpcapi (the gRPC
// adapters) and db (the schema). The names below
// are aliases (type X = Y), so values flow between them without conversion.
package iam

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc"

	pb "gyoza/contracts/gen/go/gyoza/iam/v1"
	"gyoza/services/iam/internal/db"
	"gyoza/services/iam/internal/directory"
	"gyoza/services/iam/internal/grpcapi"
	"gyoza/services/iam/internal/manager"
	"gyoza/services/storage"
)

// The API: Service and the types its calls take and return.
type (
	Service            = manager.Service
	Manager            = manager.Manager
	Config             = manager.Config
	StorageAuthorizer  = manager.StorageAuthorizer
	Storage            = manager.Storage
	IdentityProvider   = manager.IdentityProvider
	NoIdentityProvider = manager.NoIdentityProvider
	CleanupReport      = manager.CleanupReport
	Store              = directory.Store

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
	ErrNotDeleting     = directory.ErrNotDeleting
)

// NewMemoryStore returns an in-memory Store, for tests and local runs.
func NewMemoryStore() Store { return directory.NewMemoryStore() }

// NewManager builds iam's logic on store; the deletion cascades call files
// (storage) and idp.
func NewManager(store Store, files Storage, idp IdentityProvider, cfg Config) (*Manager, error) {
	return manager.NewManager(store, files, idp, cfg)
}

// NewStorageAuthorizer answers storage's permission questions from store
// alone, so the binaries can build it before storage and iam's Manager.
func NewStorageAuthorizer(store Store) *StorageAuthorizer {
	return manager.NewStorageAuthorizer(store)
}

// Migrate brings the iam schema up to date.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error { return db.Migrate(ctx, pool) }

// NewPostgres returns the production Store on pool. Run Migrate first.
func NewPostgres(pool *pgxpool.Pool) Store { return directory.NewPostgres(pool) }

// RegisterGRPC serves s as IAMService and authz as AuthzService on r.
func RegisterGRPC(r grpc.ServiceRegistrar, s Service, authz storage.Authorizer) {
	pb.RegisterIAMServiceServer(r, grpcapi.NewServer(s))
	pb.RegisterAuthzServiceServer(r, grpcapi.NewAuthzServer(authz))
}

// NewGRPCClient is a Service calling a remote iam over conn.
func NewGRPCClient(conn grpc.ClientConnInterface) Service { return grpcapi.NewClient(conn) }

// NewAuthzClient is storage's Authorizer, asking a remote iam over conn.
func NewAuthzClient(conn grpc.ClientConnInterface) storage.Authorizer {
	return grpcapi.NewAuthzClient(conn)
}
