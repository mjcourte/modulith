package dam

// Postgres is the production Store, in the storage schema. Its
// methods are split across postgres_*.go by area; this file has the shared
// plumbing: transactions, locks, ID and row conversions.

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/mjcourte/modulith/services/storage/internal/db"
	"github.com/mjcourte/modulith/services/storage/internal/ledger"
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

// lockOwnerTree serialises changes to owners' folder trees for the rest of
// the transaction. Locks are always taken in the same order, so two
// transactions locking the same pair of owners can't deadlock.
func lockOwnerTree(ctx context.Context, q *db.Queries, owners ...ledger.Owner) error {
	keys := make([]string, 0, len(owners))
	for _, o := range owners {
		keys = append(keys, string(o.Kind)+":"+o.ID)
	}
	slices.Sort(keys)
	for _, k := range slices.Compact(keys) {
		if err := q.LockOwnerTree(ctx, k); err != nil {
			return fmt.Errorf("lock folder tree: %w", err)
		}
	}
	return nil
}

// zeroID is the smallest UUID: the cursor for a first page, and the
// "exclude nothing" value for name checks.
const zeroID = "00000000-0000-0000-0000-000000000000"

// validID reports whether id is a well-formed UUID. Methods check it before
// querying, so a garbage ID is "not found" (as in the memory store) rather
// than a Postgres syntax error.
func validID(id string) bool {
	_, err := uuid.Parse(id)
	return err == nil
}

// optID maps "" to SQL NULL for nullable uuid columns.
func optID(id string) *string {
	if id == "" {
		return nil
	}
	return &id
}

// idOf maps SQL NULL back to "".
func idOf(id *string) string {
	if id == nil {
		return ""
	}
	return *id
}

// nonNil makes nil slices empty: pgx sends a nil slice as NULL, which the
// NOT NULL tags columns reject.
func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func noRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }

// uniqueViolation reports a unique index refusing a write: a sibling folder
// name taken by a concurrent transaction, caught by folders_live_name.
func uniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

func ownerOf(kind, id string) ledger.Owner {
	return ledger.Owner{Kind: ledger.OwnerKind(kind), ID: id}
}

func assetFromRow(r db.StorageAsset) Asset {
	return Asset{
		ID: r.ID, Owner: ownerOf(r.OwnerKind, r.OwnerID), ProjectID: r.ProjectID, FolderID: idOf(r.FolderID),
		Name: r.Name, Description: r.Description, Tags: nonNil(r.Tags),
		ContentType: r.ContentType, Category: Category(r.Category), CurrentVersion: int(r.CurrentVersion),
		CreatedBy: r.CreatedBy, CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC(),
		DeletedAt: utcPtr(r.DeletedAt), DeletionID: idOf(r.DeletionID),
	}
}

func versionFromRow(r db.StorageAssetVersion) Version {
	return Version{
		AssetID: r.AssetID, Number: int(r.Number), ObjectID: r.ObjectID, ContentType: r.ContentType,
		SizeBytes: r.SizeBytes, CreatedBy: r.CreatedBy, CreatedAt: r.CreatedAt.UTC(),
	}
}

func folderFromRow(r db.StorageFolder) Folder {
	return Folder{
		ID: r.ID, Owner: ownerOf(r.OwnerKind, r.OwnerID), ParentID: idOf(r.ParentID), Name: r.Name,
		CreatedBy: r.CreatedBy, CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC(),
		DeletedAt: utcPtr(r.DeletedAt), DeletionID: idOf(r.DeletionID),
	}
}

func uploadFromRow(r db.StorageUpload) Upload {
	return Upload{
		ID: r.ID, Owner: ownerOf(r.OwnerKind, r.OwnerID), ProjectID: r.ProjectID, CreatedBy: r.CreatedBy,
		AssetID: idOf(r.AssetID), Name: r.Name, Description: r.Description, Tags: nonNil(r.Tags), FolderID: idOf(r.FolderID),
		Bucket: r.Bucket, Key: r.Key, ContentType: r.ContentType, SizeBytes: r.SizeBytes, ObjectID: r.ObjectID,
		ExpiresAt: r.ExpiresAt.UTC(), CreatedAt: r.CreatedAt.UTC(), CompletedAt: utcPtr(r.CompletedAt),
		ResultAssetID: idOf(r.ResultAssetID), ResultVersion: int(r.ResultVersion),
	}
}

func foldersFromRows(rows []db.StorageFolder) []Folder {
	out := make([]Folder, len(rows))
	for i, r := range rows {
		out[i] = folderFromRow(r)
	}
	return out
}
