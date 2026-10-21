# Storage: Optimizations

Known performance issues, what was done about them, and what is deliberately
left until it matters. Add an entry whenever you find or defer one, with the
evidence (a measurement or a plan), so the reasoning survives.

## Ledger (`storage.objects`)

Measurements below come from `EXPLAIN (ANALYZE)` on 320k fake rows loaded into
a rolled-back transaction on the local dev database (Postgres 18), including
one owner with 20k objects.

### Rules the queries depend on

- `List` is one query per call, bounded by the page size. No N+1.
- The scope and the cursor must stay plain `=` and row `>` comparisons so
  Postgres uses them as index conditions. `IS NOT DISTINCT FROM` and
  `$x IS NULL OR ...` look harmless but stop the index from being used.
- The pool must set `plan_cache_mode=force_custom_plan` (done in
  `platform/postgres.Connect`, checked by its tests).

### Done

#### Owner listing scanned the whole table

- **Found:** `owner_id IS NOT DISTINCT FROM $2` (so Modulith's NULL owner ID
  matched) can't use an index. Listing by owner was a sequential scan of the
  entire table: 131ms to return 29 rows.
- **Fix:** the schema stores `''` instead of NULL for Modulith and the
  query uses `owner_id = $2`. Now an index scan.

#### Later pages re-read every earlier row

- **Found:** `($cursor IS NULL OR (created_at, id) > $cursor)` kept the cursor
  out of the index condition, so each page read and discarded every row
  before it, like `OFFSET`.
- **Fix:** the query always takes a cursor; the first page passes the zero
  cursor (`cursor{}` in Go). The cursor is now an index condition.

#### Cached generic plans read the whole scope

- **Found:** pgx uses prepared statements, and Postgres may switch them to a
  generic plan that can't see the parameter values. It assumed every owner is
  small (estimated 12 rows), chose a bitmap scan of the whole scope plus a
  sort: 7ms for the first page of a 20k-object owner, growing linearly with
  the owner's size.
- **Fix:** `plan_cache_mode=force_custom_plan` on the pool. Every page is now
  an ordered index scan that stops at the limit: ~0.05ms at any page depth.
  Planning costs ~0.03ms per query.

#### Purge listing

