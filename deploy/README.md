# Deploy

How the system is packaged and run. Cloud resources themselves live in `infra/`.

- `monolith/`: the dependencies for local runs (Postgres, RustFS as S3, NATS), plus the `gyoza` binary itself in a container behind the `app` profile.
- `distributed/`: one container per service (today iam, storage and mediaprocessor), on the same dependencies.
- Images: the root `Dockerfile`, one target per binary (`gyoza`, `iam`, `storage`, `mediaprocessor`). Static Go binaries on Alpine, running as a non-root user and listening for gRPC on `:9090`. `gyoza` and `mediaprocessor` include ffprobe.

## Local development

`monolith/compose.yaml` runs the dependencies (Postgres 18.6, RustFS 1.0.0 as an S3-compatible server, a 3-node NATS cluster) so
`go run ./cmd/gyoza` works on any machine with Docker. The Go binaries run on
the host, not in containers, for fast rebuilds.

```sh
docker compose -f deploy/monolith/compose.yaml up -d --wait   # start, wait until healthy
docker compose -f deploy/monolith/compose.yaml down           # stop, keep data
docker compose -f deploy/monolith/compose.yaml down -v        # stop, delete data
```

- Postgres listens on `127.0.0.1:5433` (not 5432, so it can run next to a locally installed Postgres), user/password/database `gyoza`.
- `.env.example` points at both Postgres and S3; copy it to `.env`. Every binary hosting storage needs the S3 settings; with `GYOZA_S3_CREATE_BUCKET=true` it creates the `gyoza-dev` bucket at start-up.
- Data lives in the `gyoza_postgres-data` volume and survives `down`. Migrations run when a binary starts, so a fresh volume needs no setup.
- S3 (RustFS) listens on `127.0.0.1:9000`: access key `gyoza`, secret `gyoza-dev-secret`, path-style URLs. Data lives in the `gyoza_s3-data` volume. Set `GYOZA_TEST_S3_ENDPOINT=http://localhost:9000` to run the S3 blob tests (see `services/storage/internal/blob/README.md`).

## Running the binaries in containers

```sh
# The monolith, on localhost:9090:
docker compose -f deploy/monolith/compose.yaml --profile app up -d --build --wait

# Distributed: storage on localhost:9091, iam on localhost:9093, mediaprocessor
# reachable only by storage.
docker compose -f deploy/distributed/compose.yaml up -d --build --wait
```

- Inside containers, presigned URLs name `http://s3:9000`, which the host can't resolve. Services probe each other's URLs fine; to upload by hand from the host, run the binary on the host instead (or PUT from inside a container).
- The distributed file includes the monolith's dependencies under its own project name (`gyoza-distributed`), so it has its own data volumes. Both publish the same host ports, so run one at a time.
- mediaprocessor gets no environment at all: no database, no S3 credentials (see `services/mediaprocessor/PLAN.md`, "Isolating ffprobe").
- storage and iam call each other (permission checks one way, deletions the other); gRPC connects lazily, so neither waits for the other to start.
- Not containerised yet: the services not built yet (gateway, documentmanager, exporter, mcp).
