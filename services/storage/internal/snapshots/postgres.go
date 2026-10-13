package snapshots

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"gyoza/services/storage/internal/db"
)

// DB is the connection Postgres runs on: *pgxpool.Pool in production, a
// pgx.Tx in tests (Begin then starts a savepoint).
type DB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Begin(ctx context.Context) (pgx.Tx, error)
}

// Postgres is the production Store. Run Migrate before using it.
type Postgres struct {
	conn DB
	q    *db.Queries
}

func NewPostgres(conn DB) *Postgres { return &Postgres{conn: conn, q: db.New(conn)} }

var _ Store = (*Postgres)(nil)

func (p *Postgres) Begin(ctx context.Context, s Snapshot) (Snapshot, error) {
	if err := checkBegin(s); err != nil {
		return Snapshot{}, err
	}
	latest, err := p.Latest(ctx, s.ProjectID)
	switch {
	case err == nil && s.Seq < latest.Seq:
		return Snapshot{}, stale(s, latest)
	case err != nil && !errors.Is(err, ErrNotFound):
		return Snapshot{}, err
	}
	var editedAt *time.Time
	if !s.EditedAt.IsZero() {
		editedAt = &s.EditedAt
	}
	err = p.q.InsertPendingSnapshot(ctx, db.InsertPendingSnapshotParams{
		ObjectID: s.ObjectID, ProjectID: s.ProjectID, Seq: int64(s.Seq),
		EditedBy: nullable(s.EditedBy), EditedAt: editedAt,
		Bucket: s.Bucket, Key: s.Key, ContentType: s.ContentType, SizeBytes: s.SizeBytes,
		ExpiresAt: s.ExpiresAt,
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" { // unique_violation
		return Snapshot{}, fmt.Errorf("%w: object %s already recorded", ErrInvalid, s.ObjectID)
	}
	if err != nil {
		return Snapshot{}, err
	}
	return p.Get(ctx, s.ObjectID)
}

func (p *Postgres) Get(ctx context.Context, objectID string) (Snapshot, error) {
	row, err := p.q.GetSnapshot(ctx, objectID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Snapshot{}, fmt.Errorf("%w: %s", ErrNotFound, objectID)
	}
	if err != nil {
		return Snapshot{}, err
	}
	return fromRow(row), nil
}

func (p *Postgres) Complete(ctx context.Context, objectID string) (Snapshot, error) {
	var out Snapshot
	err := p.inTx(ctx, func(q *db.Queries) error {
		row, err := q.GetSnapshot(ctx, objectID)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: %s", ErrNotFound, objectID)
		}
		if err != nil {
			return err
		}
		if row.Version != nil {
			out = fromRow(row)
			return nil // already completed
		}
		// From here to commit, no other completion for this project runs,
		// so the latest can't change between the check and the update.
		if err := q.LockProjectSnapshots(ctx, row.ProjectID); err != nil {
			return err
		}
		s := fromRow(row)
		next := int32(1)
		latest, err := q.LatestSnapshot(ctx, row.ProjectID)
		switch {
		case err == nil:
			if row.Seq < latest.Seq {
				return stale(s, fromRow(latest))
			}
			next = *latest.Version + 1
		case !errors.Is(err, pgx.ErrNoRows):
			return err
		}
		done, err := q.CompleteSnapshot(ctx, db.CompleteSnapshotParams{ObjectID: objectID, Version: &next})
		if err != nil {
			return err
		}
		out = fromRow(done)
		return nil
	})
	return out, err
}

func (p *Postgres) Latest(ctx context.Context, projectID string) (Snapshot, error) {
	row, err := p.q.LatestSnapshot(ctx, projectID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Snapshot{}, fmt.Errorf("%w: no snapshot of %s", ErrNotFound, projectID)
	}
	if err != nil {
		return Snapshot{}, err
	}
	return fromRow(row), nil
}

func (p *Postgres) List(ctx context.Context, projectID string, before, limit int) ([]Snapshot, error) {
	if err := checkLimit(limit); err != nil {
		return nil, err
	}
	rows, err := p.q.ListSnapshots(ctx, db.ListSnapshotsParams{ProjectID: projectID, Before: int32(before), Lim: int32(limit)})
	return fromRows(rows), err
}

func (p *Postgres) ListExpiredPending(ctx context.Context, before time.Time, limit int) ([]Snapshot, error) {
	if err := checkLimit(limit); err != nil {
		return nil, err
	}
	rows, err := p.q.ListExpiredPendingSnapshots(ctx, db.ListExpiredPendingSnapshotsParams{Before: before, Lim: int32(limit)})
	return fromRows(rows), err
}

func (p *Postgres) ListBeyondKeep(ctx context.Context, keep, limit int) ([]Snapshot, error) {
	if err := checkLimit(limit); err != nil {
		return nil, err
	}
	if keep < 1 {
		return nil, fmt.Errorf("%w: keep %d", ErrInvalid, keep)
	}
	rows, err := p.q.ListSnapshotsBeyondKeep(ctx, db.ListSnapshotsBeyondKeepParams{Keep: int32(keep), Lim: int32(limit)})
	return fromRows(rows), err
}

func (p *Postgres) Delete(ctx context.Context, objectID string) error {
	return p.q.DeleteSnapshot(ctx, objectID)
}

// inTx runs fn in a transaction, committing if it returns nil.
func (p *Postgres) inTx(ctx context.Context, fn func(q *db.Queries) error) error {
	tx, err := p.conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	if err := fn(db.New(tx)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func fromRow(r db.StorageSnapshot) Snapshot {
	s := Snapshot{
		ObjectID: r.ObjectID, ProjectID: r.ProjectID, Seq: uint64(r.Seq),
		Bucket: r.Bucket, Key: r.Key, ContentType: r.ContentType, SizeBytes: r.SizeBytes,
		ExpiresAt: r.ExpiresAt.UTC(), CreatedAt: r.CreatedAt.UTC(),
	}
	if r.Version != nil {
		s.Version = int(*r.Version)
	}
	if r.EditedBy != nil {
		s.EditedBy = *r.EditedBy
	}
	if r.EditedAt != nil {
		s.EditedAt = r.EditedAt.UTC()
	}
	if r.CompletedAt != nil {
		t := r.CompletedAt.UTC()
		s.CompletedAt = &t
	}
	return s
}

func fromRows(rows []db.StorageSnapshot) []Snapshot {
	out := make([]Snapshot, 0, len(rows))
	for _, r := range rows {
		out = append(out, fromRow(r))
	}
	return out
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func (p *Postgres) DeleteProject(ctx context.Context, projectID string) (int, error) {
	n, err := p.q.DeleteProjectSnapshots(ctx, projectID)
	return int(n), err
}

func (p *Postgres) ClearEditedBy(ctx context.Context, userID string) (int, error) {
	if userID == "" {
		return 0, fmt.Errorf("%w: empty user ID", ErrInvalid)
	}
	n, err := p.q.ClearSnapshotEditor(ctx, &userID)
	return int(n), err
}
