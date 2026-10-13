# IAM: Optimizations

Known performance issues, what was done about them, and what is deliberately
left until it matters. Add an entry whenever you find or defer one, with the
evidence, so the reasoning survives.

## Done

### "My projects" scanned every project (2026-10-12)

- **Found:** EXPLAIN of `ListVisibleProjects` with 50k users, 5k orgs, 200k projects and
  67k shares (seeded in a rolled-back transaction): one `OR` over the three routes (own,
  org's, shared) made Postgres walk the primary key and test every row, 200,000 rows
  removed by the filter, 404,054 buffer hits, 233 ms, for one page.
- **Fix:** a `UNION ALL` of three branches, each a page from its own index
  (`projects_owner_page` twice, `project_shares_user`), merged and cut to the page size.
  Same rows; 13 buffer hits.

## Open

### An authorization check is several Store reads

- **Expected:** `StorageAuthorizer` runs on every storage request. One check
  reads the user, their membership, the project and their share, and the user and
  membership are read twice when the project route is used (once by the authorizer,
  once by `accessTo`). With MemoryStore that's free; with Postgres it's 4-6 round trips.
- **Measured (step 8, same data):** each lookup is a primary-key or index scan of 2-3
  buffer hits (`users_pkey`, `memberships_pkey`, `project_shares_user`), so a check costs
  round trips, not work. Distributed, each check is also a gRPC call from storage to iam.
- **When to act:** when storage request latency shows it, before launch at the latest.
- **Fix:** one query that returns everything `projectAccess` needs (project, membership,
  share) for an actor and project, behind a Store method; pass the actor's org into the
  project route instead of looking it up again. Later, a short-lived cache in
  distributed mode, where each check is also a gRPC call.
