# Contracts

Single source of truth for service APIs.

- `proto/gyoza/<service>/v1/*.proto`
- Generated with `buf` for Go (now), Rust (prost/tonic) and TypeScript (later)
- Generated Go code is committed under `gen/go`, importable as `gyoza/contracts/gen/go/gyoza/<service>/v1`
- `buf lint` (STANDARD rules) runs before every generate

## Regenerate

```sh
go generate ./contracts

```

`buf`, `protoc-gen-go` and `protoc-gen-go-grpc` are Go tool dependencies (pinned in `go.mod`), so nothing to install. `buf` runs the generators via `go tool`, so `go` must be on PATH.

## Conventions

- Every RPC has its own `XRequest` / `XResponse` messages, so fields can be added later without breaking callers.
- Never renumber or reuse a field number; add new fields with new numbers. Check with `go tool buf breaking proto --against <previous version>`.
- A version bump (`v1` -> `v2`) is a new package alongside the old one, not an edit.
- Services: `storage/v1` (`StorageService`), `iam/v1` (`IAMService`; `AuthzService`, called by storage only), `mediaprocessor/v1` (`MediaProcessorService`, called by storage only).
- Calls made for a user carry `actor_id`, set by the gateway from the signed-in session; services trust it, so they must only be reachable inside the platform.
- When several errors share a status code, the status carries a `google.rpc.ErrorInfo` detail naming the exact one (storage: domain `storage.gyoza`, reasons like `ASSET_NOT_FOUND`; iam: `iam.gyoza`, e.g. `LAST_ADMIN`). Clients map it back to the domain error.

## Calling a running service by hand

Storage asks iam who may do what, so a user must exist first: sign them in as the gateway's sign-in hook would. Use IDs of your own; the test owners (`alice`, `ann`, `bob`, ...) must have no committed rows in the dev database.

```sh

go tool buf curl --schema contracts/proto --protocol grpc --http2-prior-knowledge \
 -d '{"identity":{"userId":"dev-user","email":"dev-user@example.com","emailVerified":true}}' \
 http://localhost:9090/gyoza.iam.v1.IAMService/SignIn

go tool buf curl --schema contracts/proto --protocol grpc --http2-prior-knowledge \
 -d '{"actorId":"dev-user","owner":{"kind":"OWNER_KIND_USER","id":"dev-user"}}' \
 http://localhost:9090/gyoza.storage.v1.StorageService/ListAssets

```

A full upload against the monolith (`go run ./cmd/gyoza`, which serves them on `localhost:9090`), from bash:

```sh

go tool buf curl --schema contracts/proto --protocol grpc --http2-prior-knowledge \
 -d '{"actorId":"dev-user","owner":{"kind":"OWNER_KIND_USER","id":"dev-user"},"name":"Logo","contentType":"image/png","sizeBytes":64}' \
 http://localhost:9090/gyoza.storage.v1.StorageService/BeginUpload

# PUT the file to the returned url with the returned Content-Type header, then:

go tool buf curl --schema contracts/proto --protocol grpc --http2-prior-knowledge \
 -d '{"actorId":"dev-user","uploadId":"<uploadId>"}' \
 http://localhost:9090/gyoza.storage.v1.StorageService/CompleteUpload

```

Windows PowerShell 5.1 strips the double quotes from JSON arguments to native programs; use bash (Git Bash) for these.
