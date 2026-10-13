# Documents: Optimizations

Known performance issues, what was done about them, and what is deliberately
left until it matters. Add an entry whenever you find or defer one, with the
evidence (a measurement or a plan), so the reasoning survives.

## Open

#### Asset uses are re-recorded after every load

- **Now:** the manager doesn't know which uses storage holds when it loads a
  document, so the first edit after a load calls `RecordAssetUse` for every
  asset version the document uses (`internal/manager/uses.go`). The calls are
  idempotent; this is what heals a record that failed before a restart.
- **Cost:** one call per distinct asset version, once per load. Documents
  use tens of assets, and loads are rare (TTL 10m).
- **Fix:** a batch `SyncAssetUses(project, refs)` in storage that sets the
  whole set in one call. That would also clear a forget that failed before a
  restart, which today stays stale.
- **When to act:** loads become frequent or documents use hundreds of assets.

#### Per-project usage state is never dropped

- **Now:** `Manager.projectUses` keeps one small entry per project ever
  edited by this process, evicted or not.
- **Cost:** a mutex and a slice of refs per project; negligible until a
  process has touched very many projects.
- **Fix:** drop the entry when live evicts the document (an eviction hook).
- **When to act:** memory profiles of a long-running instance show it.
