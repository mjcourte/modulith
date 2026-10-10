# Entrypoints

Thin Go `main` packages that only wire services together; no domain logic.

- `cmd/gyoza/`: the monolith. Runs every Go service in one process with in-process clients and embedded NATS.
- `cmd/<service>/`: one binary per service for distributed deployment, using gRPC clients.
