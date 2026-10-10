# Gyoza

A hypothetical monorepoization of Kyoso post-mortem.
Largely to be written in Go.

## Data Organization

- PostgreSQL with tables for customers, organizations, projects, permissions, billing
- one PostgreSQL schema per service, no cross-service table reads
- S3 for bulk storage
- PostgreSQL object ledger for S3

## Layout

- `apps/` - frontends
- `services/` - domain logic, one folder per service. Go services are packages with no `main`
- `cmd/` - Go entrypoints. `gyoza` = monolith, `<service>` = one binary each for distributed
- `platform/` - shared Go plumbing (config, pg, NATS, auth, telemetry, jobs)
- `libs/` - domain libraries
- `contracts/proto/` - gRPC/protobuf APIs, generated for Go, Rust, TS
- `deploy/` - monolith vs distributed packaging
- `infra/aws/` - cloud resources

## Libs

- `engine` - rendering engine, Kyoso-World (Rust)
- `agent` - agent skills, prompts, tools (Python)

## Apps/Services

- `apps/web` - web app frontend ReactJS
- `document-server` - document/rendering server (Rust or Go)
- `user-manager` - user db service (Go)
- `storage-manager` - s3 object service (Go)
- `storage-catalogue` - s3 object ledger service (Go)
- `gateway` - REST + WS middlewares merged, SuperTokens auth (Go)
- `creator-agent` - agent/genai service (Python, can defer for MVP)
- `media-processor` - media processing service (Go)
- `platform/jobs` - NATS + pgSQL job queue, a library not a service (Go)

## Technologies/Vendors

- AWS S3 - object storage
- AWS PostgreSQL - databases
- AWS ECS - basic CPU services
- AWS EC2 on ECS - quasi-stateful CPU/GPU servers
- AWS DynamoDB - Load allocation tables for document server replicas
- NATS/JetStream - message streams (embedded in monolith)
- SuperTokens - auth
- OpenTelemetry/DataDog (is there a free self hosted version?)

## Hard things but good to work towards

- Organize the repo s.t. it can be deployed either as a monolith OR distributed.
  - monolith = one Go binary + document-server + web. Services only talk through interfaces, in-process or gRPC
- Fewest possible external technologies ^^
- Like seriously how much of this can we NOT build and still have the MVP
- ~~do the middlewares need to be separated?~~ no, merged into `gateway`
- all backend was built on REST. Where can be benefit from gRPC
- auth at MW is new to me but lets do it
- Golang all services that were python except agent.
