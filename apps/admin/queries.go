package admin

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The admin reads iam.* and storage.* directly: it's a dev tool, exempt from
// the one-schema-per-service rule, and it never writes through SQL. Queries
// are hand-written rather than sqlc because they span both schemas. uuid
// columns are cast to text so they scan into strings.

// listLimit caps every list; the tool is for dev databases.
const listLimit = 500

type queries struct{ pool *pgxpool.Pool }

func collect[T any](ctx context.Context, q *queries, sql string, args ...any) ([]T, error) {
	rows, err := q.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[T])
}

func one[T any](ctx context.Context, q *queries, sql string, args ...any) (T, error) {
	rows, err := q.pool.Query(ctx, sql, args...)
	if err != nil {
		var zero T
		return zero, err
	}
	return pgx.CollectExactlyOneRow(rows, pgx.RowToStructByName[T])
}

// orphan is a SQL expression: whether the row aliased a has an owner that
// doesn't exist in iam (no such user, org or project). Storage never checks
// owners exist, so rows written by hand or by old runs can point at nobody,
// and then no actor may touch them. gyoza owners always exist.
func orphan(a string) string {
	return `(CASE ` + a + `.owner_kind
		WHEN 'user' THEN NOT EXISTS (SELECT 1 FROM iam.users ou WHERE ou.id = ` + a + `.owner_id)
		WHEN 'org' THEN NOT EXISTS (SELECT 1 FROM iam.orgs oo WHERE oo.id::text = ` + a + `.owner_id)
		WHEN 'project' THEN NOT EXISTS (SELECT 1 FROM iam.projects op WHERE op.id::text = ` + a + `.owner_id)
		ELSE false END)`
}

// OrphanOwner is an owner missing from iam, with how many rows of one table
// still point at it.
type OrphanOwner struct {
	OwnerKind string
	OwnerID   string
	TableName string
	N         int64
}

func (q *queries) orphans(ctx context.Context) ([]OrphanOwner, error) {
	sql := `SELECT * FROM (`
	for i, t := range []string{"iam.projects", "storage.assets", "storage.objects", "storage.folders", "storage.uploads"} {
		if i > 0 {
			sql += ` UNION ALL `
		}
		sql += `SELECT t.owner_kind::text AS owner_kind, t.owner_id::text AS owner_id, '` + t + `' AS table_name, count(*) AS n
			FROM ` + t + ` t WHERE ` + orphan("t") + ` GROUP BY t.owner_kind, t.owner_id`
	}
	return collect[OrphanOwner](ctx, q, sql+`) x ORDER BY owner_kind, owner_id, table_name`)
}

type Count struct {
	TableName string
	N         int64
}

func (q *queries) counts(ctx context.Context) ([]Count, error) {
	return collect[Count](ctx, q, `
		SELECT 'iam.users' AS table_name, count(*) AS n FROM iam.users
		UNION ALL SELECT 'iam.orgs', count(*) FROM iam.orgs
		UNION ALL SELECT 'iam.memberships', count(*) FROM iam.memberships
		UNION ALL SELECT 'iam.invites', count(*) FROM iam.invites
		UNION ALL SELECT 'iam.projects', count(*) FROM iam.projects
		UNION ALL SELECT 'iam.project_shares', count(*) FROM iam.project_shares
		UNION ALL SELECT 'iam.pending_shares', count(*) FROM iam.pending_shares
		UNION ALL SELECT 'storage.objects', count(*) FROM storage.objects
		UNION ALL SELECT 'storage.folders', count(*) FROM storage.folders
		UNION ALL SELECT 'storage.assets', count(*) FROM storage.assets
		UNION ALL SELECT 'storage.asset_versions', count(*) FROM storage.asset_versions
		UNION ALL SELECT 'storage.asset_uses', count(*) FROM storage.asset_uses
		UNION ALL SELECT 'storage.uploads', count(*) FROM storage.uploads
		UNION ALL SELECT 'storage.media_info', count(*) FROM storage.media_info`)
}

// --- users and orgs ---

type UserRow struct {
	ID            string
	Email         string
	Name          string
	EmailVerified bool
	CreatedAt     time.Time
	DeletingAt    *time.Time
	OrgID         *string
	OrgName       *string
	Role          *string
}

