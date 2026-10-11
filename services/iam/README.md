# IAM

Identity and access management. Formerly "user manager"; renamed because it owns more
than users.

- users (profiles; sign-in itself is the gateway's, through SuperTokens), orgs,
  membership and roles, invites, projects and project sharing; billing tables later
- answers other services' "may this user...?" questions: implements storagemanager's
  `Authorizer` (may this user read/write files for this owner or project?). Other
  services only see that interface, never these tables. In the monolith it's passed
  in-process; distributed, over gRPC
- plan and decisions: [PLAN.md](PLAN.md)

## Built so far (steps 1-2)

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

Invariants the Store enforces itself: emails unique (case-insensitive); a user belongs
to at most one org; an org with members always has an admin (the very last member may
leave, after which the org is deleted). Projects are owned by a user, an org or gyoza
(templates, never shared); one share per user and project; only org projects can be
org-locked, and a locked project takes no new or changed shares for outsiders (existing
ones are kept but suspended). "My projects" lists what a user owns, what their org owns
and what's actively shared with them, each once, never clones in progress.
