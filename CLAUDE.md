# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project status

Gyoza (formerly K2/KAD) is an early-stage monorepo re-architecture of the Kyoso platform, written after a post-mortem. `services/storagecatalogue` is the first implemented Go service. Other components hold only README intent notes. `libs/engine` is the existing Rust engine, dumped in as-is; it has its own `AGENTS.md` and build scripts and isn't integrated yet. `Cargo.toml`, `package.json` and `pyproject.toml` at the root are still empty placeholders.

The user is learning Go and is working through things step by step. Don't scaffold beyond what's asked. The README files are the spec: read the relevant one before working on a component, and record decisions there as they are made.

## Commands

Go is at `C:\Program Files\Go\bin\go.exe` (Go 1.27) and may not be on the shell's PATH.

```
go test ./...                                         # Postgres tests skip without the env var below
go test ./services/storagecatalogue -run TestMemory/ListPagination -v
go vet ./...
go generate ./services/storagecatalogue               # rerun sqlc after editing migrations/ or queries.sql
```

Postgres 18 runs locally as a Windows service (`psql` at `C:\Program Files\PostgreSQL\18\bin\psql.exe`, not on PATH). Dev database: `postgres://gyoza:gyoza@localhost:5432/gyoza`.

- `GYOZA_DATABASE_URL`: required by the binaries (`go run ./cmd/gyoza`, `go run ./cmd/storage-catalogue`). They connect, run migrations at startup, and run until Ctrl+C.
- `GYOZA_TEST_DATABASE_URL`: enables the Postgres tests. Each subtest runs in a rolled-back transaction, so they are safe against the dev database.

Always open pools with `platform/postgres.Connect`, never `pgxpool.New` directly: it sets `plan_cache_mode=force_custom_plan`, which the keyset-paginated queries depend on for index scans.

`sqlc` is a Go tool dependency (`go tool sqlc`), and builds without cgo. There's no C compiler or Docker on this machine.

## Go service pattern

Follow `services/storagecatalogue`:

- `catalogue.go`: domain types, errors, the `Service` interface, and validation/paging helpers shared by every implementation.
- `memory.go`: in-memory fake. `postgres.go`: real implementation on sqlc-generated code in `internal/db`.
- `migrations/`: goose SQL migrations, embedded and applied by `Migrate` at startup. Each service owns one Postgres schema, and goose's version table lives inside it.
- `service_test.go`: one shared suite (`runServiceTests`) that every implementation runs (`memory_test.go`, `postgres_test.go`), so the fake can't drift from the real thing.
- `OPTIMIZATIONS.md` (next to the README): performance findings with evidence, split into Done and Open (with "when to act"). Add an entry whenever a performance issue is found, fixed or deliberately deferred.
- Never edit a migration that has been applied; add a new one.
- `cmd/` mains are wiring only: `main` calls `run() error` so defers run before `os.Exit`. Shutdown sequence: after `<-ctx.Done()` call `stop()` (a second Ctrl+C then kills the process), derive `context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)`, and stop things in reverse startup order. Blocking closes without a context (`pool.Close`, gRPC `GracefulStop`) go through `platform/shutdown.Within`, never a bare `defer`, so a stuck connection can't hang exit.
- Go packages have no hyphens or underscores; binary names under `cmd/` may.

## Languages

The previous system had two Rust repos (the engine and the document server, which is the engine's native client), a ReactJS frontend, Node.js middlewares, and Python FastAPI for all other backends. The new plan:

- **Rust**: `libs/engine`, `services/document-server`
- **Go**: `gateway`, `storage-catalogue`, `storage-manager`, `user-manager`, `media-processor`, `platform/`. The user is using these to learn Go, so prefer idiomatic, standard-library-first Go and explain non-obvious idioms.
- **TypeScript/React**: `apps/web`
- **Python**: `services/creator-agent`, `libs/agent`. This can be deferred for the MVP by calling image-gen APIs directly from Go.

## Layout and the monolith/distributed goal

The repo must deploy either as a monolith or as separate services. Only same-language code can share a process, so in practice "monolith" means one Go binary, with the Rust document server, the web app and the optional Python agent running alongside it. The layout keeps service logic separate from packaging:

- `services/<name>/`: domain logic. A Go service is a library package (no `main`) exposing a `Service` interface and a constructor, and owns its `migrations/`. The Rust document server will contain its own `crates/`.
- `cmd/`: thin Go `main` packages that only do wiring. `cmd/gyoza` is the monolith, which runs every Go service in-process with embedded NATS. `cmd/<service>` builds one binary per service for distributed mode.
- `platform/`: shared Go plumbing (config, Postgres, NATS, auth/session, telemetry, client wiring). `platform/jobs` is the NATS + Postgres job queue, planned as a library rather than a service.
- `contracts/proto/`: protobuf APIs, generated with `buf` for Go, Rust (prost/tonic) and TS.
- `libs/`: domain libraries (`engine`, `agent`). These are not plumbing.
- `deploy/{monolith,distributed}`: packaging (compose files / ECS task definitions). `infra/aws/` holds cloud resources.
- Toolchain workspaces live at the repo root, and code stays grouped by domain rather than by language.

**Rules that keep the split cheap:**
- A service depends on other services only through their interfaces. The monolith injects the in-process implementation, and distributed mode injects a gRPC client generated from `contracts/`.
- Use one PostgreSQL instance with **one schema per service**. A service never reads another service's tables.

## Services

- `document-server`: the successor to Kyoso-server. It does server-side rendering, log-rolls document snapshots to S3 and load-balances replicas, with allocation tracked in DynamoDB. It was previously built on Axum; Dioxus and Leptos are under consideration. Planned crates: the server binary, a router/load-balancer binary, types, reusable data structures, and NATS and AWS SDK wrappers kept swappable.
- `gateway`: the single entry point from the frontend, merging the old REST and NATS→WebSocket middlewares. It handles SuperTokens auth (previously done in the middleware). REST and WS share one session layer.
- `storage-manager`: S3 get/put for uploads and assets (not documents). It calls `storage-catalogue` over gRPC.
- `storage-catalogue`: the PostgreSQL ledger of every S3 object. It links to user data for GDPR deletion.
- `user-manager`: users, orgs, permissions, projects and billing tables.
- `media-processor`: an ffmpeg wrapper.
- `creator-agent`: the agentic/GenAI service.

**Infrastructure**: AWS S3, PostgreSQL, ECS (CPU services), EC2-on-ECS (stateful CPU/GPU document servers), DynamoDB, NATS/JetStream, OpenTelemetry/DataDog.

## Design principles

- Use as few external technologies as possible, and build as little as possible for the MVP.
- Open questions: where gRPC should replace REST, how auth identity propagates to internal services, and whether authorization lives at the gateway or in each service.
