# IAM

Identity and access management. Formerly "user manager"; renamed because it owns more
than users.

- users (profiles; sign-in itself is the gateway's, through SuperTokens), orgs,
  membership and roles, invites, projects and project sharing; billing tables later
- answers other services' "may this user...?" questions: implements storage's
  `Authorizer` (may this user read/write files for this owner or project?). Other
  services only see that interface, never these tables. In the monolith it's passed
  in-process; distributed, over gRPC
- plan and decisions: [PLAN.md](PLAN.md)

## Layout

| Path | What |
| --- | --- |
| `iam.go` | the public surface: aliases (`Service`, `Store`, the types and errors), `NewMemoryStore`, `NewManager`, `NewStorageAuthorizer` |
| `internal/directory/` | the data: `Store` and its parts, rules, `MemoryStore`, and the access rules (`access.go`); checks data shape only |
| `internal/manager/` | the logic: `Manager`, `Service`, `StorageAuthorizer`; depends on `directory`, never the other way |

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
  project they're associated with. Refusals wrap `storage.ErrForbidden`. Not
  wired into the binaries yet (step 9); tested end to end with storage's
  memory fakes
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
