# Entrypoints

Thin Go `main` packages that only wire services together; no domain logic.

- `cmd/gyoza/`: the monolith. Runs every Go service in one process with in-process clients (today: storage). Until the gateway exists it also serves them over gRPC on `GYOZA_GRPC_ADDR`, so they can be called by hand.
- `cmd/<service>/`: one binary per service for distributed deployment, using gRPC clients for the services it depends on.
  - `cmd/storage/`: the storage service, serving `StorageService` on `GYOZA_GRPC_ADDR` (default `localhost:9091`).
- Setup a service's binaries share (reading its settings, building it, starting its background jobs) lives in the service package itself (e.g. `storage.SettingsFromEnv`, `storage.Open`, `storage.StartCleanup` in `services/storage/wiring.go`), so the monolith and the standalone binary can't drift apart.

Every binary that hosts storage also runs its cleanup job (`storage.StartCleanup`) and stops it before closing the database pool at shutdown.
