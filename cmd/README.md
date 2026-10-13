# Entrypoints

Thin Go `main` packages that only wire services together; no domain logic.

- `cmd/gyoza/`: the monolith. Runs every Go service in one process with in-process clients (today: iam, storage, documents, the gateway, and mediaprocessor when ffprobe is on PATH). The gateway serves browsers over HTTP on `GYOZA_GATEWAY_ADDR` (default `localhost:8080`); the services are also served over gRPC on `GYOZA_GRPC_ADDR`, so they can be called by hand.
- `cmd/<service>/`: one binary per service for distributed deployment, using gRPC clients for the services it depends on.
  - `cmd/storage/`: the storage service, serving `StorageService` on `GYOZA_GRPC_ADDR` (default `localhost:9091`). It asks iam who may do what at `GYOZA_IAM_ADDR` (default `localhost:9093`) and probes media through mediaprocessor at `GYOZA_MEDIAPROCESSOR_ADDR` (default `localhost:9092`).
  - `cmd/iam/`: users, orgs and projects behind `IAMService`, and storage's permission checks behind `AuthzService`, on `GYOZA_GRPC_ADDR` (default `localhost:9093`). Its account deletions also delete the sign-in account in the SuperTokens core (`GYOZA_SUPERTOKENS_URL`). Its deletions call storage at `GYOZA_STORAGE_ADDR` (default `localhost:9091`); clones also call documents at `GYOZA_DOCUMENTS_ADDR` (default `localhost:9094`).
  - `cmd/documents/`: live documents behind `DocumentsService` on `GYOZA_GRPC_ADDR` (default `localhost:9094`). It needs NATS (`GYOZA_NATS_URL`), keeps snapshots through storage at `GYOZA_STORAGE_ADDR` (default `localhost:9091`) and asks iam at `GYOZA_IAM_ADDR` (default `localhost:9093`). No database of its own.
  - `cmd/gateway/`: the browser-facing HTTP API (sign-in today) on `GYOZA_GATEWAY_ADDR` (default `localhost:8080`). It needs the SuperTokens core (`GYOZA_SUPERTOKENS_URL`) and the Google client (`GYOZA_GOOGLE_CLIENT_ID`/`_SECRET`), and records sign-ins in iam at `GYOZA_IAM_ADDR` (default `localhost:9093`). No database of its own.
  - `cmd/mediaprocessor/`: ffprobe behind `MediaProcessorService` on `GYOZA_GRPC_ADDR` (default `localhost:9092`). It reads no secrets and refuses to start without ffprobe on PATH.
- Setup a service's binaries share (reading its settings, building it, starting its background jobs) lives in the service package itself (e.g. `storage.SettingsFromEnv`, `storage.Open`, `storage.StartCleanup` in `services/storage/wiring.go`), so the monolith and the standalone binary can't drift apart.

Every binary hosting documents runs its NATS consumer (`documents.StartConsumer`) and closes documents at shutdown after the consumer stops and before NATS and storage go, so open documents are saved (their deltas stay in the stream if that fails).

Every binary that hosts storage also runs its background jobs (`storage.StartCleanup`, `storage.StartMediaProbe`), and every one hosting iam runs `iam.StartCleanup`; they stop before the database pool closes at shutdown.

storage and iam need each other (storage asks iam for permission; iam's deletions call storage), so the monolith builds them in this order: iam's store, the authorizer reading it, storage, then iam's Manager. Distributed, both connect lazily, so either may start first.

Container images for all of them come from the root `Dockerfile`, one target per binary (see `deploy/README.md`).
