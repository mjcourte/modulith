# Gyoza

A modular-monorepo Go graphic design platform.

The backend is Go, split into services that run either together as one binary (the monolith) or as separate binaries talking gRPC. Locally, Docker hosts the external pieces: Postgres, RustFS (S3-alike), NATS and the SuperTokens core. Cover as much ground as possible with Postgres before adding anything else.

What each component has built is in its README; what's next, in its `PLAN.md`.

## Running it locally

Four things run on your machine, each in its own terminal (Git Bash on Windows). The
scripts work from any directory and find Go, Node and Docker in their default install
locations.

```sh
cp .env.example .env    # once: settings for every binary (real env vars win); see .env.example
scripts/gyoza.sh        # 1. the backend: Docker deps, then the monolith
scripts/web.sh          # 2. the web app (Vite dev server)
scripts/admin.sh        # 3. the dev admin UI (optional)
scripts/signoz.sh       # 4. SigNoz, for traces (optional; starts in the background)
```

| What | URL | Notes |
| --- | --- | --- |
| Web app | http://localhost:3000 | sign in here; proxies `/api` and `/auth` to the gateway |
| Gateway (REST, WebSocket) | http://localhost:8080 | `/api/...`, `/api/ws`, `/auth/...`; browsers use it through the web app |
| gRPC (every service) | `localhost:9090` | `go tool buf curl` (examples in `contracts/README.md`) |
| Admin UI | http://localhost:8081 | browse both schemas, act as any user |
| SigNoz | http://localhost:3301 | traces; OTLP on `localhost:4317` (gRPC) and `4318` (HTTP) |

1. **Backend** (`scripts/gyoza.sh`): starts `deploy/monolith/compose.yaml` on Docker
   Desktop (Postgres on `5433`, RustFS as S3 on `9000`, a 3-node NATS cluster on `4222`,
   the SuperTokens core on `3567`), then `go run ./cmd/gyoza`: every service in one
   process, the gateway included. It runs migrations and creates the bucket at start.
   `--no-deps` skips compose. Ctrl+C stops it (a second Ctrl+C forces exit).
2. **Web app** (`scripts/web.sh`, `apps/web`): installs `node_modules` the first time.
   Signing in needs `GYOZA_PUBLIC_URL=http://localhost:3000` in `.env` (the default in
   `.env.example`): the gateway scopes its session cookies and Google's redirect to it.
3. **Admin UI** (`scripts/admin.sh`, `apps/admin`): makes every change through the running
   monolith, so start the backend first. Only reachable as `localhost`.
4. **SigNoz** (`scripts/signoz.sh`, `deploy/observability`): runs on Docker Engine inside
   WSL 2, not Docker Desktop. One-time setup (and the first-account signup, without which
   nothing is ingested) is in `deploy/observability/README.md`. Every binary sends its
   traces, logs and metrics when `GYOZA_OTLP_ENDPOINT=http://localhost:4317` is set in
   `.env`; `GYOZA_DEBUG_ADDR` adds pprof. Details in `platform/telemetry/README.md`.
   `scripts/signoz.sh down` stops it.

Stopping and resetting the Docker dependencies is in `deploy/README.md`; running each
service as its own binary (distributed) is in `cmd/README.md` and `deploy/README.md`.

## Data Organization

- one Postgres instance, one schema per service (`iam`, `storage`, ...); no service reads another's tables
- S3 for file bytes; storage keeps a Postgres ledger of every S3 object, linked to its owner (for GDPR deletion)
- live documents: edits stream through NATS JetStream, snapshots are stored via storage

## Layout

- `apps/` - the web app (`web`) and the dev admin UI (`admin`)
- `services/` - domain logic, one folder per service. Go services are packages with no `main`
- `cmd/` - Go entrypoints. `gyoza` = monolith, `<service>` = one binary each for distributed
- `platform/` - shared Go plumbing: config, postgres, nats, shutdown, telemetry (jobs planned)
- `internal/engine` - the Go binding to the rendering engine; an in-memory mock for now
- `engine/` - the native engine (C++ or Rust): schema so far, the rest planned
- `contracts/proto/` - gRPC/protobuf APIs, generated for Go
- `deploy/` - monolith and distributed packaging, plus SigNoz (`observability/`)
- `scripts/` - run helpers (see Running it locally)

## Apps/Services

Built:

- `apps/web` - the web app (React, Vite)
- `apps/admin` - dev-only admin UI over both schemas
- `gateway` - the browser's single entry point: REST + WebSocket, SuperTokens sign-in
- `iam` - users, orgs, projects, invites, sharing and permissions
- `storage` - every S3 operation: uploads, downloads, the DAM, and the ledger of every S3 object
- `documents` - live documents on the engine: applies edits from NATS, snapshots via storage
- `mediaprocessor` - ffprobe wrapper that reads uploaded media's specs for storage

Planned (README only):

- `exporter` - headless renderer producing exports
- `mcp` - agent/genAI BYOA hookups via MCP

## Technologies/Vendors

- AWS S3 - object storage. Workalike: RustFS in Docker.
- PostgreSQL - databases. Docker (or a local install).
- NATS/JetStream - message streams. Docker.
- SuperTokens - auth (Google sign-in). Docker.
- OpenTelemetry + SigNoz - traces, logs and metrics. SigNoz self-hosted, in Docker inside WSL 2.

Some imaginary future where this becomes a real thing, this could be microserviced with vendor outsourcing for the above.

## Non-goals / exclusions

- agent stuff is basically deferred to MCP BYOA
- all downstream stuff like brand guides and whatever dropped in aid of BYOA
- agent layout 1 shots should be wired via MCP, but thats like a capstone on having a platform that works without agent
- import from any other design suite sod it who needs em.
