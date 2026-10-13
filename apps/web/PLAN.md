# Plan notes: web app (`apps/web`)

Not started. UI details the backend plans rely on (2026-10-11). Related:
[gateway/PLAN.md](../../services/gateway/PLAN.md),
[iam/PLAN.md](../../services/iam/PLAN.md),
[storage/PLAN.md](../../services/storage/PLAN.md).

## Sign-in and orgs

- All data calls go to the gateway's REST/JSON API (see gateway/PLAN.md); files go
  straight to and from S3 with presigned URLs.
- Google sign-in via SuperTokens' React SDK (`supertokens-auth-react`, ThirdParty
  recipe), cookie sessions with the gateway.
- After sign-in, show pending org invites (returned by iam `SignIn`) to accept
  or decline. Admins invite by email; no email is sent in the MVP, so the UI says "tell
  them to sign in with this address".

## Workspace switcher

- A selector for the active workspace: "Personal" (the user) or their org (a user has at
  most one, so at most two entries; `GetMe` returns the org). It scopes what the app
  shows (library, folders, trash, projects) and is the default owner for new projects
  and uploads (the create-project widget starts on it).
- It changes the owner the UI passes to the APIs, never who is acting: `actor_id` is
  always the signed-in user, so permissions, `CreatedBy` and sharing stay personal
  (recommended 2026-10-11, to confirm). The backend needs nothing new: every owner-scoped call already
  takes the owner explicitly and checks the user may act for it.
- Remember the last choice per browser.

## Projects

- Create project: name, and an owner choice: "me" or "my org" (the org option only for
  members of one). Choosing the org shows an org-lock switch ("only people in this org"),
  on by default; any member creating the project may turn it off. Changing it later is
  for org admins (see Sharing).
- Gyoza templates listed separately; read-only, with a "clone" action.
- Clone any project you can read: choose the target (your library or your org). A
  locked project offers only its org as the target. A clone
  appears once it's ready (it's hidden while copying).

## Sharing

- Editors (owner, org members, editor shares) see the share dialog: add people as viewer
  or editor, change or remove shares. Viewers don't. Everyone can leave a project
  shared with them.
- Org projects: admins get an "only people in this org" (org-lock) switch. While it's
  on, outside people can't be added and existing outside shares show as suspended
  (they come back if the lock is turned off).

## Assets in documents

- Refs are pinned versions: show "update available" when the asset has a newer version,
  and let the user move the ref.
- Trashed but used: an asset in the trash still renders, marked as trashed, with when it
  will disappear (`DeletedAt` + retention); after the purge show it as missing.
- Trashing an asset that projects use: warn with the usage count first.
- Editing a shared asset (one you can read but not write): explain "this is shared:
  editing makes your own copy", then call `ForkAsset` and edit the copy.

## Live documents

Transport and streams: [platform/nats/README.md](../../platform/nats/README.md).

- The engine runs in the browser as WASM (the same engine as the backend).
- On opening a project, the gateway sends the current snapshot (from documents) and
  then fans out that project's deltas and pointers over the WebSocket. The two can
  overlap; applying is idempotent (CRDT ops with a Lamport clock), so that's harmless.
- The client keeps a live in-memory replica of the open document, applying every delta
  from `document.{projectID}.delta` (through the gateway).
- Snapshot messages (`document.{projectID}.snapshot`) are pointers, not the snapshot:
  version, last delta sequence covered, storage object ID. On receiving one, the client
  must fetch that snapshot and merge it into its existing live replica, not replace the
  replica with it. Replacing would drop local edits that aren't in the snapshot yet.
- After the merge, the client keeps applying deltas with a sequence after the one the
  snapshot covers.

## Files

- Deleting a non-empty folder: offer rm -rf with the counts from `FolderNotEmptyError`.
