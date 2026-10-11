# IAM: Optimizations

Known performance issues, what was done about them, and what is deliberately
left until it matters. Add an entry whenever you find or defer one, with the
evidence, so the reasoning survives.

## Done

None yet.

## Open

### An authorization check is several Store reads

- **Expected:** `StorageAuthorizer` runs on every storagemanager request. One check
  reads the user, their membership, the project and their share, and the user and
  membership are read twice when the project route is used (once by the authorizer,
  once by `accessTo`). With MemoryStore that's free; with Postgres it's 4-6 round trips.
- **When to act:** step 8 (Postgres store): measure, and before storagemanager runs with
  the real authorizer in production.
- **Fix:** one query that returns everything `projectAccess` needs (project, membership,
  share) for an actor and project, behind a Store method; pass the actor's org into the
  project route instead of looking it up again. Later, a short-lived cache in
  distributed mode, where each check is also a gRPC call.
