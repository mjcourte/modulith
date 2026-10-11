# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project status

Gyoza (formerly K2/KAD) is an early-stage monorepo re-architecture of the Kyoso platform, written after a post-mortem. `services/storageledger` and `services/storagemanager` (steps 1-10 of its plan: DAM, cascades, Postgres store, S3 blobs, cleanup job, gRPC API and binaries) are implemented in Go. Other components hold only README intent notes. `libs/engine` is the existing Rust engine, dumped in as-is; it has its own `AGENTS.md` and build scripts and isn't integrated yet. `Cargo.toml`, `package.json` and `pyproject.toml` at the root are still empty placeholders.

The user is learning Go and is working through things step by step. Don't scaffold beyond what's asked. The README files are the spec: read the relevant one before working on a component, and record decisions there as they are made.

## Commands

Go is at `C:\Program Files\Go\bin\go.exe` (Go 1.27) and may not be on the shell's PATH.

```
go test ./...                                         # Postgres tests skip without the env var below
go test ./services/storageledger -run TestMemory/ListPagination -v
go vet ./...
go generate ./services/storageledger               # rerun sqlc after editing migrations/ or queries.sql
go generate ./contracts                               # buf lint + generate after editing .proto (needs go on PATH)
```

Call a running gRPC service by hand with `go tool buf curl` (example in `contracts/README.md`). Run it from bash: Windows PowerShell 5.1 strips the double quotes from JSON arguments to native programs.

Postgres 18 runs locally as a Windows service (`psql` at `C:\Program Files\PostgreSQL\18\bin\psql.exe`, not on PATH). Dev database: `postgres://gyoza:gyoza@localhost:5432/gyoza`.

- `GYOZA_DATABASE_URL`: required by the binaries (`go run ./cmd/gyoza`, `./cmd/storage-ledger`, `./cmd/storage-manager`). They connect, run migrations at startup, and run until Ctrl+C. Binaries hosting storagemanager (the monolith, `storage-manager`) also need the `GYOZA_S3_*` settings, and `storage-manager` needs `GYOZA_LEDGER_ADDR`; `.env.example` has working local values.
- `GYOZA_TEST_DATABASE_URL`: enables the Postgres tests. Each subtest runs in a rolled-back transaction, so they are safe against the dev database. They do assume the test owners (`alice`, `bob`, `acme`, `p1`, ...) have no committed rows there: manual runs against the dev database must use other IDs.

Always open pools with `platform/postgres.Connect`, never `pgxpool.New` directly: it sets `plan_cache_mode=force_custom_plan`, which the keyset-paginated queries depend on for index scans.

`sqlc` is a Go tool dependency (`go tool sqlc`), and builds without cgo. There's no C compiler on this machine.

Docker Desktop is installed (`%LOCALAPPDATA%\Programs\DockerDesktop\resources\bin`, may not be on PATH; the CLI needs that folder on PATH to find its credential helper). `deploy/monolith/compose.yaml` runs Postgres on `localhost:5433` as a second option alongside the local install on 5432, and RustFS (S3-compatible) on `localhost:9000`; see `deploy/README.md`.

