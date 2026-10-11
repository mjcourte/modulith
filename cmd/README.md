# Entrypoints

Thin Go `main` packages that only wire services together; no domain logic.

- `cmd/gyoza/`: the monolith. Runs every Go service in one process with in-process clients (today: the ledger and storagemanager, the manager calling the ledger directly). Until the gateway exists it also serves both over gRPC on `GYOZA_GRPC_ADDR`, so they can be called by hand.
- `cmd/<service>/`: one binary per service for distributed deployment, using gRPC clients for the services it depends on.
  - `cmd/storage/`: the storage service: storagemanager and storageledger in one process (the ledger called in-process), serving both gRPC APIs on `GYOZA_GRPC_ADDR` (default `localhost:9091`). Merged 2026-10-11: the ledger's main writer and only purger is storagemanager, so a separate deployable only added a network hop per call.
- `cmd/internal/wiring/`: setup the binaries share (reading a service's settings, building it, starting its background jobs). `internal`, so only `cmd/` can import it.

Every binary that hosts storagemanager also runs its cleanup job (`RunCleanup`) and stops it before closing the database pool at shutdown.
