package ledger

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"gyoza/services/storage/internal/db"
)

// DB is the connection Postgres runs queries on.
// *pgxpool.Pool, *pgx.Conn and pgx.Tx all satisfy it.
type DB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Postgres is the production Service, backed by the ledger schema.
// Run Migrate before using it.
type Postgres struct {
	q *db.Queries
}

func NewPostgres(conn DB) *Postgres {
	return &Postgres{q: db.New(conn)}
}

// Compile-time check that *Postgres satisfies Service.
var _ Service = (*Postgres)(nil)

func (p *Postgres) Create(ctx context.Context, obj Object) (Object, error) {
	obj, err := obj.normalize()
	if err != nil {
		return Object{}, err
	}
	if obj.ID == "" {
		obj.ID = rand.Text()
	}
	row, err := p.q.CreateObject(ctx, db.CreateObjectParams{
		ID:          obj.ID,
		OwnerKind:   string(obj.Owner.Kind),
		OwnerID:     obj.Owner.ID, // "" for gyoza
		CreatedBy:   nullable(obj.CreatedBy),
		ProjectID:   nullable(obj.ProjectID),
		Kind:        nullable(string(obj.Kind)),
		Bucket:      obj.Bucket,
		Key:         obj.Key,
		SizeBytes:   obj.SizeBytes,
		ContentType: obj.ContentType,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// The ID is taken (ON CONFLICT DO NOTHING). The existing row can't
		// vanish meanwhile: only Purge deletes rows, and only deleted ones.
		existing, err := p.Get(ctx, obj.ID)
		if err != nil {
			return Object{}, err
		}
		return existingOrTaken(existing, obj)
	}
	if err != nil {
		return Object{}, fmt.Errorf("create object: %w", err)
	}
	return fromRow(row), nil
}

func (p *Postgres) Get(ctx context.Context, id string) (Object, error) {
	row, err := p.q.GetObject(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Object{}, ErrNotFound
	}
	if err != nil {
		return Object{}, fmt.Errorf("get object %s: %w", id, err)
	}
	return fromRow(row), nil
}

func (p *Postgres) List(ctx context.Context, q ListQuery) (ListPage, error) {
	q, err := q.normalize()
	if err != nil {
		return ListPage{}, err
	}
	after, err := q.cursor()
	if err != nil {
		return ListPage{}, err
	}
	limit := int32(q.PageSize + 1) // one extra row says whether there is a next page

	var rows []db.StorageObject
	if q.ProjectID != "" {
		rows, err = p.q.ListObjectsByProject(ctx, db.ListObjectsByProjectParams{
			ProjectID:      q.ProjectID,
			AfterCreatedAt: after.createdAt,
			AfterID:        after.id,
			CreatedBy:      nullable(q.CreatedBy),
			Kind:           nullable(string(q.Kind)),
			IncludeDeleted: q.IncludeDeleted,
			RowLimit:       limit,
		})
	} else {
		rows, err = p.q.ListObjectsByOwner(ctx, db.ListObjectsByOwnerParams{
			OwnerKind:      string(q.Owner.Kind),
			OwnerID:        q.Owner.ID,
			AfterCreatedAt: after.createdAt,
			AfterID:        after.id,
			CreatedBy:      nullable(q.CreatedBy),
			Kind:           nullable(string(q.Kind)),
			IncludeDeleted: q.IncludeDeleted,
			RowLimit:       limit,
		})
	}
	if err != nil {
		return ListPage{}, fmt.Errorf("list objects: %w", err)
	}

	objs := make([]Object, len(rows))
	for i, row := range rows {
		objs[i] = fromRow(row)
	}
	return pageOf(objs, q.PageSize), nil
}

func (p *Postgres) MarkDeleted(ctx context.Context, id string) error {
	n, err := p.q.MarkObjectDeleted(ctx, id)
	if err != nil {
		return fmt.Errorf("mark object %s deleted: %w", id, err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (p *Postgres) ClearCreatedBy(ctx context.Context, userID string) (int, error) {
	if userID == "" {
		return 0, fmt.Errorf("%w: empty userID", ErrInvalidQuery)
	}
	n, err := p.q.ClearCreatedBy(ctx, userID)
	if err != nil {
		return 0, fmt.Errorf("clear created_by for %s: %w", userID, err)
	}
	return int(n), nil
}

func (p *Postgres) TransferOwner(ctx context.Context, from, to Owner) (int, error) {
	if err := checkTransfer(from, to); err != nil {
		return 0, err
	}
	n, err := p.q.TransferOwner(ctx, db.TransferOwnerParams{
		FromKind: string(from.Kind), FromID: from.ID,
		ToKind: string(to.Kind), ToID: to.ID,
	})
	if err != nil {
		return 0, fmt.Errorf("transfer objects from %v to %v: %w", from, to, err)
	}
	return int(n), nil
}

func (p *Postgres) MarkDeletedByOwner(ctx context.Context, owner Owner) (int, error) {
	if err := checkOwnerArg(owner); err != nil {
		return 0, err
	}
	n, err := p.q.MarkDeletedByOwner(ctx, db.MarkDeletedByOwnerParams{OwnerKind: string(owner.Kind), OwnerID: owner.ID})
	if err != nil {
		return 0, fmt.Errorf("delete objects of %v: %w", owner, err)
	}
	return int(n), nil
}

// fromRow converts a database row to the domain type.
// Times are returned in UTC, matching Memory.
func fromRow(r db.StorageObject) Object {
	obj := Object{
		ID:          r.ID,
		Owner:       Owner{Kind: OwnerKind(r.OwnerKind), ID: r.OwnerID},
		CreatedBy:   deref(r.CreatedBy),
		ProjectID:   deref(r.ProjectID),
		Kind:        Kind(deref(r.Kind)),
		Bucket:      r.Bucket,
		Key:         r.Key,
		SizeBytes:   r.SizeBytes,
		ContentType: r.ContentType,
		CreatedAt:   r.CreatedAt.UTC(),
	}
	if r.DeletedAt != nil {
		t := r.DeletedAt.UTC()
		obj.DeletedAt = &t
	}
	return obj
}

// nullable maps Go's "" to SQL NULL.
func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// deref maps SQL NULL to Go's "".
func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func (p *Postgres) ListDeleted(ctx context.Context, before time.Time, limit int) ([]Object, error) {
	if err := checkLimit(limit); err != nil {
		return nil, err
	}
	rows, err := p.q.ListDeletedObjects(ctx, db.ListDeletedObjectsParams{Before: before, RowLimit: int32(limit)})
	if err != nil {
		return nil, fmt.Errorf("list deleted objects: %w", err)
	}
	objs := make([]Object, len(rows))
	for i, row := range rows {
		objs[i] = fromRow(row)
	}
	return objs, nil
}

func (p *Postgres) Purge(ctx context.Context, id string) error {
	n, err := p.q.PurgeObject(ctx, id)
	if err != nil {
		return fmt.Errorf("purge object %s: %w", id, err)
	}
	if n == 1 {
		return nil
	}
	// Nothing deleted: either the object is already gone (fine) or it's live.
	if _, err := p.Get(ctx, id); errors.Is(err, ErrNotFound) {
		return nil
	} else if err != nil {
		return err
	}
	return fmt.Errorf("%w: %s", ErrNotDeleted, id)
}
