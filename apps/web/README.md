# Web app

The browser frontend. Next steps: [PLAN.md](PLAN.md).

## Status (2026-10-13): sign-in and the signed-in user

Google sign-in through the gateway, a session-guarded home page showing the user from
`GET /api/me`, and sign-out.

| Path | What |
| --- | --- |
| `src/main.tsx` | entry: `./auth` first, then StrictMode, `SuperTokensWrapper`, TanStack Query, the router |
| `src/auth.ts` | `SuperTokens.init`: Google (ThirdParty) and Session, API at `/auth`, UI at `/login` |
| `src/routes/` | file-based routes; `routeTree.gen.ts` is generated from them (committed, never edited) |
| `src/api/` | `client.ts` (`api<T>()` and `ApiError`), one file per area with wire types and hooks (`me.ts`) |
| `src/routes/_app.tsx` | pathless layout: session guard, `useMe`, `WorkspaceProvider`, the sidebar around an `Outlet`; signed-in pages live under `_app/` |
| `src/lib/workspace*.ts(x)` | `useWorkspace()`: the active owner (user or org); its kind is kept in localStorage (`modulith.workspace`) |
| `src/components/` | app components (`AppSidebar`, `WorkspaceSwitcher`, `UserNav`, `CreateOrgForm`, `UserAvatar`, `SuperTokensRoutes`); `ui/` is shadcn's |

- **Stack:** React 19 + TypeScript, Vite, Tailwind v4, shadcn/ui on Base UI (style
  `base-lyra`, Phosphor icons), TanStack Router (file-based, its Vite plugin) and TanStack
  Query, `supertokens-auth-react`. npm; oxlint.
- **One origin.** The gateway has no CORS, uses SameSite cookies and same-origin
  WebSockets, so the browser only talks to `http://localhost:3000`: Vite proxies `/api`
  (with WebSockets) and `/auth` to the gateway on `:8080`. The gateway needs
  `MODULITH_PUBLIC_URL=http://localhost:3000` (its SuperTokens website and API domain).
  In production the gateway (or whatever fronts it) serves `dist/` on the same origin.
- **Sign-in.** SuperTokens' prebuilt UI at `/login`, the Google callback at
  `/login/callback/google` (`components/SuperTokensRoutes.tsx` renders whichever
  matches). The website path must differ from the API's `/auth`, or the proxy would send
  the callback page to the gateway; the gateway sets the same `WebsiteBasePath`. Works with
  the SuperTokens demo Google client on localhost:3000. Redirects after sign-in and
  sign-out are full page loads.
- **Routes** needing a session check `Session.doesSessionExist()` in `beforeLoad` and
  redirect to `/login`. `Session.init()` patches `fetch`, so an expired access token is
  refreshed and the call retried; a 401 reaching `api()` means signed out.
- **API calls** go through `api<T>(path, { json })`: JSON bodies get
  `Content-Type: application/json` (the gateway requires it on changing requests);
  errors throw `ApiError` with the gateway's `{"error": {"code", "message"}}`. Wire
  types are hand-written from `services/gateway/internal/rest` (Go `time.Time` is an ISO
  string).
- **Google avatars** need `referrerPolicy="no-referrer"` (Google rejects a localhost
  Referer); `UserAvatar` sets it and falls back to initials.

## Status (2026-10-14): app shell

- **Sidebar** (`AppSidebar`): the workspace switcher on top; Home, Projects, Asset
  Library, Shared, Organization (always shown: without an org it will offer create or
  join), Trash. The footer (`UserNav`) shows the user and their org, Settings
  (`/profile`) and Sign out. Items light up via `useMatchRoute` (fuzzy, except Home).
- **Workspace** (`useWorkspace()`): "Personal" or the user's org. It sets the owner the
  UI passes to the APIs, never who acts (`actor_id` is always the signed-in user).
  Without an org it falls back to the user.
- Base UI's `render` prop (not `asChild`) makes a shadcn button a link or menu trigger.

## Commands

From `apps/web` (Node 24 at `C:\Program Files\nodejs`):

```sh
npm run dev        # http://localhost:3000; scripts/web.sh does the same from anywhere
npm run build      # tsc -b, then vite build into dist/
npm run lint       # oxlint
npx shadcn@latest add <component>   # copies it into src/components/ui
```

Full stack: `MODULITH_PUBLIC_URL=http://localhost:3000` in `.env`, then `scripts/modulith.sh`
and `scripts/web.sh`.

## Wishlist

- canvas background colour/brightness (aka dark mode? this should be for in-app only, not exports)
