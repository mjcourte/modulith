# Gyoza

A modular-monorepo Go graphic design platform.

Basically the v0 plan is get it running as a local docker container, but actually requires compose to host external services: rustfs (local S3 alike), postgres, NATS, and probably a local redis or dynamodb alike.

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
- `libs/` - domain libraries, TBD
- `contracts/proto/` - gRPC/protobuf APIs, generated for Go, Rust, TS, whatever
- `deploy/` - monolith vs distributed packaging
- `infra/aws/` - cloud resources

## Libs

- `engine` - rendering engine, Kyoso-World (Rust)
- `agent` - agent skills, prompts, tools (Python)

## Apps/Services

- `apps/web` - web app frontend ReactJS
- `document-manager` - document/rendering service (Rust or Go)
- `iam` - identity and access management: users, orgs, projects, sharing (Go)
- `storage` - s3 object service (Go): `storagemanager` (uploads, DAM) + `storageledger` (ledger of every S3 object), one deployable
- `gateway` - REST + WS middlewares merged, SuperTokens auth (Go)
- `creator-agent` - agent/genai service (Python, can defer for MVP)
- `media-processor` - media processing service (Go)
- `platform/jobs` - NATS + pgSQL job queue, a library not a service (Go)

## Technologies/Vendors

- AWS S3 - object storage. Workalike: RustFS Docker for now.
- PostgreSQL - databases. Docker for now.
- NATS/JetStream - message streams. Docker.
- SuperTokens - auth. Docker.
- OpenTelemetry/DataDog (is there a free self hosted version?) TBD on tracing.

## Hard things but good to work towards

- Organize the repo s.t. it can be deployed either as a monolith OR distributed.
  - monolith = one Go binary + document-manager + web. Services only talk through interfaces, in-process or gRPC
- Fewest possible external technologies ^^
- Like seriously how much of this can we NOT build and still have the MVP
- ~~do the middlewares need to be separated?~~ no, merged into `gateway`
- all backend was built on REST. Where can be benefit from gRPC
- auth at MW is new to me but lets do it
- Golang all services that were python except agent.