- **Done with the code:** `ListDeleted` (the cleanup job's work list) uses
  a partial index on `(deleted_at, id) WHERE deleted_at IS
  NOT NULL`: only deleted rows, so it stays small however many objects are
  live, and the oldest-first query stops at the batch size.

### Open

#### Rare filter values scan the whole scope

- **Found:** optional filters (`Kind`, `CreatedBy`) are checked on rows the
  index returns. Finding 20 exports among a 20k-object owner read all 20k
  rows: 4ms.
- **When to act:** a real caller filters by a rare value on large scopes.
- **Fix:** an index with the filter column before the page columns, e.g.
  `(owner_kind, owner_id, kind, created_at, id)`, plus a query variant that
  uses it. Each index slows writes, so only for filters that are actually used.

#### Soft-deleted rows stay in the indexes

- **Expected:** deleted rows are skipped by a filter, not by the index. If
  deletions pile up, every List reads and discards them.
- **Partly addressed:** the cleanup job now purges deleted
  objects after the retention period (7 days), so deleted rows can only pile
  up for that long.
- **When to act:** deletions within one retention period are a meaningful
  share of the rows.
- **Fix:** partial indexes `WHERE deleted_at IS NULL` for the default
  (non-deleted) listing.

#### Random IDs scatter primary-key inserts

- **Expected:** IDs come from `crypto/rand.Text()`, so each insert lands on a
  random page of the primary-key B-tree. At high insert rates that means more
  page splits and a larger working set than time-ordered keys.
- **When to act:** insert throughput becomes a bottleneck.
- **Fix:** time-ordered IDs (e.g. UUIDv7). Existing IDs can stay; only the
  generator changes.
- **Precedent:** the DAM tables use UUIDv7 from the start (`newID` in
  `internal/manager/assets_rules.go`, `github.com/google/uuid`), which
  also let it page by ID alone. The ledger keeps `rand.Text()` until this
  item's trigger is hit.

#### force_custom_plan applies to every query on the pool

- **Expected:** in the monolith all services share one pool, so every query
  is re-planned on each run, not only the ledger's List.
- **When to act:** planning time shows up in profiles for some other
  service's hot query.
- **Fix:** give that service's statements a different exec mode, or set
  `plan_cache_mode` per transaction for the List queries only.

#### ClearCreatedBy is one UPDATE

- **Expected:** a user who created a very large number of objects makes this a
  single long-running update that locks all those rows at once.
- **When to act:** user deletion for heavy users takes noticeably long or
  blocks other writes.
- **Fix:** update in batches (e.g. 10k rows per statement) until 0 rows change.

## DAM (folders, assets, versions, uploads)

### Done

#### DownloadURL loaded every version to serve one

- **Found:** `DownloadURL` called `ListVersions` and picked one entry.
- **Fix:** `Store.GetVersion(assetID, number)`; one indexed row in Postgres.

#### Folder contents scanned the whole assets table

- **Found** (EXPLAIN ANALYZE, 200k assets, one owner with 50k, Postgres 18):
  the generic listing's `folder_id IS NOT DISTINCT FROM $x` can't use an
  index, so the file browser's main query was a parallel seq scan + sort:
  33ms, growing with the table. Same trap as the ledger's owner query.
- **Fix:** dedicated `ListAssetsAtTop` (`folder_id IS NULL`) and
  `ListAssetsInFolder` (`folder_id = $x`) queries for owner-scoped folder
  listings, and the `assets_folder_page` index. Now an index scan
  that stops at the page limit: 0.036ms.
- Plain owner listing was already an index scan: 0.036ms for the first page of 50k.

#### Cleanup listings

- **Done with the code:** partial indexes on trashed
  assets and folders (`deleted_at, id`) and completed uploads
  (`completed_at, id`), holding only those rows, so the cleanup's
  oldest-first listings are index scans that stop at the batch size.

### Open

#### Tag filters search every owner's tags

- **Found:** a rare tag in a large owner's library uses the GIN index on
  `tags`, which returns that tag's assets across _all_ owners, then filters to
  the owner and sorts: 26ms for 500 tagged rows (fine). A tag common across
  many owners (e.g. "logo" in every library) would make this much slower.
- **When to act:** tag filtering shows up slow in profiles.
- **Fix:** a composite GIN index on (owner, tags) via the `btree_gin`
  extension, or per-owner tag tables.

#### Postgres store concurrency isn't tested

- **Expected:** the shared suite runs each case in one transaction on one
  connection, so row locks (`FOR UPDATE`), the per-owner advisory lock and the
  unique-index backstop are written for concurrency but only exercised serially.
  The forced-overlap tests (`gatedBlobs`/`gatedStore`) use the memory store.
- **When to act:** before production traffic.
- **Fix:** a Postgres-only test that runs overlapping `CompleteUpload`,
  `MoveFolder` and `CreateFolder` calls on separate pool connections.

#### MemoryStore scans whole maps

- **Expected:** folder children, subtree walks, live-asset counts and
  `ListAssets` iterate every folder/asset in the store. Fine for tests and
  local runs; it's the reference behaviour, not the production path.
- **When to act:** never for production (Postgres in step 7 uses indexes);
  only if tests get slow.
- **Fix:** none planned.

#### A failing item blocks its cleanup step

- **Expected:** each cleanup step works through the oldest items first and
  stops at the first failure, so an item that always fails (e.g. a file S3
  refuses to delete) is retried at the head of the list every run and the
  items behind it wait. Errors are logged every run, so it's visible.
- **When to act:** the cleanup log shows the same item failing run after run.
- **Fix:** record failures (attempt count, last error) and skip items that
  failed recently, or move them to a dead-letter list.

#### Every replica runs the whole cleanup

- **Expected:** `RunCleanup` runs in every process hosting the Manager, so N
  replicas do the same work N times (safely: every step is idempotent, and
  Postgres row locks keep two folder purges apart). Cheap while there's
  little to clean.
- **When to act:** cleanup load shows up in S3 request costs or database load.
- **Fix:** a Postgres advisory lock (`pg_try_advisory_lock`) so one replica
  runs each round, or a shared job queue on JetStream + Postgres if one is built.

## Snapshots (`storage.snapshots`)

### Open

#### Retention ranks every snapshot on each cleanup run

- **Now:** `ListSnapshotsBeyondKeep` numbers every completed snapshot per
  project (`row_number()` over the whole table) to find those beyond the
  newest `SnapshotKeep`, on every cleanup run (default every 10 minutes).
- **Cost:** a full scan of `storage.snapshots`: at most 100 rows per project
  once retention has caught up, so it grows with the number of projects. Not
  measured; cheap while projects are few.
- **Fix:** track projects that gained a snapshot since the last run, or prune
  each project right after `CompleteSnapshot` (delete versions at or below
  `version - keep`), which needs no scan at all.
- **When to act:** cleanup query time shows up, or projects reach the tens of
  thousands.
