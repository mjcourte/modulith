# Storage Ledger: Optimizations

Known performance issues, what was done about them, and what is deliberately
left until it matters. Add an entry whenever you find or defer one, with the
evidence (a measurement or a plan), so the reasoning survives.

Measurements below come from `EXPLAIN (ANALYZE)` on 320k fake rows loaded into
a rolled-back transaction on the local dev database (Postgres 18), including
one owner with 20k objects.

## Rules the queries depend on

- `List` is one query per call, bounded by the page size. No N+1.
- The scope and the cursor must stay plain `=` and row `>` comparisons so
  Postgres uses them as index conditions. `IS NOT DISTINCT FROM` and
  `$x IS NULL OR ...` look harmless but stop the index from being used.
- The pool must set `plan_cache_mode=force_custom_plan` (done in
  `platform/postgres.Connect`, checked by its tests).

## Done

### Owner listing scanned the whole table

- **Found:** `owner_id IS NOT DISTINCT FROM $2` (so Gyoza's NULL owner ID
  matched) can't use an index. Listing by owner was a sequential scan of the
  entire table: 131ms to return 29 rows.
- **Fix:** migration `00002` stores `''` instead of NULL for Gyoza and the
  query uses `owner_id = $2`. Now an index scan.

### Later pages re-read every earlier row

- **Found:** `($cursor IS NULL OR (created_at, id) > $cursor)` kept the cursor
  out of the index condition, so each page read and discarded every row
  before it, like `OFFSET`.
- **Fix:** the query always takes a cursor; the first page passes the zero
  cursor (`cursor{}` in Go). The cursor is now an index condition.

### Cached generic plans read the whole scope

- **Found:** pgx uses prepared statements, and Postgres may switch them to a
  generic plan that can't see the parameter values. It assumed every owner is
  small (estimated 12 rows), chose a bitmap scan of the whole scope plus a
  sort: 7ms for the first page of a 20k-object owner, growing linearly with
  the owner's size.
- **Fix:** `plan_cache_mode=force_custom_plan` on the pool. Every page is now
  an ordered index scan that stops at the limit: ~0.05ms at any page depth.
  Planning costs ~0.03ms per query.

## Open

### Rare filter values scan the whole scope

- **Found:** optional filters (`Kind`, `CreatedBy`) are checked on rows the
  index returns. Finding 20 exports among a 20k-object owner read all 20k
  rows: 4ms.
- **When to act:** a real caller filters by a rare value on large scopes.
- **Fix:** an index with the filter column before the page columns, e.g.
  `(owner_kind, owner_id, kind, created_at, id)`, plus a query variant that
  uses it. Each index slows writes, so only for filters that are actually used.

### Soft-deleted rows stay in the indexes

- **Expected:** deleted rows are skipped by a filter, not by the index. If
  deletions pile up, every List reads and discards them.
- **When to act:** a meaningful share of rows are deleted, or the hard-purge
  question (README) is answered.
- **Fix:** hard-purge after retention, or partial indexes `WHERE deleted_at IS NULL`
  for the default (non-deleted) listing.

### Random IDs scatter primary-key inserts

- **Expected:** IDs come from `crypto/rand.Text()`, so each insert lands on a
  random page of the primary-key B-tree. At high insert rates that means more
  page splits and a larger working set than time-ordered keys.
- **When to act:** insert throughput becomes a bottleneck.
- **Fix:** time-ordered IDs (e.g. UUIDv7). Existing IDs can stay; only the
  generator changes.
- **Precedent:** storagemanager uses UUIDv7 from the start (`newID` in
  `services/storagemanager/assets_rules.go`, `github.com/google/uuid`), which
  also let it page by ID alone. The ledger keeps `rand.Text()` until this
  item's trigger is hit.

### force_custom_plan applies to every query on the pool

- **Expected:** in the monolith all services share one pool, so every query
  is re-planned on each run, not only the ledger's List.
- **When to act:** planning time shows up in profiles for some other
  service's hot query.
- **Fix:** give that service's statements a different exec mode, or set
  `plan_cache_mode` per transaction for the List queries only.

### ClearCreatedBy is one UPDATE

- **Expected:** a user who created a very large number of objects makes this a
  single long-running update that locks all those rows at once.
- **When to act:** user deletion for heavy users takes noticeably long or
  blocks other writes.
- **Fix:** update in batches (e.g. 10k rows per statement) until 0 rows change.
