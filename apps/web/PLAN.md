# Plan notes: web app (`apps/web`)

Step 1 (the sign-in shell) is done: see [README.md](README.md). The sections below are
UI details the backend plans rely on (2026-10-11), to build one at a time. Related:
[gateway/PLAN.md](../../services/gateway/PLAN.md),
[iam/PLAN.md](../../services/iam/PLAN.md).

## Open from step 1

- **Gateway dev pages** (`/dev/signin`) were built for the public URL on :8080; untested
  with it on :3000 (the proxy doesn't forward `/dev`).
- **Bundle size:** Vite warns of a chunk over 500 kB (SuperTokens' prebuilt UI). Check it
  stays out of the main chunk (`autoCodeSplitting`) before shipping.
- **Sign-in look:** the prebuilt SuperTokens UI isn't themed to shadcn; theme it or
  replace it with our own Google button (`redirectToThirdPartyLogin`).
- **Navigation:** SuperTokens redirects with full page loads; wire it to the router
  (`windowHandler`/`getRedirectionURL`) if that ever matters.
- **Deliberately not yet:** tests (Vitest once there is logic worth testing), API type
  generation (hand-written wire types until they hurt), a formatter check in CI.

## Part 2: profile, orgs, library, projects (planned 2026-10-14)

Everything up to the canvas (part 3). The REST API already covers most of it; the
backend additions (project shelf, read-only shares, copies) are in
[storage/PLAN.md](../../services/storage/PLAN.md). Profile stays read-only (name and
avatar come from Google at each sign-in; no RPC to edit them).

**Vision (2026-10-14):** the finished app has a canvas *and* a DAM file browser next to
it. So the library is one reusable `<FileBrowser>` component: `/library` hosts it
full-screen now, the project page (later the canvas) as a side panel.

- Commands (toolbar, context menu, multi-select where sensible): open/download, upload,
  replace (new version: `POST /uploads` with `assetId`), rename, move, delete, share,
  clone, link to project, versions/revert.
- Ownership badges: self, org, project, Gyoza, shared with me, other (read through a
  project). Computed from the item's `owner` against `useMe()`, plus the `shared` flag
  storage adds.
- Clone anything readable (own, org, project, shared) to an owner the user can write:
  `CopyAsset`, and `CopyFolder` for folders.
- Links: explicit shelf links, and link-on-use (dropping an asset on the canvas adds a
  document ref; documents records that use). The panel shows both; only shelf links can
  be unlinked there.
- Shared items are read-only: edit actions explain "make your own copy to edit". The
  copy is fully the user's, free to share back or not.

### Step 2.0: groundwork

- Fix `src/api/client.ts`: the header is `'ontent-Type'`, so every mutating request
  fails the gateway's CSRF check.
- App shell: a pathless layout route `src/routes/_app.tsx` holding the session guard
  (moved from `index.tsx`) and a sidebar: workspace switcher, Projects, Library, Shared
  with me, Trash, Org, and a user menu (profile, sign out).
- `useWorkspace()`: the active owner from `useMe()` ("Workspace switcher" below), the
  last choice in localStorage (wrapped in try/catch).
- API layer: one file per area (`orgs.ts`, `invites.ts`, `projects.ts`, `assets.ts`,
  `folders.ts`, `uploads.ts`, `shares.ts`) following `me.ts`. Mutations invalidate by
  query key; paged lists use `useInfiniteQuery` on `pageToken`/`nextPageToken`. One
  `<ErrorMessage>` maps `ApiError.code` to text.
- shadcn components: sidebar, dialog, alert-dialog, dropdown-menu, input, label, select,
  switch, table, sonner.

### Step 2.1: profile and orgs

- `/profile`: name, email and avatar, read-only. Leave org (`DELETE /me/org`; explain
  `LAST_ADMIN`). Delete account (`DELETE /me`, confirmed by typing the email).
- Pending invites (`GET /me/invites`): a dialog after sign-in to accept or decline.
- `/org` is always in the sidebar (decided 2026-10-14). Without an org, the page offers
  to create one or join one: their pending invites (`GET /me/invites`) with accept and
  decline. With one, admins can rename and delete it, change
  roles, remove members, and invite by email, list or revoke invites ("no email is
  sent"). Controls follow the role; a 403 shows as a toast.

### Step 2.2: projects (detailed 2026-10-14; built before the library)

API facts: `GET /projects` returns everything the user can reach (their own, their
org's, shared with them; oldest first, paged), each with its `owner`, so the page
filters on the client. `GET /templates` lists templates.

1. **List:** `src/api/projects.ts` (types, `useInfiniteQuery`, "Load more"). Tabs:
   **Workspace** (owner = `useWorkspace().owner`; switching filters without a refetch),
   **Shared with me** (owner is neither the user nor their org), **Templates**
   (read-only, "Use template" clones into the workspace). Cards: a preview placeholder
   (until the engine renders one), name, updated date, owner badge, a lock if
   `orgLocked`. A click opens `/projects/$id`.
2. **Create and basic actions:** a "New project" dialog (name; owner = the workspace;
   for an org, an org-lock switch, on by default). A "⋯" menu on each card: rename, delete
   (confirm).
3. **More actions:** clone (target owner; a locked project offers only its org), move to
   org (personal projects, with an org), org lock (org admins), and the share dialog
   ("Sharing" below; pending shares revocable with
   `DELETE /projects/{id}/pending-shares/{email}`).
- `/projects/$id`: a placeholder for opening a project. It shows the header and the
  document's metadata (headers of `GET /projects/{id}/document`), and later the shelf in
  a `<FileBrowser>` panel (unlink, "update available"). No canvas or WebSocket yet.

### Step 2.3: library (detailed 2026-10-14)

Browsing and files need nothing new from the backend; share, copy and link wait for
[storage/PLAN.md](../../services/storage/PLAN.md). `GET /folders` returns one folder's
whole view: `path` (breadcrumbs), `folders`, and `assets` (paged).

1. **Browse:** `<FileBrowser>` scoped to the workspace's owner, folder in the URL
   (`/library?folder=<id>`, so back and links work). A top bar with breadcrumbs, New
   folder, Upload, and a grid/list toggle. Folders first, then assets, as cards with a
   **category icon** (image, vector, video, audio, font, pdf, json, text, other). There
   are no thumbnails: a real preview needs a presigned download per asset, so previews
   wait for a backend thumbnail (an Open entry for storage's OPTIMIZATIONS.md). Empty
   state: "Drop files here or click Upload".
2. **Files:** upload by a whole-body drop zone or the button: `POST /uploads` →
   `XMLHttpRequest` PUT to the presigned URL (for progress) → `/complete`, with a queue
   panel; handle 410 expired. Download via `/assets/{id}/download` and `<a download>`.
   An asset drawer (double-click or "Open"): name, description, tags, type and size,
   versions with revert, replace (a new version), download.
3. **Organise:** rename and move (folder picker) for folders and assets, delete
   (non-empty folder: confirm with the 409 counts, then `?tree=true`), `/trash`
   (restore, time left before the purge, usage count).
4. **After storage's steps:** share (by email), copy ("make my own copy"), add to
   project (offer a fork on `FORK_REQUIRED`), ownership badges, `/shared`.

### Verification

`npm run build` and `npm run lint`. Then by hand, with two Google accounts on
`scripts/gyoza.sh` + `scripts/web.sh`: org invite and accept; upload, download, trash,
restore; share a folder and check the other account can download but not rename, and
can copy; link and unlink; create, share, clone and delete a project.

## Step 1: kickstart (planned 2026-10-13)

Goal: a shell that signs in with Google through the gateway and shows "hello <name>"
from `GET /api/me`. Nothing else; later sections come one at a time after it.

**Prerequisite:** Node 22 LTS or newer (not installed as of 2026-10-13).

**Stack:** React + TypeScript + Vite, Tailwind v4 (`@tailwindcss/vite`), shadcn/ui
(components added only as needed), TanStack Query, TanStack Router (file-based, via
its Vite plugin), `supertokens-auth-react` (ThirdParty with Google, Session). npm as
the package manager (ships with Node; revisit pnpm workspaces only if more `apps/*`
need shared packages). Router and npm recommended, to confirm.

**Dev origin and the proxy.** The gateway has no CORS, uses SameSite cookies, requires
`application/json` on changing requests, and the WebSocket is same-origin only. So the
browser must see one origin:

- Vite serves on `http://localhost:3000` and proxies `/api` (with `ws: true` for
  `/api/ws`) and `/auth` to the gateway on `localhost:8080`.
- `GYOZA_PUBLIC_URL=http://localhost:3000` while developing the web app (it sets the
  gateway's SuperTokens `WebsiteDomain`/`APIDomain`). Note it in `.env.example`.
- Port 3000 and `/auth/callback/google` are what the SuperTokens demo Google client is
  believed to allow (gateway/PLAN.md); verify on first sign-in. If the gateway's dev
  pages break under the new public URL, keep them reachable via the proxy too.
- Production: the gateway (or whatever fronts it) serves the built `dist/` on the same
  origin. Not part of this step.

**Layout:**

```
apps/web/
  package.json, package-lock.json, vite.config.ts, tsconfig*.json, index.html
  components.json                 shadcn config
  src/
    main.tsx                      SuperTokens.init, QueryClientProvider, RouterProvider
    auth.ts                       SuperTokens config (apiBasePath/websiteBasePath /auth)
    index.css                     Tailwind
    api/client.ts                 fetch wrapper: same-origin credentials, JSON, parses
                                  {"error": {"code", "message"}} into an ApiError
    api/me.ts                     useMe() over GET /api/me
    routes/__root.tsx             layout
    routes/index.tsx              home: requires a session, shows the user
    routes/auth/...               SuperTokens' prebuilt sign-in UI and Google callback
    components/ui/                shadcn components
```

**Steps:**

1. Scaffold with `npm create vite@latest` (react-ts) into `apps/web`; strip the demo.
2. Add Tailwind v4 and `shadcn init` (path alias `@/` in tsconfig and Vite).
3. Vite config: port 3000 (`strictPort`), the proxy above.
4. TanStack Router (plugin + generated route tree) and TanStack Query.
5. SuperTokens: init, the `/auth` routes, a session guard on the home route,
   sign-out button.
6. `api/client.ts` + `useMe()`; home shows name/email/avatar from `GetMe`.
7. `scripts/web.sh` like `scripts/admin.sh` (finds Node, `npm install` if needed,
   `npm run dev`); `.gitignore` for `node_modules/` and `dist/`.
8. Smoke test: `scripts/gyoza.sh` with `GYOZA_PUBLIC_URL=http://localhost:3000`, then
   `scripts/web.sh`; sign in with Google, see the home page, reload (session cookie
   survives), sign out.
9. Docs: README records the stack, commands and dev origin; CLAUDE.md gets the Node
   commands next to the Go ones.

**Deliberately not now:** tests (add Vitest when there is logic worth testing), API type
generation (hand-written wire types until they hurt), ESLint beyond Vite's default,
the WebSocket, the WASM engine, the workspace switcher.

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