const userSelect = `
	SELECT u.id, u.email, u.name, u.email_verified, u.created_at, u.deleting_at,
	       m.org_id::text AS org_id, o.name AS org_name, m.role
	FROM iam.users u
	LEFT JOIN iam.memberships m ON m.user_id = u.id
	LEFT JOIN iam.orgs o ON o.id = m.org_id`

// users lists users matching search (id, email or name; empty matches all).
func (q *queries) users(ctx context.Context, search string) ([]UserRow, error) {
	return collect[UserRow](ctx, q, userSelect+`
		WHERE $1 = '' OR u.id ILIKE '%'||$1||'%' OR u.email ILIKE '%'||$1||'%' OR u.name ILIKE '%'||$1||'%'
		ORDER BY u.created_at DESC LIMIT $2`, search, listLimit)
}

func (q *queries) user(ctx context.Context, id string) (UserRow, error) {
	return one[UserRow](ctx, q, userSelect+` WHERE u.id = $1`, id)
}

type OrgRow struct {
	ID         string
	Name       string
	CreatedAt  time.Time
	DeletingAt *time.Time
	Members    int64
}

const orgSelect = `
	SELECT o.id::text AS id, o.name, o.created_at, o.deleting_at,
	       (SELECT count(*) FROM iam.memberships m WHERE m.org_id = o.id) AS members
	FROM iam.orgs o`

func (q *queries) orgs(ctx context.Context, search string) ([]OrgRow, error) {
	return collect[OrgRow](ctx, q, orgSelect+`
		WHERE $1 = '' OR o.id::text ILIKE '%'||$1||'%' OR o.name ILIKE '%'||$1||'%'
		ORDER BY o.created_at DESC LIMIT $2`, search, listLimit)
}

func (q *queries) org(ctx context.Context, id string) (OrgRow, error) {
	return one[OrgRow](ctx, q, orgSelect+` WHERE o.id::text = $1`, id)
}

type MemberRow struct {
	UserID   string
	Email    string
	Name     string
	Role     string
	JoinedAt time.Time
}

func (q *queries) members(ctx context.Context, orgID string) ([]MemberRow, error) {
	return collect[MemberRow](ctx, q, `
		SELECT m.user_id, u.email, u.name, m.role, m.joined_at
		FROM iam.memberships m JOIN iam.users u ON u.id = m.user_id
		WHERE m.org_id::text = $1 ORDER BY m.joined_at`, orgID)
}

type InviteRow struct {
	ID        string
	OrgID     string
	OrgName   string
	Email     string
	Role      string
	InvitedBy string
	CreatedAt time.Time
	ExpiresAt time.Time
}

// invites lists invites of an org (orgID set) or to an email (email set).
func (q *queries) invites(ctx context.Context, orgID, email string) ([]InviteRow, error) {
	return collect[InviteRow](ctx, q, `
		SELECT i.id::text AS id, i.org_id::text AS org_id, o.name AS org_name, i.email, i.role,
		       i.invited_by, i.created_at, i.expires_at
		FROM iam.invites i JOIN iam.orgs o ON o.id = i.org_id
		WHERE ($1 = '' OR i.org_id::text = $1) AND ($2 = '' OR i.email = lower($2))
		ORDER BY i.created_at DESC LIMIT $3`, orgID, email, listLimit)
}

// --- projects ---

type ProjectRow struct {
	ID         string
	OwnerKind  string
	OwnerID    string
	Orphan     bool
	OwnerName  string
	Name       string
	State      string
	ClonedFrom *string
	OrgLocked  bool
	CreatedBy  string
	CreatedAt  time.Time
	DeletingAt *time.Time
}

var projectSelect = `
	SELECT p.id::text AS id, p.owner_kind, p.owner_id,
	       COALESCE(u.name, o.name, 'gyoza') AS owner_name, ` + orphan("p") + ` AS orphan,
	       p.name, p.state, p.cloned_from::text AS cloned_from, p.org_locked,
	       p.created_by, p.created_at, p.deleting_at
	FROM iam.projects p
	LEFT JOIN iam.users u ON p.owner_kind = 'user' AND u.id = p.owner_id
	LEFT JOIN iam.orgs o ON p.owner_kind = 'org' AND o.id::text = p.owner_id`

