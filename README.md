# Gyoza

A modular-monorepo Go graphic design platform.

Basically the v0 plan is get it running as a local docker container, but actually requires compose to host external services: rustfs (local S3 alike), postgres, NATS, and probably a local redis or dynamodb alike. Try to cover as much ground as possible with PostGres.

The Go services are divided into approximate microservices, but can be run initially as a monolith.

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
- `libs/` - domain libraries, TBD, looks like we need a rendering engine...
- `contracts/proto/` - gRPC/protobuf APIs, generated for Go
- `deploy/` - monolith AND distributed packaging
- `infra/aws/` - cloud resources. nothing here for now as everything we need will be dockered.

## Apps/Services

- `apps/web` - web app frontend ReactJS
- `document-manager` - document WAL rolling service (Rust? Go?)
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
