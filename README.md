# Gyoza

A modular-monorepo Go graphic design platform.

Basically the v0 plan is get it running as a local docker container, but actually requires compose to host external services: rustfs (local S3 alike), postgres, NATS, and probably a local redis or dynamodb alike. Try to cover as much ground as possible with PostGres.

The Go services are divided into approximate microservices, but can be run initially as a monolith.

## Running the monolith

`cmd/gyoza` runs every service in one process. It needs Postgres and an S3-compatible store; `deploy/monolith/compose.yaml` provides both (plus NATS) via Docker.

```sh
docker compose -f deploy/monolith/compose.yaml up -d --wait   # Postgres on :5433, RustFS (S3) on :9000, NATS on :4222
cp .env.example .env                                          # binaries read .env from the working directory; real env vars win
go run ./cmd/gyoza                                            # runs migrations, creates the bucket, serves gRPC on localhost:9090
```

Stop with Ctrl+C (a second Ctrl+C forces exit). To use a locally installed Postgres instead, point `GYOZA_DATABASE_URL` in `.env` at it (e.g. `localhost:5432`). Until the gateway exists, call the services over gRPC with `go tool buf curl` (see `contracts/README.md`). See `deploy/README.md` for stopping/resetting the containers and `.env.example` for every setting.

## Data Organization

- PostgreSQL with tables for customers, organizations, projects, permissions, billing
- one PostgreSQL schema per service, no cross-service table reads
- S3 for bulk storage
- PostgreSQL object ledger for S3

## Layout

- `apps/` - frontend(s?)
- `services/` - domain logic, one folder per service. Go services are packages with no `main`
- `cmd/` - Go entrypoints. `gyoza` = monolith, `<service>` = one binary each for distributed
- `platform/` - shared Go plumbing (config, pg, NATS, auth, telemetry, jobs)
- `internal/` - domain libraries, TBD, looks like we need a rendering engine...
- `engine/` - 3 guesses what this is
- `contracts/proto/` - gRPC/protobuf APIs, generated for Go
- `deploy/` - monolith AND distributed packaging

## Apps/Services

- `apps/web` - web app frontend ReactJS
- `documents` - document WAL rolling service (Rust? Go?)
- `exporter` - renderer service (Rust? Go?) ^- these must be Go for monolith.
- `iam` - identity and access management: users, orgs, projects, sharing (Go)
- `storage` - s3 object service (Go): uploads, the DAM, and the ledger of every S3 object
- `gateway` - REST + WS middlewares merged, SuperTokens auth
- `mcp` - agent/genAI BYOA hookups/MCP
- `media-processor` - does ffmpeg stuff, like read file specs and edit
- `platform/jobs` - NATS + pgSQL job queue, a library not a service

## Technologies/Vendors

- AWS S3 - object storage. Workalike: RustFS Docker for now.
- PostgreSQL - databases. Docker for now.
- NATS/JetStream - message streams. Docker.
- SuperTokens - auth. Docker.
- OpenTelemetry/DataDog - Tracing/logging/metrics. Try SigNoz?

Some imaginary future where this becomes a real thing, this could be microserviced with vendor outsourcing for the above.

## Non-goals / exclusions

- agent stuff is basically deferred to MCP BYOA
- all downstream stuff like brand guides and whatever dropped in aid of BYOA
- agent layout 1 shots should be wired via MCP, but thats like a capstone on having a platform that works without agent
- import from any other design suite sod it who needs em.
