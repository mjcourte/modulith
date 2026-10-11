# Storage Manager: Optimizations

Known performance issues, what was done about them, and what is deliberately
left until it matters. Add an entry whenever you find or defer one, with the
evidence (a measurement or a plan), so the reasoning survives.

## Done

### DownloadURL loaded every version to serve one

- **Found:** `DownloadURL` called `ListVersions` and picked one entry.
- **Fix:** `Store.GetVersion(assetID, number)`; one indexed row in Postgres.

### Folder contents scanned the whole assets table

- **Found** (EXPLAIN ANALYZE, 200k assets, one owner with 50k, Postgres 18):
  the generic listing's `folder_id IS NOT DISTINCT FROM $x` can't use an
  index, so the file browser's main query was a parallel seq scan + sort:
  33ms, growing with the table. Same trap as the ledger's owner query.
- **Fix:** dedicated `ListAssetsAtTop` (`folder_id IS NULL`) and
  `ListAssetsInFolder` (`folder_id = $x`) queries for owner-scoped folder
  listings, and migration 00002's `assets_folder_page` index. Now an index scan
  that stops at the page limit: 0.036ms.
- Plain owner listing was already an index scan: 0.036ms for the first page of 50k.

### Cleanup listings

- **Done with the code:** migration 00003 adds partial indexes on trashed
  assets and folders (`deleted_at, id`) and completed uploads
  (`completed_at, id`), holding only those rows, so the cleanup's
  oldest-first listings are index scans that stop at the batch size.

## Open

### Tag filters search every owner's tags

- **Found:** a rare tag in a large owner's library uses the GIN index on
  `tags`, which returns that tag's assets across _all_ owners, then filters to
  the owner and sorts: 26ms for 500 tagged rows (fine). A tag common across
  many owners (e.g. "logo" in every library) would make this much slower.
- **When to act:** tag filtering shows up slow in profiles.
- **Fix:** a composite GIN index on (owner, tags) via the `btree_gin`
  extension, or per-owner tag tables.

### Postgres store concurrency isn't tested

- **Expected:** the shared suite runs each case in one transaction on one
  connection, so row locks (`FOR UPDATE`), the per-owner advisory lock and the
  unique-index backstop are written for concurrency but only exercised serially.
  The forced-overlap tests (`gatedBlobs`/`gatedStore`) use the memory store.
- **When to act:** before production traffic.
- **Fix:** a Postgres-only test that runs overlapping `CompleteUpload`,
  `MoveFolder` and `CreateFolder` calls on separate pool connections.

### MemoryStore scans whole maps

- **Expected:** folder children, subtree walks, live-asset counts and
  `ListAssets` iterate every folder/asset in the store. Fine for tests and
  local runs; it's the reference behaviour, not the production path.
- **When to act:** never for production (Postgres in step 7 uses indexes);
  only if tests get slow.
- **Fix:** none planned.

### A failing item blocks its cleanup step

- **Expected:** each cleanup step works through the oldest items first and
  stops at the first failure, so an item that always fails (e.g. a file S3
  refuses to delete) is retried at the head of the list every run and the
  items behind it wait. Errors are logged every run, so it's visible.
- **When to act:** the cleanup log shows the same item failing run after run.
- **Fix:** record failures (attempt count, last error) and skip items that
  failed recently, or move them to a dead-letter list.

### Every replica runs the whole cleanup

- **Expected:** `RunCleanup` runs in every process hosting the Manager, so N
  replicas do the same work N times (safely: every step is idempotent, and
  Postgres row locks keep two folder purges apart). Cheap while there's
  little to clean.
- **When to act:** cleanup load shows up in S3 request costs or database load.
- **Fix:** a Postgres advisory lock (`pg_try_advisory_lock`) so one replica
  runs each round, or move the job to `platform/jobs` once it exists.
