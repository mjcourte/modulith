# IAM

Identity and access management. Formerly "user manager"; renamed because it owns more
than users.

- users (profiles; sign-in itself is the gateway's, through SuperTokens), orgs,
  membership and roles, invites, projects and project sharing; billing tables later
- answers other services' "may this user...?" questions: implements storage's
  `Authorizer` (may this user read/write files for this owner or project?). Other
  services only see that interface, never these tables. In the monolith it's passed
  in-process; distributed, over gRPC
- decisions below; what's next: [PLAN.md](PLAN.md)

## Decisions (agreed 2026-10-11)

| Decision       | Choice                                                                                                                         |
| -------------- | ------------------------------------------------------------------------------------------------------------------------------ |
| Project owner  | a user, an org, or gyoza (the ledger's `Owner`; never another project)                                                         |
| Gyoza projects | templates: read-only for every signed-in user, and clonable into a project the cloner owns; made only by service-to-service calls |
| Org roles      | admin and member; an org always keeps at least one admin                                                                       |
| Project access | every member of the owning org edits; any project can also be shared with individual users as viewer or editor                 |
| Joining an org | an admin invites an email; a Google account with that verified email can accept on sign-in. No email sending in the MVP        |
| Orgs per user  | at most one; org-less users are fine                                                                                           |
| Cloning        | any project the actor can read, templates included; no "allow cloning" flag, since readers can download anyway                 |
| Billing        | deferred                                                                                                                       |

- identity comes from SuperTokens (Google sign-in) through the gateway; iam never talks
  to SuperTokens. Users are keyed by the SuperTokens ID; orgs, projects and invites by UUIDv7
- sharing only with users who already signed up (looked up by email). Anyone with editor
  access shares (as viewer or editor) and changes or removes others' shares; viewers
  don't; anyone can remove their own share. Sharing can raise access, never lower it
- managing a project (rename, move, delete): the owning user, or an admin of the owning org
- org-lock: only org projects; chosen at creation (the UI defaults to locked), changed by
  that org's admins. While locked, no new outside shares, existing ones are suspended
  (restored on unlock), and clones only go to the org, locked too. Downloads stay possible
- leaving an org moves nothing; account deletion moves the user's library and projects
  to their org (or deletes them). The last member leaving deletes the org; the last admin
  can't leave while others remain
- deletions mark the row first (access stops at once), cascade, then remove it; user rows
  are hard-deleted at the end (GDPR)
- architecture: `Store` and `Manager` as in storage; the `StorageAuthorizer` needs only the
  Store, which breaks the construction cycle (store -> authorizer -> storage -> Manager).
  iam imports storage, never the reverse

## Layout

| Path | What |
| --- | --- |
| `iam.go` | the public surface: aliases (`Service`, `Store`, the types and errors), `NewMemoryStore`, `NewPostgres`, `Migrate`, `NewManager`, `NewStorageAuthorizer`, `RegisterGRPC`, `NewGRPCClient`, `NewAuthzClient` |
| `wiring.go` | what the binaries share: `SettingsFromEnv`, `OpenStore`, `StartCleanup` |
| `internal/directory/` | the data: `Store` and its parts, rules, `MemoryStore`, `Postgres` (`postgres*.go`), and the access rules (`access.go`); checks data shape only |
| `internal/manager/` | the logic: `Manager`, `Service`, `StorageAuthorizer`, the deletion cascades and cleanup loop; depends on `directory`, never the other way |
| `internal/grpcapi/` | `IAMService` and `AuthzService` (`contracts/proto/gyoza/iam/v1`): servers, and clients implementing `Service` and storage's `Authorizer` |
| `internal/db/` | schema `iam`: goose migrations, one query file per area, sqlc output |

The sections below name files in `internal/directory` (Store, Access, Deletion store) and `internal/manager` (Manager, `storage_authz.go`).

## Built so far (steps 1-3)

- `Store` (`store.go`) embeds `UserStore` (`users.go`), `OrgStore` (`orgs.go`),
  `InviteStore` (`invites.go`) and `ProjectStore` (`projects.go`: projects, shares,
  org-lock); errors and limits sit at the top of each
- rules every Store implementation shares: `rules.go` (IDs, timestamps, names),
  `users_rules.go`, `orgs_rules.go` (membership decisions as pure functions over an
  org's member list, so every implementation decides the same way), `invites_rules.go`,
  `projects_rules.go` (owners, states, transfers, shares, `shareActive`: whether a share
  counts on an org-locked project, paging)
- `MemoryStore` (`memory.go`, `*_memory.go`): one mutex per call, so the invariants (one
  org per user, an admin in every org with members) hold under concurrent calls
- the shared suite `runStoreTests` (`store_test.go`, `store_*_test.go`, 29 cases) and the
  internal rules tests (`rules_test.go`, `projects_rules_test.go`)

## Access (step 3)

- `access.go`: `projectAccess` decides a user's access to a project as a pure function: the
  greatest of owner (editor), member of the owning org (editor), gyoza template (viewer
  for everyone) and their share, with shares suspended on org-locked projects for
  people outside the org; clones in progress grant nothing. `AccessTo` gathers the
  facts from a Store; unknown users get nothing; Store failures are errors, never
  "no access"
- `storage_authz.go`: `StorageAuthorizer` implements storage's `Authorizer` from
  a Store alone (so the binaries can build it before storage). Files are
  reachable through their owner (own library, org library for members, a project's
  files by project access, gyoza's files read-only for all) or, read-only, through a
  project they're associated with. `ProjectOwner` tells storage who owns a project
  (unknown or deleting: refused), for storage's rule that a project uses in place only
  its own, its owner's and gyoza's files. Refusals wrap `storage.ErrForbidden`. Tested end
  to end with storage's memory fakes; the binaries use it in-process (monolith) or
  over gRPC (`AuthzService`)
- performance notes: [OPTIMIZATIONS.md](OPTIMIZATIONS.md)

## Manager (steps 4-5)

- `manager.go`: `Manager`, `Config` (`InviteTTL`, default 14 days), `ErrForbidden`, and
  the permission helpers every method uses: `actor` (a known user), `member` (of this
  org) and `admin`. Refusals are `ErrForbidden` whether or not the org exists, so
  outsiders learn nothing
- `manager_users.go`: `SignIn` (the gateway's sign-in hook: upsert the profile, return
  pending invites, only for a verified email), `GetMe` (profile + org)
- `manager_orgs.go`: create (creator becomes admin), get and list members (members),
  rename, set role, remove member (admins), leave
- `manager_invites.go`: invite, list, revoke (admins); list mine, accept, decline (the
  invitee: same email, verified; someone else's invite looks like a missing one)
- `manager_projects.go`: create (for yourself, or for your org by any member, choosing
  the org-lock), get (with your access level) and list mine / templates; rename and move
  to my org (the owner, or admins of the owning org; templates never); share, change
  and remove shares (anyone with editor access), leave a project shared with you;
  org-lock (admins of the owning org). `CreateTemplate` is service to service only
- `service.go`: `Service`, what the gateway calls; `*Manager` implements it

## Deletion store (step 6a)

- `deletions.go`: `DeletionStore` (embedded in `Store`): mark a user (with the org their
  things move to, fixed at the first mark), an org (with all its projects, in one
  step) or a project as deleting; list what's deleting (for resuming); list an owner's
  projects; move a user's projects to an org; forget a user (shares and invites to them
  deleted; creator, sharer and inviter cleared); hard deletes that refuse unmarked rows
  (`ErrNotDeleting`) and unfinished cascades (an org or user still owning projects, a
  user still in an org); delete expired invites
- a row being deleted takes no new references: no sign-in, org, project, share, invite
  or membership for it, and listings hide projects being deleted

Invariants the Store enforces itself: emails unique (case-insensitive); a user belongs
to at most one org; an org with members always has an admin (the very last member may
leave, after which the org is deleted). Projects are owned by a user, an org or gyoza
(templates, never shared); one share per user and project; only org projects can be
org-locked, and a locked project takes no new or changed shares for outsiders (existing
ones are kept but suspended). "My projects" lists what a user owns, what their org owns
and what's actively shared with them, each once, never clones in progress.

## Deletions (step 6)

- `manager_deletions.go`: `DeleteProject` (owner or org admin; final, its files go to
  storage's trash), `DeleteTemplate` (service to service), `DeleteOrg` (admins, even with
  other members; they keep their own things), `DeleteAccount` (the user; refused with
  `ErrLastAdmin` while they're their org's only admin and others remain). The last member
  leaving an org (`LeaveOrg`, `RemoveMember`) deletes it
- every deletion marks the row first (access stops at once), then works through steps
  that are each safe to repeat, and removes the row last. An account with an org: their
  projects and library move to it (`TransferUserProjects`, storage `RemoveUser`), then they
  leave it. Without: their projects and library are deleted. Then iam forgets them, the
  `IdentityProvider` deletes their sign-in account, and the row goes (freeing the email)
- dependencies are consumer-side interfaces: `Storage` (`DeleteOwned`, `RemoveUser`;
  `storage.Service` satisfies it) and `IdentityProvider` (`NoIdentityProvider` until the
  gateway exists)
- `manager_cleanup.go`: `Cleanup`/`RunCleanup` finish deletions cut off part-way (e.g. by a
  storage outage) and remove expired invites; off `Service`, run by whichever binary
  hosts the Manager. If an org's other admins step down while an admin's deletion is
  pending, its longest-standing member is promoted so the deletion can't get stuck
- tests run the cascades against storage's memory fakes with iam's own authorizer, so
  they check the files too (`manager_deletions_test.go`)

## Postgres (step 8)

- `postgres*.go`: the production Store, passing the same suite as `MemoryStore`
  (`postgres_test.go`, needs `GYOZA_TEST_DATABASE_URL`). One transaction per call
- invariants without the memory store's mutex: memberships' primary key keeps a user in
  one org; membership changes lock the org row, then apply the same pure rules
  (`orgs_rules.go`); adding a reference to a user, org or project takes a shared lock on
  its row and checks it isn't being deleted, which marking it (an `UPDATE`) waits for
- foreign keys back the cascade order: memberships, invites and shares must go before
  their org, project or user; `DeleteUser` also removes shares to the user
- "my projects" is a `UNION ALL` of three index-backed branches (own, org's, shared),
  not an `OR`: see [OPTIMIZATIONS.md](OPTIMIZATIONS.md)

## gRPC and binaries (step 9)

- `IAMService` mirrors `Service` (calls carry `actor_id`, as in storage); `AuthzService`
  is storage's `Authorizer` (`CanRead`, `CanWrite`, `ProjectOwner`). Errors carry `google.rpc.ErrorInfo`
  (domain `iam.gyoza`), rebuilt by the clients from one table (`wireErrors`); an
  `AuthzService` refusal comes back as `storage.ErrForbidden`, and an unreachable iam as
  an error that is never mistaken for a refusal
- the monolith builds iam's store, the `StorageAuthorizer` on it, storage, then iam's
  `Manager` (which calls storage), and serves all of them. `cmd/iam` (`localhost:9093`)
  calls storage at `GYOZA_STORAGE_ADDR`; `cmd/storage` asks iam at `GYOZA_IAM_ADDR`
- settings: `GYOZA_INVITE_TTL` (default 14 days), `GYOZA_IAM_CLEANUP_INTERVAL` (10m)

## Cloning (step 7)

- `CloneProject(actor, source, target)`: the actor reads the source (templates
  included) and may create projects for the target (their library, or their org). An
  org-locked source only clones into its own org, and the clone is locked too. The clone
  gets the source's name and `ClonedFrom`; shares aren't copied. Also `IAMService.CloneProject`
- a clone is a job: the row is created `cloning` (hidden, granting nothing), storage
  copies the files (`CopyProjectFiles`, resumable), then it's marked `ready`. If copying
  fails, the call returns the half-made clone with the error (over gRPC, just the error)
- the cleanup loop picks up clones still `cloning` 5 minutes after they started
  (`ListCloningProjects`, partial index from migration 00002): it finishes them, or
  deletes them if their source is gone or they've failed for 24 hours. A clone abandoned
  during an outage is marked for deletion and finished as a project deletion
- not yet: documentmanager copying the document and rewriting its refs (it slots in
  between the copy and `ready`)
- tests: `manager_clone_test.go` (cloning, every refusal, resumed and abandoned clones);
  mutation-checked 11/12. The survivor is the "source being deleted" check, which
  `Cleanup` can't reach (deletions finish before clones are looked at); it guards
  against two replicas racing
