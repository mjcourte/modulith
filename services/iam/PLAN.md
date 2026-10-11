# Plan: iam (`services/iam`)

Identity and access management: users, orgs, projects, sharing and access decisions.
Formerly usermanager; renamed 2026-10-11 after step 1 (`services/iam`, package `iam`,
`cmd/iam`, schema `iam`).

Status: **steps 1-5 done** (users, orgs, membership, invites, projects, shares, org-lock:
types, rules, MemoryStore, 29-case store suite; access rules and `StorageAuthorizer`;
Manager for users, orgs, invites, projects and sharing. Mutation-checked: step 1 rules 21/21 and
MemoryStore 25/25, step 2 rules 21/21 and MemoryStore + lock rules 29/29, step 3 access
+ authorizer 21/21, step 4 Manager 26/27 (the survivor, `member()` re-checking the
actor, becomes observable in step 6 with users being deleted), step 5 projects 24/24, 6a deletion store 34/34).
Step 6 in progress: 6a (Store markers, bulk steps, hard deletes, guards) done; next 6b
(access rules respect the markers).

## Context

storagemanager authorizes with `AllowAll` until something can answer "may this user
read/write these files?". iam owns users, orgs, membership, projects and
sharing, implements storagemanager's `Authorizer`, and drives the cascades already
built (`DeleteOwned`, `RemoveUser`). Identity comes from SuperTokens (Google sign-in)
via the gateway; iam never talks to SuperTokens directly.

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
- managing a project (rename, transfer, delete): the owning user, or an admin of the owning org
- sharing (decided 2026-10-11): anyone with editor access (owner, org member, editor
  share) can share as viewer or editor, and change or remove others' shares; viewers
  can't share; anyone can remove their own share (leave the project)