// projects lists projects, optionally of one owner (ownerKind set).
func (q *queries) projects(ctx context.Context, ownerKind, ownerID, search string) ([]ProjectRow, error) {
	return collect[ProjectRow](ctx, q, projectSelect+`
		WHERE ($1 = '' OR (p.owner_kind = $1 AND p.owner_id = $2))
		  AND ($3 = '' OR p.id::text ILIKE '%'||$3||'%' OR p.name ILIKE '%'||$3||'%')
		ORDER BY p.created_at DESC LIMIT $4`, ownerKind, ownerID, search, listLimit)
}

func (q *queries) project(ctx context.Context, id string) (ProjectRow, error) {
	return one[ProjectRow](ctx, q, projectSelect+` WHERE p.id::text = $1`, id)
}

type ShareRow struct {
	ProjectID   string
	ProjectName string
	UserID      string
	Email       string
	Access      string
	SharedBy    string
	CreatedAt   time.Time
}

// shares lists shares of a project (projectID set) or to a user (userID set).
func (q *queries) shares(ctx context.Context, projectID, userID string) ([]ShareRow, error) {
	return collect[ShareRow](ctx, q, `
		SELECT s.project_id::text AS project_id, p.name AS project_name, s.user_id, u.email,
		       s.access, s.shared_by, s.created_at
		FROM iam.project_shares s
		JOIN iam.projects p ON p.id = s.project_id
		JOIN iam.users u ON u.id = s.user_id
		WHERE ($1 = '' OR s.project_id::text = $1) AND ($2 = '' OR s.user_id = $2)
		ORDER BY s.created_at DESC LIMIT $3`, projectID, userID, listLimit)
}

type PendingShareRow struct {
	Email     string
	Access    string
	SharedBy  string
	CreatedAt time.Time
	ExpiresAt time.Time
	Expired   bool
}

// pendingShares lists a project's shares to addresses with no verified
// account yet, expired ones included (the cleanup loop removes those).
func (q *queries) pendingShares(ctx context.Context, projectID string) ([]PendingShareRow, error) {
	return collect[PendingShareRow](ctx, q, `
		SELECT email, access, shared_by, created_at, expires_at, expires_at <= now() AS expired
		FROM iam.pending_shares
		WHERE project_id::text = $1
		ORDER BY created_at DESC LIMIT $2`, projectID, listLimit)
}

// --- assets ---

type AssetRow struct {
	ID             string
	OwnerKind      string
	OwnerID        string
	Orphan         bool
	ProjectID      string
	FolderID       *string
	FolderName     *string
	Name           string
	Description    string
	Tags           []string
	ContentType    string
	Category       string
	CurrentVersion int32
	CreatedBy      string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	DeletedAt      *time.Time
}

var assetSelect = `
	SELECT a.id::text AS id, a.owner_kind, a.owner_id, ` + orphan("a") + ` AS orphan, a.project_id,
	       a.folder_id::text AS folder_id, f.name AS folder_name, a.name, a.description, a.tags,
	       a.content_type, a.category, a.current_version, a.created_by,
	       a.created_at, a.updated_at, a.deleted_at
	FROM storage.assets a LEFT JOIN storage.folders f ON f.id = a.folder_id`

// assets lists assets, optionally of one owner or in one folder.
func (q *queries) assets(ctx context.Context, ownerKind, ownerID, folderID, search string, trashed bool) ([]AssetRow, error) {
	return collect[AssetRow](ctx, q, assetSelect+`
		WHERE ($1 = '' OR (a.owner_kind = $1 AND a.owner_id = $2))
		  AND ($3 = '' OR a.folder_id::text = $3)
		  AND ($4 = '' OR a.id::text ILIKE '%'||$4||'%' OR a.name ILIKE '%'||$4||'%')
		  AND ($5 OR a.deleted_at IS NULL)
		ORDER BY a.created_at DESC LIMIT $6`, ownerKind, ownerID, folderID, search, trashed, listLimit)
}

func (q *queries) asset(ctx context.Context, id string) (AssetRow, error) {
	return one[AssetRow](ctx, q, assetSelect+` WHERE a.id::text = $1`, id)
}