- `GYOZA_TEST_S3_ENDPOINT=http://localhost:9000`: enables the S3 blob tests (`platform/blob/s3_test.go`; credentials default to the compose file's).

## Go service pattern

Follow `services/storageledger`:

- `ledger.go`: domain types, errors, the `Service` interface, and validation/paging helpers shared by every implementation.
- `memory.go`: in-memory fake. `postgres.go`: real implementation on sqlc-generated code in `internal/db`.
- `migrations/`: goose SQL migrations, embedded and applied by `Migrate` at startup. Each service owns one Postgres schema, and goose's version table lives inside it.
- `service_test.go`: one shared suite (`runServiceTests`) that every implementation runs (`memory_test.go`, `postgres_test.go`, `grpc_test.go`), so the fake and the remote client can't drift from the real thing.
- `grpc.go` / `grpc_server.go` / `grpc_client.go`: `GRPCServer` wraps any `Service`; `GRPCClient` implements `Service` over the wire and maps status codes back to the same sentinel errors. Contracts live in `contracts/proto`, generated Go in `contracts/gen/go` (committed). `grpc_test.go` runs the suite through a real client/server over `bufconn`.
- `OPTIMIZATIONS.md` (next to the README): performance findings with evidence, split into Done and Open (with "when to act"). Add an entry whenever a performance issue is found, fixed or deliberately deferred.
- Never edit a migration that has been applied; add a new one. (One deliberate pre-release exception: on 2026-10-10 the service was renamed storagecatalogue → storageledger, including its Postgres schema `catalogue` → `ledger`, by editing migrations and dropping the old schema from the empty dev databases. Once anything is deployed, renames need forward migrations.)
- Naming: the service is the "ledger" (`storageledger`, `cmd/storage-ledger`, `LedgerService`, schema `ledger`), never "catalogue"/"catalog".
- `cmd/` mains are wiring only: `main` calls `run() error` so defers run before `os.Exit`. Shutdown sequence: after `<-ctx.Done()` call `stop()` (a second Ctrl+C then kills the process), derive `context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)`, and stop things in reverse startup order. Blocking closes without a context (`pool.Close`, gRPC `GracefulStop`) go through `platform/shutdown.Within`, never a bare `defer`, so a stuck connection can't hang exit.
- Setup shared by several mains (reading a service's settings, building it, starting its background jobs) goes in `cmd/internal/wiring`, so the monolith and the standalone binary can't drift apart.
- Go packages have no hyphens or underscores; binary names under `cmd/` may.

### Services with real logic: `services/storagemanager`

The ledger is pure data, so its `Service` *is* its store. storagemanager has logic, so it splits:

- `Manager` (logic) depends on interfaces only: `storageledger.Service`, `Store` (DAM data, `assets.go`), `blob.Store` (S3, `platform/blob`), and `Authorizer` (`authz.go`). Tests wire memory fakes of all of them.
- The `Store` checks data shape only, never permissions. Shared validation/normalisation lives in `assets_rules.go`, so every Store implementation accepts and rejects the same input.
- Errors and limits go at the top of the file that defines the types they belong to: asset ones in `assets.go`, folder ones in `folders.go`, upload ones in `uploads.go`, `ErrForbidden` in `authz.go`; `blob.ErrNotFound` in `platform/blob/blob.go`.
- Package layout: a folder is a package, so split only along real boundaries (a small API, no dependency back, reusable), not by layer. Many files in one package, grouped by name prefix, is normal. Package names give context, so avoid stutter (`blob.Store`, not `blobs.Blobs`). Shared test suites other packages need go in an `xxxtest` package (`platform/blob/blobtest`).
- Callers use the `Service` interface (`service.go`), implemented by `*Manager` and `GRPCClient`, never `*Manager` itself. Background work (`Cleanup`/`RunCleanup`) stays off `Service`: it runs inside whichever binary hosts the Manager.
- When several sentinel errors share a gRPC code, the status carries one `google.rpc.ErrorInfo` per matching sentinel and the client rebuilds them (`wireErrors` in `grpc.go`); one table drives both directions.
- Progress and decisions: `services/storagemanager/README.md`; upcoming work in its `PLAN.md`.

Planned work lives in a `PLAN.md` next to each component's README (`services/usermanager`, `services/storagemanager`, `services/gateway`, `services/documentmanager`, `apps/web`); the plans link to each other where a decision spans components. READMEs record what is built and decided; PLAN.md records what is next.

### Testing conventions

- Every interface with several implementations gets one shared suite (`runServiceTests`, `blobtest.RunSuite`, `runStoreTests`) taking a constructor or harness; each implementation has a tiny `*_test.go` that runs it.
- Mutation-check a new suite: reintroduce realistic bugs into the implementation one at a time, confirm each is caught by a named test, then restore the file. A first pass found tests that passed while proving nothing.
- External test files (`package x_test`) in one folder share a namespace: prefix helper and case names (`testGetAssetMissing`, not `testGetMissing`) to avoid clashes.
- Stores keep microsecond timestamps, so two calls can share one; tests asserting "must not change `UpdatedAt`/`DeletedAt`" call `letTimePass()` between them.
- Compare whole sets of IDs for order-independent results; with UUIDv7 (storagemanager) order is exact and can be asserted.
- Test-only hooks into unexported state (e.g. `SetClock` to jump past the trash retention) go in `export_test.go` in the package itself (not `_test`): compiled only into tests, so the real API never exposes them.

## Languages

The previous system had two Rust repos (the engine and the document server, which is the engine's native client), a ReactJS frontend, Node.js middlewares, and Python FastAPI for all other backends. The new plan:

- **Rust**: `libs/engine`. `services/documentmanager` (formerly "document server") is Rust or Go, undecided: Go is viable via a C ABI crate (`engine-ffi`) over the engine's top-level API, which would make a true one-binary monolith possible.
- **Go**: `gateway`, `storage-ledger`, `storage-manager`, `user-manager`, `media-processor`, `platform/`. The user is using these to learn Go, so prefer idiomatic, standard-library-first Go and explain non-obvious idioms.
- **TypeScript/React**: `apps/web`
- **Python**: `services/creator-agent`, `libs/agent`. This can be deferred for the MVP by calling image-gen APIs directly from Go.

## Layout and the monolith/distributed goal

The repo must deploy either as a monolith or as separate services. Only same-language code can share a process, so in practice "monolith" means one Go binary, with the document manager (while it's Rust), the web app and the optional Python agent running alongside it. The layout keeps service logic separate from packaging:

- `services/<name>/`: domain logic. A Go service is a library package (no `main`) exposing a `Service` interface and a constructor, and owns its `migrations/`. If the document manager stays Rust, it will contain its own `crates/`.
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

- `document-manager` (`services/documentmanager`): the successor to Kyoso-server. It does server-side rendering, log-rolls document snapshots to S3 and load-balances replicas, with allocation tracked in DynamoDB. It was previously built on Axum; Dioxus and Leptos are under consideration. Planned crates: the server binary, a router/load-balancer binary, types, reusable data structures, and NATS and AWS SDK wrappers kept swappable.
- `gateway`: the single entry point from the frontend, merging the old REST and NATS→WebSocket middlewares. It handles SuperTokens auth (previously done in the middleware). REST and WS share one session layer.
- `storage-manager`: S3 get/put for uploads and assets (not documents). It calls `storage-ledger` over gRPC.
- `storage-ledger`: the PostgreSQL ledger of every S3 object. It links to user data for GDPR deletion.
- `user-manager`: users, orgs, permissions, projects and billing tables.
- `media-processor`: an ffmpeg wrapper.
- `creator-agent`: the agentic/GenAI service.

**Infrastructure**: AWS S3, PostgreSQL, ECS (CPU services), EC2-on-ECS (stateful CPU/GPU document-manager replicas), DynamoDB, NATS/JetStream, OpenTelemetry/DataDog.

## Design principles

- Use as few external technologies as possible, and build as little as possible for the MVP.
- Open questions: where gRPC should replace REST, how auth identity propagates to internal services, and whether authorization lives at the gateway or in each service.
