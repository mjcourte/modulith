# Deploy

How the system is packaged and run. Cloud resources themselves live in `infra/`.

- `monolith/`: `gyoza` binary + document-manager + PostgreSQL (+ RustFS for local S3)
- `distributed/`: one container/ECS task per service

## Local development

`monolith/compose.yaml` runs the dependencies (Postgres 18.6, and RustFS 1.0.0 as an S3-compatible server) so
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
