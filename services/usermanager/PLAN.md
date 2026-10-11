# Plan: user-manager (`services/usermanager`)

## Context

storagemanager authorizes with `AllowAll` until something can answer "may this user
read/write these files?". usermanager owns users, orgs, membership, projects and
sharing, implements storagemanager's `Authorizer`, and drives the cascades already
built (`DeleteOwned`, `RemoveUser`). Identity comes from SuperTokens (Google sign-in)
via the gateway; usermanager never talks to SuperTokens directly.

Related plans: auth and sign-in in [gateway/PLAN.md](../gateway/PLAN.md); asset refs,
usage, forking and file copying in [storagemanager/PLAN.md](../storagemanager/PLAN.md);
the document side of cloning in [documentmanager/PLAN.md](../documentmanager/PLAN.md);
what the UI must show in [apps/web/PLAN.md](../../apps/web/PLAN.md).

## Decisions (agreed 2026-10-11)

| Decision       | Choice                                                                                                                        |
| -------------- | ----------------------------------------------------------------------------------------------------------------------------- |
| Project owner  | a user, an org, or gyoza (the ledger's `Owner`; never another project)                                                        |
| Gyoza projects | templates and demonstrators: read-only for every signed-in user, and clonable into a new project the cloner owns (read-write) |
| Org roles      | admin and member; an org always keeps at least one admin                                                                      |
| Project access | every member of the owning org can edit; any project can also be shared with individual users as viewer or editor             |
| Joining an org | admin invites an email; when a Google account with that (verified) email signs in, it can accept. No email sending in the MVP |
| Orgs per user  | at most one (unique membership per user); org-less users are fine                                                             |
| Cloning        | any project the actor can read (gyoza templates included); no "allow cloning" flag, since readers can download anyway        |
| Billing        | deferred                                                                                                                      |

Defaults (mine, to confirm while building):

- users keyed by the SuperTokens user ID (text); orgs, projects, invites by UUIDv7
- sharing only with users who already signed up (looked up by email)
- managing a project (rename, share, delete): the owning user, or an admin of the owning org
- leaving an org moves nothing: org files stay the org's, personal files stay personal.
  Account deletion is what moves a user's library and projects to their org (or deletes them)
- the last member leaving deletes the org (its projects and files, via storagemanager);
  the last admin can't leave while other members remain (promote someone first)
- deletions mark the row first (access stops at once), cascade, then remove the row;
  a cleanup loop finishes interrupted deletions (same resumable pattern as storagemanager)
- user rows are hard-deleted at the end (GDPR); invites and shares go with them
- gyoza projects are created, filled and deleted only through service-to-service calls
  (a seeding tool or admin tooling later), never through the gateway; they can't be
  shared (everyone can already see them) and nobody can edit them in place

## Cloning (usermanager's part)

- `CloneProject(actor, sourceID, target owner)`: the actor must be able to read the
  source and own the target (their library, or their org: any member may). The clone
  gets the source's name, a fresh ID and `cloned_from`; shares aren't copied.
- A clone is a multi-step job, so the project starts as `cloning`: invisible to
  listings and granting no access until it's `ready`. Steps: create the row ->
  storagemanager `CopyProjectFiles(source, clone)` -> (later) documentmanager clones the
  document and rewrites its refs -> mark `ready`. Each step is idempotent; the cleanup
  loop resumes stalled clones or abandons (deletes) them.
- What gets copied, and why the clone is independent of the source in both directions,
  is storagemanager's side: see its PLAN.md.

## Access rules (the heart of it)

`access(actor, project)`: owning user -> edit; member of owning org -> edit;
share -> viewer/editor; gyoza project -> viewer for every signed-in user; otherwise
none. For storagemanager's `Authorizer`:

- owner user: write if actor is that user
- owner org: write if actor is a member
- owner project: the project access (editor -> write, viewer -> read)
- owner gyoza: everyone reads, nobody writes (admin tooling later)
- assets used by a project: reading the project grants read on them, checked "via the
  project" (storagemanager's `ResolveRef`); usermanager answers the project part
- `Asset.ProjectID` association: project access grants read only, until storagemanager
  retires it in favour of usage rows
- deleted users/orgs/projects, and projects still `cloning`, grant nothing

## Architecture

Like storagemanager: `Store` (data, own schema `usermanager`, MemoryStore + Postgres,
shared suite) and `Manager` (rules, permission checks, cascades) behind a `Service`.

- `Manager` depends on `Store`, `storagemanager.Service` (cascades, cloning) and an
  `Identity` interface declared here (`DeleteUser(ctx, userID)`), implemented by the
  gateway's SuperTokens wrapper, so usermanager stays SuperTokens-free.
- The storage `Authorizer` needs only the `Store` (reads), not the Manager. That breaks
  the construction cycle (storagemanager needs the authorizer; usermanager's Manager needs
  storagemanager): build Store -> Authorizer -> storagemanager -> usermanager Manager.
- Import direction: usermanager imports storagemanager (interface, errors), never the
  reverse.

## Data model (schema `usermanager`)

- `users`: id (SuperTokens ID), email (lowercased), email_verified, name, provider_avatar_url,
  created_at, updated_at, deleting_at
- `orgs`: id, name, created_at, updated_at, deleting_at
- `memberships`: org_id, user_id (unique: one org per user), role (admin|member), joined_at
- `invites`: id, org_id, email (lowercased), role, invited_by, created_at, expires_at;
  unique pending invite per (org, email)
- `projects`: id, owner_kind (user|org|gyoza), owner_id ('' for gyoza, as in the
  ledger), name, state (cloning|ready), cloned_from (project id, optional), created_by,
  created_at, updated_at, deleting_at
- `project_shares`: project_id, user_id, access (viewer|editor), PK (project_id, user_id)

## Steps

1. **Users, orgs, membership, invites in the Store**: types, errors, rules file,
   `Store` interface, `MemoryStore`, shared suite, mutation check.
2. **Projects and shares in the Store**: same, plus listing "my projects" (owned,
   org's, shared with me) and gyoza templates, keyset-paged by UUIDv7.
3. **Access rules + storage Authorizer**: pure access functions over Store data;
   `StorageAuthorizer` implementing `storagemanager.Authorizer`; table tests covering
   every rule above (including deleted and `cloning` rows), mutation check.
4. **Manager: users and orgs**: `SignIn(identity)` (upsert profile, return pending
   invites), `CreateOrg`, invites (create/list/revoke/accept/decline), roles,
   remove member, leave; admin and last-admin rules.
5. **Manager: projects**: create (as user or for my org), get, list mine, list gyoza
   templates, rename, share/unshare/list shares, transfer my project to my org;
   service-level create/delete for gyoza projects.
6. **Cascades**: delete project (storagemanager `DeleteOwned(project)`), delete org,
   delete account (`RemoveUser` with the org, move projects or delete them, drop shares
   and invites, `Identity.DeleteUser`, delete the row); deletion markers + resumable
   cleanup loop; tests with storagemanager's memory fakes.
7. **Cloning**: `CloneProject` and the `cloning` state as above. Needs storagemanager's
   phase 2 steps 1-5 (asset refs, usage, `CopyProjectFiles`) first. Tests: clone visible
   only when ready; files readable/writable by the new owner; source untouched; a clone
   interrupted after each step resumes or is abandoned.
8. **Postgres store**: migrations, sqlc, shared suite on Postgres, EXPLAIN the access
   queries (they run on every storagemanager request).
9. **gRPC + binaries**: `UserManagerService` (user-facing, `actor_id` as in
   storagemanager) and `AuthzService` (CanRead/CanWrite for storagemanager);
   `AuthzClient` implementing `storagemanager.Authorizer` over gRPC; `cmd/user-manager`;
   monolith wiring in the order above (replacing `AllowAll`); `storage-manager` gets
   `GYOZA_USERMANAGER_ADDR`; docs (README, OPTIMIZATIONS: per-request authz calls and
   caching).

Not in this plan: billing, email sending for invites, a seeding/admin tool that creates
and fills gyoza projects.

## Verification

Per step: shared suite on MemoryStore (and Postgres from step 8), `go vet`, mutation
check of each new suite, `go test -race`. Step 9: storagemanager's tests still pass with
the real authorizer wired in a new integration test (member/non-member/guest/viewer
against real uploads), plus a `buf curl` smoke run of the monolith.
