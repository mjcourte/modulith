# Entrypoints

Thin Go `main` packages that only wire services together; no domain logic.

- `cmd/gyoza/`: the monolith. Runs every Go service in one process with in-process clients (today: the ledger and storagemanager, the manager calling the ledger directly). Until the gateway exists it also serves both over gRPC on `GYOZA_GRPC_ADDR`, so they can be called by hand.
- `cmd/<service>/`: one binary per service for distributed deployment, using gRPC clients for the services it depends on.
  - `cmd/storage-ledger/`: the ledger on `GYOZA_GRPC_ADDR` (default `localhost:9090`).
  - `cmd/storage-manager/`: storagemanager on `GYOZA_GRPC_ADDR` (default `localhost:9091`), calling the ledger at `GYOZA_LEDGER_ADDR`.
- `cmd/internal/wiring/`: setup the binaries share (reading a service's settings, building it, starting its background jobs). `internal`, so only `cmd/` can import it.

Every binary that hosts storagemanager also runs its cleanup job (`RunCleanup`) and stops it before closing the database pool at shutdown.
