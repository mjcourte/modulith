# Plan: iam (`services/iam`)

Steps 1-9 are done (2026-10-12): users, orgs, invites, projects and sharing; access
rules and storage's `Authorizer`; deletion cascades; cloning (document side included); the Postgres store;
gRPC and binaries. Their record, decisions included, is the README. Related:
[gateway/PLAN.md](../gateway/PLAN.md) (auth and sign-in),
[documents/PLAN.md](../documents/PLAN.md) (the document side of cloning),
[apps/web/PLAN.md](../../apps/web/PLAN.md).

## Open

- `go test -race` hasn't run (no C compiler on the dev machine)
- a `buf curl` smoke run of `CloneProject` against the containers (the step 9 run
  predates it)

## Not planned

Billing, email sending for invites, a seeding/admin tool that creates and fills modulith
projects.