type VersionRow struct {
	Number      int32
	ObjectID    string
	ContentType string
	SizeBytes   int64
	CreatedBy   string
	CreatedAt   time.Time
	MediaStatus *string
	Width       *int32
	Height      *int32
	DurationUs  *int64
	MediaError  *string
	Details     *string
}

func (q *queries) versions(ctx context.Context, assetID string) ([]VersionRow, error) {
	return collect[VersionRow](ctx, q, `
		SELECT v.number, v.object_id, v.content_type, v.size_bytes, v.created_by, v.created_at,
		       mi.status AS media_status, mi.width, mi.height, mi.duration_us,
		       mi.error AS media_error, mi.details::text AS details
		FROM storage.asset_versions v
		LEFT JOIN storage.media_info mi ON mi.object_id = v.object_id
		WHERE v.asset_id::text = $1 ORDER BY v.number DESC`, assetID)
}

type UseRow struct {
	ProjectID   string
	ProjectName *string
	AssetID     string
	AssetName   string
	Version     int32
	CreatedAt   time.Time
}

// uses lists asset uses by a project (projectID set) or of an asset (assetID set).
func (q *queries) uses(ctx context.Context, projectID, assetID string) ([]UseRow, error) {
	return collect[UseRow](ctx, q, `
		SELECT au.project_id, p.name AS project_name, au.asset_id::text AS asset_id,
		       a.name AS asset_name, au.version, au.created_at
		FROM storage.asset_uses au
		JOIN storage.assets a ON a.id = au.asset_id
		LEFT JOIN iam.projects p ON p.id::text = au.project_id
		WHERE ($1 = '' OR au.project_id = $1) AND ($2 = '' OR au.asset_id::text = $2)
		ORDER BY au.created_at DESC LIMIT $3`, projectID, assetID, listLimit)
}

// WritableProject is a project the actor may write, for the use pickers.
type WritableProject struct {
	ID     string
	Name   string
	Access string // why: owner, org or editor share
}

// writableProjects lists the ready projects actorID can write: their own,
// their org's (unless they're being deleted), and those shared with them as
// editor. It mirrors iam's rules loosely; the service has the last word.
func (q *queries) writableProjects(ctx context.Context, actorID string) ([]WritableProject, error) {
	return collect[WritableProject](ctx, q, `
		SELECT p.id::text AS id, p.name,
		       CASE WHEN p.owner_kind = 'user' THEN 'owner'
		            WHEN m.user_id IS NOT NULL THEN 'org'
		            ELSE 'editor share' END AS access
		FROM iam.projects p
		LEFT JOIN iam.memberships m ON p.owner_kind = 'org' AND m.org_id::text = p.owner_id AND m.user_id = $1
		LEFT JOIN iam.project_shares s ON s.project_id = p.id AND s.user_id = $1 AND s.access = 'editor'
		WHERE p.state = 'ready' AND p.deleting_at IS NULL
		  AND ((p.owner_kind = 'user' AND p.owner_id = $1) OR m.user_id IS NOT NULL OR s.user_id IS NOT NULL)
		ORDER BY p.name LIMIT $2`, actorID, listLimit)
}

// usableAssets lists the live assets projectID can use in place: its own,
// its owner's, and gyoza's (see storage's RecordAssetUse).
func (q *queries) usableAssets(ctx context.Context, projectID string) ([]AssetRow, error) {
	return collect[AssetRow](ctx, q, assetSelect+`
		JOIN iam.projects p ON p.id::text = $1
		WHERE a.deleted_at IS NULL
		  AND ((a.owner_kind = 'project' AND a.owner_id = $1)
		    OR (a.owner_kind = p.owner_kind AND a.owner_id = p.owner_id)
		    OR a.owner_kind = 'gyoza')
		ORDER BY a.name LIMIT $2`, projectID, listLimit)
}

type FolderRow struct {
	ID        string
	OwnerKind string
	OwnerID   string
	Orphan    bool
	ParentID  *string
	Name      string
	CreatedBy string
	CreatedAt time.Time
	DeletedAt *time.Time
	Assets    int64
	Depth     int32
}

