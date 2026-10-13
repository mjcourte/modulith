# Gateway

The single entry point from the browser. Built in Go (previously Node.js, merged from
the former `rest-middleware` and `nats-ws-middleware`, so REST and the WebSocket share
one session layer). Design and next steps: [PLAN.md](PLAN.md).

## Status (2026-10-13): sign-in, the REST API and the WebSocket

| Path | What |
| --- | --- |
| `gateway.go`, `wiring.go` | public surface: `Open` (the HTTP handler and `StartFeed`), `SettingsFromEnv`, `IdentityProviderFromEnv` (iam's) |
| `internal/auth` | SuperTokens setup, `Require` (the session middleware), the sign-in hook, `CoreIdentityProvider`, dev pages |
| `internal/session` | the actor and the access token's expiry in a request's context |
| `internal/rest` | the JSON API under `/api`: one route per service call, wire types, error tables |
| `internal/ws` | the WebSocket (`/api/ws`): the `Hub` (fan-out), sockets, the NATS `Bus` |

- SuperTokens (`supertokens-golang` v0.26.0, core `supertokens-postgresql:12.2.0` in
  `deploy/monolith/compose.yaml`, tables in the `supertokens` schema). Google only
  (ThirdParty recipe), cookie sessions only (never tokens in headers page scripts can
  read). SuperTokens serves `/auth/*`; everything under `/api` is behind `Require`.
  The website base path is `/login` (apps/web's sign-in UI and Google callback,
  `/login/callback/google`): on one origin it must differ from the API's `/auth`.
- **Sign-in hook:** every successful sign-in also calls iam's `SignIn` (an upsert) with the
  SuperTokens user ID and Google's claims (`email_verified`, `name`, `picture`, the ID
  token preferred). If iam fails, the sign-in fails: no session without an iam user.
- **Account deletion:** iam's `IdentityProvider` removes the sign-in account through the
  core's HTTP API (`/user/remove`, OK for a missing user). It needs no SDK set-up, so
  `cmd/iam` uses it too.
- **Dev pages** (`GYOZA_GATEWAY_DEV_PAGES`, never in production): `/dev/signin` does what
  the SuperTokens frontend SDK would (authorisation URL, then the code to
  `/auth/signinup` on `/dev/callback/google`), plus "who am I" and sign-out.
- **Google client:** SuperTokens' public demo client for now (`.env.example`). The SDK
  routes it through supertokens.io, so any localhost callback works; replace it with our
  own OAuth client before anything leaves localhost.
- **REST API** (`/api`, PLAN.md step 2 has the route table): me, orgs, invites, projects,
  shares, uploads, assets, folders, trash, documents (the snapshot as bytes with
  `Gyoza-Format`/`Gyoza-Version`/`Gyoza-Seq`, queries, refs, forks, snapshot downloads).
  JSON wire types owned here; errors `{"error": {"code", "message"}}` from one table per
  service (unmapped: a generic 500, logged). Changing requests must be
  `application/json` (the CSRF defence, with SameSite cookies and no CORS). Snapshot
  downloads check read access here, as storage's snapshot calls don't.
- **WebSocket** (`GET /api/ws`, `github.com/coder/websocket`; protocol in PLAN.md step 3):
  one socket per browser tab, each its own engine replica (`hello` names it, with the
  server time). Joining a project (read access, through `GetDocument`) sends the document,
  then its deltas, snapshot pointers and user state; traffic arriving during the join is
  held until the document is sent, minus what it covers. Edits need write access and the
  socket's replica in their stamp (`engine.StampOf`), and are published as the session's
  user, never with `Gyoza-Origin`. User state is always the session's user. Same-origin
  only; closes with 4001 when the access token expires and 4008 when the tab falls 256
  messages behind (both: reconnect and rejoin). One NATS consumer and one `userstate.>`
  subscription per instance; each message is encoded once for all sockets.
  The delta wire format (`Gyoza-*` headers) is in `platform/nats`, shared with documents.
- **Dev pages** also have a box to call any `/api` route as the signed-in user.
  and a WebSocket box (connect, send any message, see what comes back).
- The SDK keeps global state: `Open` runs once per process.
- Binaries: `cmd/gyoza` serves it next to gRPC; `cmd/gateway` alone, calling iam, storage and
  documents over gRPC, and NATS. Tests: `internal/auth` (the hook, the identity provider, `/api` requiring a session;
  the core tests need `GYOZA_TEST_SUPERTOKENS_URL=http://localhost:3567`) and
  `internal/rest` (every area over the real in-memory iam, storage and documents through
  `httptest`, and the error tables) and `internal/ws` (a real WebSocket client against the
  same services and a memory bus; the NATS bus needs `GYOZA_TEST_NATS_URL`); mutation-checked.
