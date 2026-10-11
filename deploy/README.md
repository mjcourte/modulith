# Deploy

How the system is packaged and run. Cloud resources themselves live in `infra/`.

- `monolith/`: `gyoza` binary + document-server + PostgreSQL (+ MinIO for local S3)
- `distributed/`: one container/ECS task per service

## Local development

`monolith/compose.yaml` runs the dependencies (currently just Postgres 18.6) so
`go run ./cmd/gyoza` works on any machine with Docker. The Go binaries run on
the host, not in containers, for fast rebuilds.

```
docker compose -f deploy/monolith/compose.yaml up -d --wait   # start, wait until healthy
docker compose -f deploy/monolith/compose.yaml down           # stop, keep data
docker compose -f deploy/monolith/compose.yaml down -v        # stop, delete data
```

- Postgres listens on `127.0.0.1:5433` (not 5432, so it can run next to a locally installed Postgres), user/password/database `gyoza`.
- `.env.example` points at it; copy it to `.env`.
- Data lives in the `gyoza_postgres-data` volume and survives `down`. Migrations run when a binary starts, so a fresh volume needs no setup.