// folders returns an owner's folders depth-first (parents before children),
// trashed ones included.
func (q *queries) folders(ctx context.Context, ownerKind, ownerID string) ([]FolderRow, error) {
	return collect[FolderRow](ctx, q, `
		WITH RECURSIVE tree AS (
			SELECT f.*, 0 AS depth, ARRAY[f.name_key || f.id::text] AS path
			FROM storage.folders f
			WHERE f.owner_kind = $1 AND f.owner_id = $2 AND f.parent_id IS NULL
			UNION ALL
			SELECT f.*, t.depth + 1, t.path || (f.name_key || f.id::text)
			FROM storage.folders f JOIN tree t ON f.parent_id = t.id
		)
		SELECT t.id::text AS id, t.owner_kind, t.owner_id, `+orphan("t")+` AS orphan, t.parent_id::text AS parent_id, t.name,
		       t.created_by, t.created_at, t.deleted_at, t.depth,
		       (SELECT count(*) FROM storage.assets a WHERE a.folder_id = t.id) AS assets
		FROM tree t ORDER BY t.path`, ownerKind, ownerID)
}

// --- ledger and uploads ---

type ObjectRow struct {
	ID          string
	OwnerKind   string
	OwnerID     string
	Orphan      bool
	CreatedBy   *string
	ProjectID   *string
	Kind        *string
	Bucket      string
	Key         string
	SizeBytes   int64
	ContentType string
	CreatedAt   time.Time
	DeletedAt   *time.Time
}

func (q *queries) objects(ctx context.Context, ownerKind, ownerID, search string) ([]ObjectRow, error) {
	return collect[ObjectRow](ctx, q, `
		SELECT id, owner_kind, owner_id, `+orphan("storage.objects")+` AS orphan, created_by, project_id, kind, bucket, key,
		       size_bytes, content_type, created_at, deleted_at
		FROM storage.objects
		WHERE ($1 = '' OR (owner_kind = $1 AND owner_id = $2))
		  AND ($3 = '' OR id ILIKE '%'||$3||'%' OR key ILIKE '%'||$3||'%')
		ORDER BY created_at DESC LIMIT $4`, ownerKind, ownerID, search, listLimit)
}

type OwnerUsage struct {
	Objects int64
	Bytes   int64
}

// usage sums an owner's live ledger objects.
func (q *queries) usage(ctx context.Context, ownerKind, ownerID string) (OwnerUsage, error) {
	return one[OwnerUsage](ctx, q, `
		SELECT count(*) AS objects, COALESCE(sum(size_bytes), 0)::bigint AS bytes
		FROM storage.objects
		WHERE owner_kind = $1 AND owner_id = $2 AND deleted_at IS NULL`, ownerKind, ownerID)
}

type UploadRow struct {
	ID            string
	OwnerKind     string
	OwnerID       string
	Orphan        bool
	ProjectID     string
	CreatedBy     string
	AssetID       *string
	Name          string
	ContentType   string
	SizeBytes     int64
	ObjectID      string
	ExpiresAt     time.Time
	CreatedAt     time.Time
	CompletedAt   *time.Time
	ResultAssetID *string
	ResultVersion int32
}

func (q *queries) uploads(ctx context.Context) ([]UploadRow, error) {
	return collect[UploadRow](ctx, q, `
		SELECT id::text AS id, owner_kind, owner_id, `+orphan("storage.uploads")+` AS orphan, project_id, created_by,
		       asset_id::text AS asset_id, name, content_type, size_bytes, object_id,
		       expires_at, created_at, completed_at,
		       result_asset_id::text AS result_asset_id, result_version
		FROM storage.uploads ORDER BY created_at DESC LIMIT $1`, listLimit)
}

// SnapshotRow is one of a project's stored document snapshots.
type SnapshotRow struct {
	ObjectID    string
	Version     int32
	Seq         int64
	EditedBy    *string
	EditedAt    *time.Time
	ContentType string
	SizeBytes   int64
	CompletedAt *time.Time
}

// snapshots lists a project's completed snapshots, newest first: the
// versions it can be reverted to (storage keeps the newest GYOZA_SNAPSHOT_KEEP).
func (q *queries) snapshots(ctx context.Context, projectID string) ([]SnapshotRow, error) {
	return collect[SnapshotRow](ctx, q, `
		SELECT object_id, version, seq, edited_by, edited_at, content_type, size_bytes, completed_at
		FROM storage.snapshots
		WHERE project_id = $1 AND version IS NOT NULL
		ORDER BY version DESC LIMIT $2`, projectID, listLimit)
}
