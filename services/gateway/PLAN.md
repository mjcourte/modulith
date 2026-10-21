# Plan: gateway (`services/gateway`): auth

Steps 1 (sign-in), 2 (REST API) and 3 (WebSocket) done 2026-10-13, see the README. Auth decided while planning iam
(2026-10-11). Related:
[iam/PLAN.md](../iam/PLAN.md) (`SignIn`, `Identity`),
[apps/web/PLAN.md](../../apps/web/PLAN.md) (the sign-in UI).

## Auth design: three layers, three places

1. **Authentication (who is this?)**: only here. SuperTokens Go SDK
   (`supertokens-golang`) serves `/auth/*` (sign in/out, refresh) with cookie sessions
   and anti-CSRF. `VerifySession` middleware on every API route and on the WebSocket
   upgrade; access tokens are JWTs checked locally against the core's JWKS, so no
   network hop per request. No other service sees cookies or credentials.
2. **Identity propagation (gateway -> services)**: explicit `actorID` in Go `Service`
   methods and `actor_id` on the wire (already in storage). Services trust it,
   so they're reachable only inside the platform (private subnets, security groups:
   only the gateway). Service-to-service calls (`DeleteOwned`, `RemoveUser`,
   `CopyProjectFiles`, modulith project management) are never routed by the gateway.
