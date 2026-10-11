# Plan: gateway (`services/gateway`): auth

Not started. Decided while planning usermanager (2026-10-11). Related:
[usermanager/PLAN.md](../usermanager/PLAN.md) (`SignIn`, `Identity`),
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
   (storagemanager's `Authorizer`), implemented by usermanager. SuperTokens holds
   identities and sessions only, never our permissions.

## Decisions (2026-10-11)

- SuperTokens core self-hosted: a container in `deploy/monolith/compose.yaml` locally,
  ECS in production, its own database on our Postgres instance.
- Sign-in for the MVP: Google only (ThirdParty recipe; needs a Google OAuth client ID
  and secret). No email+password or passwordless yet; adding them later is another
  recipe here and changes nothing in usermanager.
- Internal services trust `actor_id`; verifying the forwarded access token in a
  `platform/auth` gRPC interceptor (defence in depth) is deferred.

## Work (when the gateway is built)

- SuperTokens init (ThirdParty/Google, Session), compose service for the core.
- Sign-in hook: on every Google sign-in, call usermanager `SignIn(identity)` (SuperTokens
  user ID, email, verified flag, name, Google picture URL) to upsert the user; it returns pending
  org invites for the UI.
- `Identity` implementation for usermanager (`DeleteUser` via the SuperTokens SDK), used
  when an account is deleted.
- REST/WS routes mapping to service calls with the session's user ID as `actor_id`; WS
  sessions verified at upgrade and on expiry.
- Later: `platform/auth` interceptor checking the forwarded token matches `actor_id`.

## Open

- If browsers open WebSockets straight to documentmanager replicas (bypassing the
  gateway), those replicas must verify SuperTokens tokens themselves (JWT + JWKS, no SDK
  needed). See documentmanager/PLAN.md.
