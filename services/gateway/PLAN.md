# Plan: gateway (`services/gateway`): auth

Not started. Decided while planning iam (2026-10-11). Related:
[iam/PLAN.md](../iam/PLAN.md) (`SignIn`, `Identity`),
[apps/web/PLAN.md](../../apps/web/PLAN.md) (the sign-in UI).

## Auth design: three layers, three places

1. **Authentication (who is this?)**: only here. SuperTokens Go SDK
   (`supertokens-golang`) serves `/auth/*` (sign in/out, refresh) with cookie sessions
   and anti-CSRF. `VerifySession` middleware on every API route and on the WebSocket
   upgrade; access tokens are JWTs checked locally against the core's JWKS, so no
   network hop per request. No other service sees cookies or credentials.
2. **Identity propagation (gateway -> services)**: explicit `actorID` in Go `Service`
   methods and `actor_id` on the wire (already in storagemanager). Services trust it,
   so they're reachable only inside the platform (private subnets, security groups:
   only the gateway). Service-to-service calls (`DeleteOwned`, `RemoveUser`,
   `CopyProjectFiles`, gyoza project management) are never routed by the gateway.
3. **Authorization (may they?)**: in each service, through an interface it declares
   (storagemanager's `Authorizer`), implemented by iam. SuperTokens holds
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
- Handlers depend only on `iam.Service` and `storagemanager.Service`: the monolith passes
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

## Work (when the gateway is built)

- SuperTokens init (ThirdParty/Google, Session), compose service for the core.
- Sign-in hook: on every Google sign-in, call iam `SignIn(identity)` (SuperTokens
  user ID, email, verified flag, name, Google picture URL) to upsert the user; it returns pending
  org invites for the UI.
- `Identity` implementation for iam (`DeleteUser` via the SuperTokens SDK), used
  when an account is deleted.
- REST/WS routes mapping to service calls with the session's user ID as `actor_id`; WS
  sessions verified at upgrade and on expiry.
- Later: `platform/auth` interceptor checking the forwarded token matches `actor_id`.

## Open

- If browsers open WebSockets straight to documentmanager replicas (bypassing the
  gateway), those replicas must verify SuperTokens tokens themselves (JWT + JWKS, no SDK
  needed). See documentmanager/PLAN.md.
