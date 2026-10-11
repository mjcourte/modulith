package storageledger

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// OwnerKind says which kind of principal owns an object.
type OwnerKind string

const (
	OwnerProject OwnerKind = "project"
	OwnerUser    OwnerKind = "user"
	OwnerOrg     OwnerKind = "org"
	OwnerGyoza   OwnerKind = "gyoza" // shared/common files owned by the platform; ID is empty
)

// Owner is the single principal that owns an object. An object's lifetime
// follows its owner: deleting the owner deletes everything it owns.
// The zero value means "no owner set".
type Owner struct {
	Kind OwnerKind
	ID   string
}

// IsZero reports whether no owner is set.
func (o Owner) IsZero() bool { return o == Owner{} }

// Validate reports whether o is well-formed: a known kind, with an ID for
// everything except gyoza. It checks shape only, not whether the owner exists
// or who may act for it; other services use it so everyone agrees on what a
// valid owner looks like.
func (o Owner) Validate() error {
	switch o.Kind {
	case OwnerUser, OwnerOrg, OwnerProject:
		if o.ID == "" {
			return fmt.Errorf("%s owner needs an ID", o.Kind)
		}
	case OwnerGyoza:
		if o.ID != "" {
			return errors.New("gyoza owner must not have an ID")
		}
	case "":
		return errors.New("owner is not set")
	default:
		return fmt.Errorf("unknown owner kind %q", o.Kind)
	}
	return nil
}

// Kind is what an object is used for.
type Kind string

const (
	KindSnapshot Kind = "snapshot" // document snapshot written by the document manager
	KindAsset    Kind = "asset"    // user-uploaded or generated asset
	KindExport   Kind = "export"   // rendered output
)

// Object is one S3 object recorded in the ledger.
type Object struct {
	ID          string
	Owner       Owner
	CreatedBy   string // user ID that uploaded/produced it; empty for platform-created objects
	ProjectID   string // optional: the project (and so the document) it is associated with, whoever owns it
	Kind        Kind
	Bucket      string
	Key         string
	SizeBytes   int64
	ContentType string
	CreatedAt   time.Time
	DeletedAt   *time.Time // nil until soft-deleted
}

var (
	ErrNotFound      = errors.New("storageledger: object not found")
	ErrInvalidQuery  = errors.New("storageledger: invalid query")
	ErrInvalidObject = errors.New("storageledger: invalid object")
)

// normalize validates obj for Create and fills in ProjectID for
// project-owned objects. Shared by every implementation.
func (obj Object) normalize() (Object, error) {
	if err := obj.Owner.Validate(); err != nil {
		// Two %w: the result matches both ErrInvalidObject and err with errors.Is.
		return obj, fmt.Errorf("%w: %w", ErrInvalidObject, err)
	}
	// A project-owned object is always associated with its own project,
	// so listing by ProjectID includes it.
	if obj.Owner.Kind == OwnerProject {
		switch obj.ProjectID {
		case "":
			obj.ProjectID = obj.Owner.ID
		case obj.Owner.ID:
		default:
			return obj, fmt.Errorf("%w: owned by project %s but ProjectID is %s", ErrInvalidObject, obj.Owner.ID, obj.ProjectID)
		}
	}
	return obj, nil
}

const (
	DefaultPageSize = 50
	MaxPageSize     = 500
)

// ListQuery selects objects within exactly one scope: a project or an owner.
// Everything else narrows the results within that scope.
type ListQuery struct {
	// Scope: set exactly one.
	ProjectID string
	Owner     Owner

	// Optional filters.
	CreatedBy      string
	Kind           Kind
	IncludeDeleted bool

	// Pagination. PageSize 0 means DefaultPageSize; values above MaxPageSize are capped.
	// PageToken is the NextPageToken from the previous page, or empty for the first page.
	PageSize  int
	PageToken string
}

// ListPage is one page of results, oldest first.
// NextPageToken is empty when there are no more results.
type ListPage struct {
	Objects       []Object
	NextPageToken string
}

type Service interface {
	Create(ctx context.Context, obj Object) (Object, error)
	Get(ctx context.Context, id string) (Object, error)
	List(ctx context.Context, q ListQuery) (ListPage, error)
	MarkDeleted(ctx context.Context, id string) error

	// ClearCreatedBy removes userID from CreatedBy on every object, deleted or not,
	// when that user is deleted. It returns how many objects were changed.
	ClearCreatedBy(ctx context.Context, userID string) (int, error)
}

// normalize validates q and fills in the default page size.
// Shared by every implementation so they reject the same queries.
func (q ListQuery) normalize() (ListQuery, error) {
	hasProject, hasOwner := q.ProjectID != "", !q.Owner.IsZero()
	if hasProject == hasOwner {
		return q, fmt.Errorf("%w: set exactly one of ProjectID or Owner", ErrInvalidQuery)
	}
	switch {
	case q.PageSize < 0:
		return q, fmt.Errorf("%w: negative PageSize", ErrInvalidQuery)
	case q.PageSize == 0:
		q.PageSize = DefaultPageSize
	case q.PageSize > MaxPageSize:
		q.PageSize = MaxPageSize
	}
	return q, nil
}

// pageOf turns up to pageSize+1 sorted matches into a page; the extra
// match, if present, only signals that another page exists.
func pageOf(matches []Object, pageSize int) ListPage {
	if len(matches) <= pageSize {
		return ListPage{Objects: matches}
	}
	matches = matches[:pageSize]
	return ListPage{Objects: matches, NextPageToken: encodeToken(matches[pageSize-1])}
}

// cursor is the position after which the next page starts.
// Results are ordered by (CreatedAt, ID), which maps directly to a Postgres
// keyset query: WHERE (created_at, id) > ($1, $2) ORDER BY created_at, id.
// The zero cursor sorts before every object, so it means "from the start".
type cursor struct {
	createdAt time.Time
	id        string
}

// cursor returns where the requested page starts.
func (q ListQuery) cursor() (cursor, error) {
	if q.PageToken == "" {
		return cursor{}, nil
	}
	return decodeToken(q.PageToken)
}

func (c cursor) before(o Object) bool {
	if t := c.createdAt.Compare(o.CreatedAt); t != 0 {
		return t < 0
	}
	return c.id < o.ID
}

// Page tokens are opaque to callers; the encoding is an implementation detail.
func encodeToken(o Object) string {
	raw := strconv.FormatInt(o.CreatedAt.UnixMicro(), 10) + ":" + o.ID
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeToken(token string) (cursor, error) {
	bad := fmt.Errorf("%w: bad page token", ErrInvalidQuery)
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return cursor{}, bad
	}
	micros, id, ok := strings.Cut(string(raw), ":")
	if !ok {
		return cursor{}, bad
	}
	us, err := strconv.ParseInt(micros, 10, 64)
	if err != nil {
		return cursor{}, bad
	}
	return cursor{createdAt: time.UnixMicro(us).UTC(), id: id}, nil
}
