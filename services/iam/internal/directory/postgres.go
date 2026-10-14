package directory

// Postgres is the production Store, in the iam schema. Its methods are split
// across postgres_*.go by area; this file has the shared plumbing:
// transactions, error mapping and row conversions.
//
// How it keeps the invariants the memory store keeps with one mutex:
//   - one org per user: memberships' primary key is the user ID
//   - an admin in every org: membership changes lock the org row first
//     (GetOrgForUpdate), then apply the same pure rules (orgs_rules.go) to
//     the member list
//   - no new references to a row being deleted: adding one takes a shared
//     lock on the user, org or project row (Get...ForShare) and checks it's
//     live; marking it deleting is an UPDATE, which waits for those locks
//     (and they for it)

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"gyoza/services/iam/internal/db"
	"gyoza/services/storage"
)

// DB is the connection Postgres runs on: *pgxpool.Pool in production, a
// pgx.Tx in tests. Begin on a transaction starts a savepoint, so the Store's
// own transactions nest inside a test's rolled-back one.
type DB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Begin(ctx context.Context) (pgx.Tx, error)
}

// Postgres is a Store backed by PostgreSQL. Run Migrate before using it.
type Postgres struct {
	conn DB
	q    *db.Queries
}

// NewPostgres returns a Postgres store on conn.
func NewPostgres(conn DB) *Postgres {
	return &Postgres{conn: conn, q: db.New(conn)}
}

// Compile-time check that *Postgres satisfies Store.
var _ Store = (*Postgres)(nil)

// inTx runs fn in a transaction, committing if it returns nil.
func (p *Postgres) inTx(ctx context.Context, fn func(q *db.Queries) error) error {
	tx, err := p.conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	// Rollback after Commit is a no-op; a fresh context lets it run even if
	// ctx was cancelled mid-way.
	defer tx.Rollback(context.WithoutCancel(ctx))
	if err := fn(db.New(tx)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func noRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }

// violates reports a write refused by the named unique index or primary key.
func violates(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == constraint
}

// optID maps "" to SQL NULL for nullable uuid columns, and idOf maps back.
func optID(id string) *string {
	if id == "" {
		return nil
	}
	return &id
}

func idOf(id *string) string {
	if id == nil {
		return ""
	}
	return *id
}

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

// ---- Liveness, under a shared lock (see the top of this file)

// lockLiveUser locks the user's row until the transaction ends and checks
// they exist and aren't being deleted (ErrUserNotFound).
func lockLiveUser(ctx context.Context, q *db.Queries, id string) error {
	u, err := q.GetUserForShare(ctx, id)
	if noRows(err) || (err == nil && u.DeletingAt != nil) {
		return ErrUserNotFound
	}
	return err
}

// lockLiveOrg is lockLiveUser for orgs (ErrOrgNotFound).
func lockLiveOrg(ctx context.Context, q *db.Queries, id string) error {
	if !validID(id) {
		return ErrOrgNotFound
	}
	o, err := q.GetOrgForShare(ctx, id)
	if noRows(err) || (err == nil && o.DeletingAt != nil) {
		return ErrOrgNotFound
	}
	return err
}

// lockLiveOwner checks a project owner as the memory store's ownerExists:
// a live user or org; gyoza always exists.
func lockLiveOwner(ctx context.Context, q *db.Queries, o storage.Owner) error {
	switch o.Kind {
	case storage.OwnerUser:
		return lockLiveUser(ctx, q, o.ID)
	case storage.OwnerOrg:
		return lockLiveOrg(ctx, q, o.ID)
	}
	return nil
}

// ---- Rows

func userFromRow(r db.IamUser) User {
	return User{
		ID: r.ID, Email: r.Email, EmailVerified: r.EmailVerified, Name: r.Name,
		ProviderAvatarURL: r.ProviderAvatarUrl, CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC(),
		DeletingAt: utcPtr(r.DeletingAt), DeletingToOrg: idOf(r.DeletingToOrg),
	}
}

func orgFromRow(r db.IamOrg) Org {
	return Org{ID: r.ID, Name: r.Name, CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC(), DeletingAt: utcPtr(r.DeletingAt)}
}

func memberFromRow(r db.IamMembership) Member {
	return Member{OrgID: r.OrgID, UserID: r.UserID, Role: Role(r.Role), JoinedAt: r.JoinedAt.UTC()}
}

func membersFromRows(rows []db.IamMembership) []Member {
	out := make([]Member, len(rows))
	for i, r := range rows {
		out[i] = memberFromRow(r)
	}
	return out
}

func inviteFromRow(r db.IamInvite) Invite {
	return Invite{
		ID: r.ID, OrgID: r.OrgID, Email: r.Email, Role: Role(r.Role), InvitedBy: r.InvitedBy,
		CreatedAt: r.CreatedAt.UTC(), ExpiresAt: r.ExpiresAt.UTC(),
	}
}

func invitesFromRows(rows []db.IamInvite) []Invite {
	out := make([]Invite, len(rows))
	for i, r := range rows {
		out[i] = inviteFromRow(r)
	}
	return out
}

func projectFromRow(r db.IamProject) Project {
	return Project{
		ID: r.ID, Owner: storage.Owner{Kind: storage.OwnerKind(r.OwnerKind), ID: r.OwnerID}, Name: r.Name,
		State: ProjectState(r.State), ClonedFrom: idOf(r.ClonedFrom), OrgLocked: r.OrgLocked, CreatedBy: r.CreatedBy,
		CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC(), DeletingAt: utcPtr(r.DeletingAt),
	}
}

func projectsFromRows(rows []db.IamProject) []Project {
	out := make([]Project, len(rows))
	for i, r := range rows {
		out[i] = projectFromRow(r)
	}
	return out
}

func shareFromRow(r db.IamProjectShare) Share {
	return Share{
		ProjectID: r.ProjectID, UserID: r.UserID, Access: Access(r.Access), SharedBy: r.SharedBy,
		CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC(),
	}
}

func pendingShareFromRow(r db.IamPendingShare) Share {
	return Share{
		ProjectID: r.ProjectID, Email: r.Email, Access: Access(r.Access), SharedBy: r.SharedBy,
		CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC(), ExpiresAt: r.ExpiresAt.UTC(),
	}
}