- org-lock (decided 2026-10-11): an org-owned project can be locked (user-owned
  projects can't be). Whoever creates an org project chooses, and `CreateProject` takes
  it explicitly; the UI defaults new org projects to locked. Changing it afterwards is
  for that org's admins only. While locked:
  - no new shares with people outside the org (inside, shares change nothing: members
    are already editors)
  - existing outside shares are suspended, not deleted: they grant nothing while
    locked, and unlocking restores them. A member who leaves the org loses access the
    same way
  - clones of the project can only go to its org (as org projects), closing the
    one-click way around the lock; downloads stay possible
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

## Cloning (iam's part)

- `CloneProject(actor, sourceID, target owner)`: the actor must be able to read the
  source and own the target (their library, or their org: any member may). A locked
  source can only be cloned into its own org. The clone
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
none. A user can reach a project through several of these at once (an org member
who also has a share); their access is the greatest of them, except that on an
org-locked project shares with people outside the org count for nothing; so sharing with someone
who already has access is allowed and harmless: it can raise access, never lower it
(decided 2026-10-11). Sharing again with the same level changes nothing (idempotent). For storagemanager's `Authorizer`:

- owner user: write if actor is that user
- owner org: write if actor is a member
- owner project: the project access (editor -> write, viewer -> read)
- owner gyoza: everyone reads, nobody writes (admin tooling later)
- assets used by a project: reading the project grants read on them, checked "via the
  project" (storagemanager's `ResolveRef`); iam answers the project part
- `Asset.ProjectID` association: project access grants read only, until storagemanager
  retires it in favour of usage rows
- deleted users/orgs/projects, and projects still `cloning`, grant nothing

## Architecture

Like storagemanager: `Store` (data, own schema `iam`, MemoryStore + Postgres,
shared suite) and `Manager` (rules, permission checks, cascades) behind a `Service`.

- `Manager` depends on `Store`, `storagemanager.Service` (cascades, cloning) and an
  `Identity` interface declared here (`DeleteUser(ctx, userID)`), implemented by the
  gateway's SuperTokens wrapper, so iam stays SuperTokens-free.
- The storage `Authorizer` needs only the `Store` (reads), not the Manager. That breaks
  the construction cycle (storagemanager needs the authorizer; iam's Manager needs
  storagemanager): build Store -> Authorizer -> storagemanager -> iam Manager.
- Import direction: iam imports storagemanager (interface, errors), never the
  reverse.

## Data model (schema `iam`)

- `users`: id (SuperTokens ID), email (lowercased), email_verified, name, provider_avatar_url,
  created_at, updated_at, deleting_at
- `orgs`: id, name, created_at, updated_at, deleting_at
- `memberships`: org_id, user_id (unique: one org per user), role (admin|member), joined_at
- `invites`: id, org_id, email (lowercased), role, invited_by, created_at, expires_at;
  unique pending invite per (org, email)
- `projects`: id, owner_kind (user|org|gyoza), owner_id ('' for gyoza, as in the
  ledger), name, state (cloning|ready), cloned_from (project id, optional), org_locked
  (bool, org-owned only), created_by,
  created_at, updated_at, deleting_at
- `project_shares`: project_id, user_id, access (viewer|editor), PK (project_id, user_id)

## Steps

1. **Users, orgs, membership, invites in the Store**: types, errors, rules file,
   `Store` interface, `MemoryStore`, shared suite, mutation check.
2. **Projects and shares in the Store**: same, plus listing "my projects" (owned,
   org's, shared with me) and gyoza templates, keyset-paged by UUIDv7. Includes
   `Project.OrgLocked` and `SetOrgLocked` (refused on projects not owned by an org).
   Done.
3. **Access rules + storage Authorizer**: pure access functions over Store data;
   greatest of all routes; outside shares ignored on locked projects;
   `StorageAuthorizer` implementing `storagemanager.Authorizer`; table tests covering
   every rule above (including deleted and `cloning` rows), mutation check.
4. **Manager: users and orgs**: `SignIn(identity)` (upsert profile, return pending
   invites), `CreateOrg`, invites (create/list/revoke/accept/decline), roles,
   remove member, leave; admin and last-admin rules.
5. **Manager: projects**: create (as user or for my org), get, list mine, list gyoza
   templates, rename, share/unshare/list shares, the sharing rules above (editors share,
   viewers don't, self-removal), org-lock toggled by org admins (outside shares refused
   while locked), transfer my project to my org;
   service-level create/delete for gyoza projects.
6. **Cascades** (design agreed 2026-10-11):
   - **Deletion markers first**: users, orgs and projects get `DeletingAt`; setting it
     is the first step of every deletion and cuts access at once: `actor()` refuses a
     user being deleted, `accessTo`/`projectAccess` grant nothing on a project being
     deleted and treat an org being deleted as no org, listings hide them.
   - **Delete a project** (owner or org admin; templates via service-only
     `DeleteTemplate`); final, no project trash (decided): mark deleting ->
     storagemanager `DeleteOwned(project)` (files and document snapshots to the storage
     trash, purged after 7 days) -> delete its shares and the row.
   - **Delete an org**: an admin may delete it even with other members (decided, with
     confirmation in the UI), or it goes when its last member leaves. Mark the org and
     all its projects deleting (one transaction) -> project cascade for each ->
     `DeleteOwned(org)` -> delete invites and memberships -> delete the row. Members keep
     their personal files and projects.
   - **Delete my account** (GDPR): refused with `ErrLastAdmin` while they're their org's
     only admin and others remain (decided: they promote someone first, as when
     leaving). Mark the user deleting, recording the destination org (or none) so a
     resumed deletion can't mistake "membership already removed" for "had no org". With
     an org: move personal projects to it (unlocked), `RemoveUser(user, org)` (library
     moves, creator cleared), then remove the membership (last member: delete the org
     and continue as org-less). Without: project cascade for each personal project,
     `RemoveUser(user, "")`. Then forget them in iam (delete shares to them and invites to
     their email; clear `CreatedBy`, `SharedBy`, `InvitedBy`), `IdentityProvider.DeleteUser`
     (SuperTokens, via the gateway), delete the row (frees the email).
   - **Resumable loop**: `Cleanup`/`RunCleanup` finishes everything marked deleting
     (each step idempotent) and removes expired invites.
   - **Dependencies**, declared in iam (consumer-side interfaces): `Storage`
     (`DeleteOwned`, `RemoveUser`; `storagemanager.Service` satisfies it) and
     `IdentityProvider` (`DeleteUser`). `NewManager(store, storage, idp, cfg)`.
   - **Store**: mark deleting (user with destination org; org with its projects;
     project), list what's deleting, transfer a user's projects to an org, hard deletes
     (refused unless marked), forget a user; shared suite + mutation check. Tests run the
     cascades against storagemanager's memory fakes.
7. **Cloning**: `CloneProject` and the `cloning` state as above. Needs storagemanager's
   phase 2 steps 1-5 (asset refs, usage, `CopyProjectFiles`) first. Tests: clone visible
   only when ready; files readable/writable by the new owner; source untouched; a clone
   interrupted after each step resumes or is abandoned.
8. **Postgres store**: migrations, sqlc, shared suite on Postgres, EXPLAIN the access
   queries (they run on every storagemanager request).
9. **gRPC + binaries**: `IAMService` (user-facing, `actor_id` as in
   storagemanager) and `AuthzService` (CanRead/CanWrite for storagemanager);
   `AuthzClient` implementing `storagemanager.Authorizer` over gRPC; `cmd/iam`;
   monolith wiring in the order above (replacing `AllowAll`); `cmd/storage` gets
   `GYOZA_IAM_ADDR`; docs (README, OPTIMIZATIONS: per-request authz calls and
   caching).

Not in this plan: billing, email sending for invites, a seeding/admin tool that creates
and fills gyoza projects.

## Verification

Per step: shared suite on MemoryStore (and Postgres from step 8), `go vet`, mutation
check of each new suite, `go test -race`. Step 9: storagemanager's tests still pass with
the real authorizer wired in a new integration test (member/non-member/guest/viewer
against real uploads), plus a `buf curl` smoke run of the monolith.