3. **Authorization (may they?)**: in each service, through an interface it declares
   (storage's `Authorizer`), implemented by iam. SuperTokens holds
   identities and sessions only, never our permissions.

## Decisions (2026-10-11)

- SuperTokens core self-hosted: a container in `deploy/monolith/compose.yaml` locally,
  ECS in production, its own database on our Postgres instance.
- Sign-in for the MVP: Google only (ThirdParty recipe; needs a Google OAuth client ID
  and secret). No email+password or passwordless yet; adding them later is another
  recipe here and changes nothing in iam.
- Internal services trust `actor_id`; verifying the forwarded access token in a
  `platform/auth` gRPC interceptor (defence in depth) is deferred.

## Browser-facing API (decided 2026-10-11)

- The browser talks only to the gateway: REST/JSON for CRUD (projects, assets,
  folders, shares, invites, orgs) plus the WebSocket. Files never pass through it
  (presigned S3 URLs), so the API carries metadata only.
- Hand-written handlers on the standard library (`net/http`, Go 1.22+ method and path
  patterns such as `GET /projects/{id}`), JSON request and response types owned by the
  gateway, and one error -> HTTP status table per service (`ErrForbidden` -> 403,
  not-found -> 404, invalid -> 400, `ErrNameTaken`, `ErrAlreadyMember` and
  `FolderNotEmptyError` -> 409, the latter with its counts).
- Handlers depend only on `iam.Service` and `storage.Service`: the monolith passes
  the in-process Managers, distributed mode passes the gRPC clients, and the handler code
  is the same. No REST anywhere else; services talk gRPC (distributed) or Go calls
  (monolith).
- The internal gRPC contracts are never exposed: they carry `actor_id`, which services
  trust. The gateway owns its own API (backend-for-frontend) with no actor fields; the
  actor always comes from the SuperTokens session. (A generic REST->gRPC proxy such as
  grpc-gateway was rejected for that reason.)
- Service-to-service calls (`DeleteOwned`, `RemoveUser`, `CopyProjectFiles`,
  `CreateTemplate`, `SignIn` apart from the sign-in hook) get no routes.
- TypeScript client written by hand for now; an OpenAPI description can be added later
  to generate it.

## NATS to WebSocket (decided 2026-10-12)

Rule: one consumer per gateway instance, never one per project or socket
([platform/nats](../../platform/nats/README.md)). Fan-out happens here.

- One ordered (ephemeral) consumer per instance on the `DOCUMENTS` stream
  (`document.>`), starting at new messages. Each message is routed by the project token
  in its subject to the sockets that have that project open; projects with no sockets
  are skipped. An ordered consumer recreates itself after a gap, so nothing per
  project needs restarting.
- Deltas and snapshot pointers are both forwarded; the client merges a pointed-to
  snapshot (apps/web/PLAN.md).
- Joining a project: register the socket for it first, then call documents'
  `GetDocument` (the snapshot with the stream sequence it covers) and send it.
  Deltas fanned out in between may be in the snapshot already; the client applies them
  idempotently, so the overlap is harmless and nothing is lost.
- User state: one core-NATS subscription per instance on `userstate.>`, fanned out the
  same way.
- Browser deltas: the gateway publishes them to `document.{p}.delta` after checking
  write access (`CanWrite`), with the op ID as `Nats-Msg-Id`. Never set
  `Modulith-Origin` on them: documents trusts the stamps of deltas carrying it (another
  documents replica published them) and bounds the rest by `MaxSkew`, so an empty
  Origin is what keeps a browser from forging stamps
  ([engine/PLAN.md](../../internal/engine/PLAN.md) row 2). Also forward `ReplaceAssetRef`
  after a `ForkAsset` (documents' "Fork").
- Snapshots: route only `SnapshotURL` (read, for a client fetching the snapshot a
  pointer names). Never route `BeginSnapshot`/`CompleteSnapshot`: stored snapshots
  are trusted unchecked, so only documents may write them (storage README "Document
  snapshots").

## Build steps (planned 2026-10-12)

Three steps, each runnable on its own. The frontend needs step 1 to sign in and step 2
for everything else; step 3 only once there's a canvas.

### Step 1: sign-in

**Done (2026-10-13).** As planned, except: SuperTokens' tables are a `supertokens` schema
in the `modulith` database (`POSTGRESQL_TABLE_SCHEMA`), not a database of their own, matching
one schema per service; the demo client routes through a SuperTokens proxy, so any
localhost callback works (the gateway stays on 8080); the identity provider calls the
core's HTTP API directly, so `cmd/iam` needs no SDK. Sign-in and sign-out checked in a browser
(2026-10-13); Google needs the `profile` scope for the name (the SDK asks only `openid
email`). Still to do: our own OAuth client before leaving localhost.


- **SuperTokens core:** a `supertokens/supertokens-postgresql` container in
  `deploy/monolith/compose.yaml` (pinned version), on its own database (`supertokens`)
  on the compose Postgres; `MODULITH_SUPERTOKENS_URL` (default `http://localhost:3567`).
  The distributed compose gets the same service.
- **Google credentials:** SuperTokens' public demo keys for now (from the SuperTokens
  docs, `localhost` only), as `.env.example` defaults for
  `MODULITH_GOOGLE_CLIENT_ID`/`MODULITH_GOOGLE_CLIENT_SECRET`. Verify which website domain
  and callback path the demo client allows (their examples use `http://localhost:3000`
  and `/auth/callback/google`), and set the gateway's `WebsiteDomain`/`APIDomain` to
  match. Swap in our own OAuth client before anything leaves localhost.
- **Callback without a frontend:** Google redirects to the *website* domain, whose page
  then posts the code to `/auth/signinup`. Until `apps/web` exists, the gateway serves a
  tiny dev-only sign-in page and callback page itself, so step 1 can be smoke-tested
  alone (gateway on the demo client's allowed origin, or the dev pages proxied there).
- **`services/gateway`** (library package, the storage layout): `wiring.go` with
  `SettingsFromEnv` (core URL, Google keys, API and website domains, listen address,
  default `localhost:8080`) and `Open`, which calls `supertokens.Init` (ThirdParty with
  Google, Session) and returns an `http.Handler` serving `/auth/*`, plus
  `GET /me` behind `VerifySession` as the first protected route.
- **Sign-in hook:** override ThirdParty's sign-in-up to call iam's `SignIn` with
  `Identity{UserID: the SuperTokens user ID, Email, EmailVerified, Name,
  ProviderAvatarURL}` from Google's user info, on every sign-in. The gateway depends on
  a narrow interface it declares (`SignIn` only), satisfied by iam in-process or over
  gRPC. If `SignIn` fails, the sign-in fails (no session without an iam user).
- **`IdentityProvider`** for iam: `DeleteUser` via the SuperTokens SDK (deleting a
  missing user succeeds). The monolith wires it instead of `NoIdentityProvider`;
  `cmd/iam` needs the core URL too.
- **Binaries:** `cmd/modulith` serves the gateway on its own listener next to gRPC (same
  shutdown sequence: `http.Server.Shutdown` with the shutdown timeout);
  `cmd/gateway` for distributed mode, calling iam over gRPC.
- **Tests:** the hook's mapping and its failure path against iam's memory store with
  the SuperTokens parts behind the narrow interface; a smoke run signing in with
  Google locally and checking the `iam.users` row (`scripts/admin.sh` shows it).

### Step 2: REST API

**Done (2026-10-13)** as planned. Also: storage re-exports its DAM types and errors
(`storage.go`), and documents has `OpenLocal` (no NATS) for the gateway's tests. A move
plus rename in one `PATCH /folders/{id}` moves first; not tested: the fork route's
"rewrite failed" path (it needs a document edit to fail after a fork succeeded).

Design rules are in "Browser-facing API"; this was the build.

**Layout**

- `internal/session`: `WithActor(ctx, userID)` and `Actor(ctx)`. `auth` adds a
  `Require(http.Handler)` middleware: `VerifySession`, then the user ID into the context.
  `rest` reads only `session.Actor`, so it never imports SuperTokens and its tests set
  the actor directly.
- `internal/rest`: a `Server` holding `iam.Service`, `storage.Service`,
  `documents.Service` and storage's `Authorizer` (iam's, for the checks storage doesn't
  make, below). One file per area, grouped by prefix: `rest_me.go`, `rest_orgs.go`,
  `rest_projects.go`, `rest_assets.go`, `rest_folders.go`, `rest_documents.go`;
  `json.go` (decode and encode helpers, the wire types' conversions); `errors.go` (the
  status tables).
- `Open` mounts `/api/` routes behind `auth.Require`; `/auth/*` and `/dev/*` stay as
  they are. The handlers receive the services, in-process or gRPC clients alike.

**Conventions**

- JSON in and out, camelCase, wire types owned by the gateway (`rest` structs with json
  tags, converted from domain types; domain structs are never encoded directly, so a
  service's field can't leak by accident). Timestamps RFC 3339.
- Errors: `{"error": {"code": "NAME_TAKEN", "message": "..."}}`, plus extra fields where
  a sentinel carries data (`FolderNotEmptyError`'s counts). One table per service maps
  sentinels to status and code: forbidden 403, not found 404, invalid 400, conflicts 409
  (`ErrNameTaken`, `ErrAlreadyMember`, `ErrEmailTaken`, `ErrLastAdmin`,
  `FolderNotEmptyError`, `ErrForkRequired`), expired 410. Anything unmapped is 500 with a
  generic message, the real error logged.
- Lists: `?pageToken=`, responses carry `nextPageToken` (empty at the end).
- Owners in requests and responses: `{"kind": "user"|"org"|"project", "id": "..."}`.
  The `modulith` kind is never accepted from a browser (templates are service-to-service).
- Request bodies capped at 1 MiB (`http.MaxBytesReader`); files never pass through
  (presigned URLs).
- CSRF: session cookies are `SameSite=Lax`, every mutating route requires
  `Content-Type: application/json` (a cross-site form can't send it without a CORS
  preflight, and there is no CORS: the API and website share an origin). Revisit if
  `apps/web` is served from another origin.

**Routes** (all under `/api`, all need a session; the actor is the session's user)

| Area | Routes | Calls |
| --- | --- | --- |
| Me | `GET /me`, `DELETE /me`, `GET /me/invites`, `DELETE /me/org` | `GetMe` (profile, org, pending invites; replaces auth's `/me`), `DeleteAccount`, `ListMyInvites`, `LeaveOrg` |
| Orgs | `POST /orgs`, `GET/PATCH/DELETE /orgs/{id}`, `GET /orgs/{id}/members`, `PATCH/DELETE /orgs/{id}/members/{userId}` | `CreateOrg`, `GetOrg`, `RenameOrg`, `DeleteOrg`, `ListMembers`, `SetRole`, `RemoveMember` |
| Invites | `POST/GET /orgs/{id}/invites`, `DELETE /invites/{id}`, `POST /invites/{id}/accept`, `POST /invites/{id}/decline` | `InviteMember`, `ListOrgInvites`, `RevokeInvite`, `AcceptInvite`, `DeclineInvite` |
| Projects | `POST/GET /projects`, `GET /templates`, `GET/PATCH/DELETE /projects/{id}`, `POST /projects/{id}/clone`, `POST /projects/{id}/move-to-org`, `PUT /projects/{id}/org-lock` | `CreateProject`, `ListMyProjects`, `ListTemplates`, `GetProject`, `RenameProject`, `DeleteProject`, `CloneProject`, `MoveProjectToOrg`, `SetOrgLocked` |
| Shares | `GET/POST /projects/{id}/shares`, `DELETE /projects/{id}/shares/{userId}` | `ListShares`, `ShareProject`, `Unshare` |
| Uploads | `POST /uploads`, `POST /uploads/{id}/complete` | `BeginUpload` (returns the presigned PUT), `CompleteUpload` |
| Assets | `GET /assets` (scope and filters as query params), `GET/PATCH/DELETE /assets/{id}`, `POST /assets/{id}/restore`, `GET /assets/{id}/versions`, `POST /assets/{id}/revert`, `GET /assets/{id}/download?version=` | `ListAssets`, `GetAsset`, `UpdateAsset`, `DeleteAsset`, `RestoreAsset`, `ListVersions`, `RevertAsset`, `DownloadURL` |
| Folders | `GET /folders` (an owner's folder contents), `POST /folders`, `PATCH /folders/{id}` (rename and/or move), `DELETE /folders/{id}?tree=true`, `POST /folders/{id}/restore`, `GET /folders/{id}/path`, `GET /trash` | `ListFolderContents`, `CreateFolder`, `RenameFolder`/`MoveFolder`, `DeleteFolder`/`DeleteFolderTree`, `RestoreFolder`, `FolderPath`, `ListTrash` |
| Documents | `GET /projects/{id}/document`, `POST /projects/{id}/document/query`, `GET /projects/{id}/refs/{assetId}/{version}`, `POST /projects/{id}/forks`, `GET /projects/{id}/snapshots/{objectId}` | `GetDocument` (the snapshot as `application/octet-stream`, with `Modulith-Format`, `Modulith-Version` and `Modulith-Seq` headers), `QueryDocument`, `ResolveRef`, `ForkAsset` then `ReplaceAssetRef`, `SnapshotURL` |

- **Fork** is one route: `ForkAsset`, then `ReplaceAssetRef` with the fork's ref. If the
  rewrite fails after the fork succeeded, return the fork with the error in the
  response (retrying is safe: `ForkAsset` returns the same copy for the same version).
- **Snapshots:** storage's snapshot calls take no actor (service-to-service), so the
  gateway checks `CanRead` on the project through iam's authorizer first. Only
  `SnapshotURL` is routed; never `BeginSnapshot`/`CompleteSnapshot`.
- **Not routed:** service-to-service calls (`SignIn`, `DeleteOwned`, `RemoveUser`,
  `CopyProjectFiles`, `RecordAssetUse`/`ForgetAssetUse`, `CreateTemplate`/
  `DeleteTemplate`, the snapshot writes, `CloneDocument`); `RevertDocument` until iam has
  a staff flag; `ExportURL` until the exporter exists; document edits (`UpdateDocument`)
  go over the WebSocket (step 3).

**Prerequisites**

- storage's public surface re-exports the DAM errors the gateway must map
  (`ErrAssetNotFound`, `ErrVersionNotFound`, `ErrInvalidAsset`, `ErrInvalidQuery`,
  `ErrFolderNotFound`, `ErrInvalidFolder`, `ErrFolderNotEmpty`/`FolderNotEmptyError`,
  `ErrNameTaken`, `ErrUploadNotFound`, `ErrUploadExpired`, `ErrInvalidUpload`); today
  they're only in `internal/dam`. The gRPC client already rebuilds them, so mapping by
  `errors.Is` works on both transports.
- Wiring: `Open` takes the three services and the authorizer; `cmd/modulith` passes the
  in-process ones, `cmd/gateway` gRPC clients (storage, documents, and iam's
  `NewAuthzClient`), so it gains `MODULITH_STORAGE_ADDR` and `MODULITH_DOCUMENTS_ADDR`.

**Tests**

- A harness wiring the real in-memory iam, storage and documents (as the monolith does,
  `storage.NewMemory`, iam's memory store, documents over `engine.Mock`), the `rest`
  handler with a test middleware setting the actor, and `httptest`. Black box: HTTP in,
  JSON out.
- Per area, the main flows (create, list, update, delete) and one forbidden path through
  real permissions (a stranger gets 403). A table test that every mapped sentinel
  produces its status and code, and that an unmapped error is a generic 500.
- The fork route (fork plus rewrite; the rewrite failing still returns the fork), the
  snapshot route's read check, and the CSRF rule (a mutating request without JSON
  content type is refused).
- Mutation-check the error tables and the session requirement (a route reachable
  without a session must fail a test).
- Smoke: sign in on the dev page, then call `/api/me` and create a project.

### Step 3: WebSocket

**Done (2026-10-13)** as planned, with all six decisions below. The package is
`internal/ws` (not `live`, which documents already uses); `cmd/gateway` connects to NATS
(`MODULITH_NATS_URL`). Not tested: a real browser edit end to end (no WASM engine yet to
stamp one; the dev page can join and send user state), and the ordered consumer
recovering from a NATS restart. Design rules are in "NATS to WebSocket".

**Decisions** (confirmed 2026-10-13)

1. **Library:** `github.com/coder/websocket` (formerly nhooyr): context-aware,
   maintained, no dependencies, checks `Origin` on upgrade by default (same-origin
   only, matching the REST CSRF rule). The standard library has no server.
2. **Frames:** JSON text frames, one envelope `{"type": ..., ...}`; delta and snapshot
   bytes base64 inside. Simple to debug from the dev page. Binary frames (about 25%
   smaller) go in OPTIMIZATIONS.md as Open, "when deltas show up in profiles".
3. **One socket, many projects:** a socket joins and leaves projects by message, so a
   tab switching projects keeps its connection.
4. **Session expiry:** the access token's expiry is read at upgrade; when it passes,
   the gateway closes the socket with code 4001. The client refreshes
   (`/auth/session/refresh`) and reconnects, rejoining its projects (they resync from
   `GetDocument`, so nothing is lost). No in-band token refresh.
5. **Delta headers move to `platform/nats`:** the `Modulith-*` header names and the delta
   message encode/decode are a wire contract between documents and the gateway, but
   live in documents' `internal/wal`. They move to `platform/nats` (`documents.go`);
   `wal` uses them. The gateway still never sets `Modulith-Origin`.
6. **Slow sockets:** each socket has a bounded send queue (256 messages); a full queue
   closes the socket (code 4008), never blocks the fan-out. The client reconnects and
   resyncs.

**Protocol** (client -> server / server -> client)

- On connect: `hello {replica, serverTime}`. The replica ID is
  `{userID}.{random}`, unique per socket; the WASM engine stamps with it and offsets
  its clock by `serverTime`.
- `join {projectId}` -> `document {projectId, format, version, seq, payload}` after
  `CanRead`; then live `delta {projectId, seq, actorId, format, version, payload}` and
  `snapshot {projectId, version, seq, objectId}` (pointer; fetch with REST
  `GET /projects/{id}/snapshots/{objectId}`).
- `leave {projectId}`.
- `edit {projectId, id, format, version, payload}`: `CanWrite`, the delta's stamp
  replica must be the socket's (`engine.StampOf`; unstamped is refused too), then
  published to `document.{p}.delta` with `Nats-Msg-Id = id` and `Modulith-Actor` = the
  session user; `ack {id, seq}` or `error {id, code, message}` (REST's codes).
- `userstate {projectId, kind: viewing|cursorpos, data}`: published to
  `userstate.{p}.{sessionUser}.{kind}` (the user ID always from the session); joined
  sockets receive `userstate {projectId, userId, kind, data}`, own messages included
  (the client filters).
- Errors without an `id` (join refused, bad message) carry the `projectId` or nothing.

**Layout**

- `internal/live` (the gateway's): `Hub` (project -> sockets, the fan-out), one
  `socket` per connection (reader goroutine, writer goroutine draining the send queue),
  the handler mounted at `GET /api/ws` behind `auth.Require` (plus the expiry from the
  session). One socket per browser tab: each tab's WASM engine is its own replica, so
  two tabs are two peers (the two-tab test goes through the real path). The hub
  encodes each fanned-out message once and queues the same bytes to every socket in
  the project, never once per socket.
- The hub reads from and writes to a small interface: `Bus` (publish a delta, publish
  user state, run the delta/snapshot feed, run the user state feed). The NATS
  implementation: one ordered consumer on `document.>` from new messages, one core
  subscription on `userstate.>`. A memory `Bus` for tests.
- `engine.StampOf(DeltaInput) (lamport uint64, replica string, err error)`: read a
  delta's stamp without applying it (mock: decode the flatbuffer header).
- Wiring: `gateway.Services` gains the NATS client (`cmd/modulith` already has one;
  `cmd/gateway` connects with `MODULITH_NATS_URL`, added to the distributed compose).

**Tests**

- The hub and sockets against the memory `Bus`, real documents (`OpenLocal`) and real
  iam permissions, over `httptest` with a real WebSocket client: join gets the document
  then live deltas; a stranger's join is refused; edits need write access; an edit
  stamped with another replica, or unstamped, is refused; published deltas never carry
  an origin and carry the session's actor; user state subjects use the session user;
  leave stops deltas; a slow socket is closed without stalling others; expiry closes
  with 4001.
- The NATS `Bus` against the compose cluster (`MODULITH_TEST_NATS_URL`): a delta
  published by `wal` reaches a joined socket, and a gateway edit reaches documents'
  consumer.
- `engine.StampOf` in the engine's suite. Mutation-check the hub's checks.
- Smoke: the dev page gets a WebSocket box (connect, join, show messages).

## Open

- If browsers open WebSockets straight to documents replicas (bypassing the
  gateway), those replicas must verify SuperTokens tokens themselves (JWT + JWKS, no SDK
  needed).
