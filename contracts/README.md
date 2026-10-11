# Contracts

Single source of truth for service APIs.

- `proto/gyoza/<service>/v1/*.proto`
- Generated with `buf` for Go (now), Rust (prost/tonic) and TypeScript (later)
- Generated Go code is committed under `gen/go`, importable as `gyoza/contracts/gen/go/gyoza/<service>/v1`
- `buf lint` (STANDARD rules) runs before every generate

## Regenerate

```
go generate ./contracts
```

`buf`, `protoc-gen-go` and `protoc-gen-go-grpc` are Go tool dependencies (pinned in `go.mod`), so nothing to install. `buf` runs the generators via `go tool`, so `go` must be on PATH.

## Conventions

- Every RPC has its own `XRequest` / `XResponse` messages, so fields can be added later without breaking callers.
- Never renumber or reuse a field number; add new fields with new numbers. Check with `go tool buf breaking proto --against <previous version>`.
- A version bump (`v1` -> `v2`) is a new package alongside the old one, not an edit.

## Calling a running service by hand

```
go tool buf curl --schema contracts/proto --protocol grpc --http2-prior-knowledge \
  -d '{"projectId":"p1"}' \
  http://localhost:9090/gyoza.storageledger.v1.LedgerService/ListObjects
```
