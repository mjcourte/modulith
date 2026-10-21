# Platform

Shared Go plumbing used by every Go service: config, PostgreSQL, NATS (embedded in monolith mode, external when distributed), auth/session verification, telemetry, and in-process vs gRPC client wiring.

Domain logic does not belong here; it lives in `services/`.

## Packages

- `postgres`: `Connect(ctx, url, appName)` opens the shared pgx pool, sets the session settings every service relies on (`application_name`, `plan_cache_mode=force_custom_plan`), and pings so a bad URL fails at startup.

- `config`: reads environment variables (falling back to `.env` for local dev), collecting every problem so a misconfigured binary reports them all at once. Settings: `MODULITH_DATABASE_URL`, `MODULITH_LOG_LEVEL`, `MODULITH_SHUTDOWN_TIMEOUT` (see `.env.example`).
- `shutdown`: `Within(ctx, fn)` runs a blocking cleanup such as `pool.Close` but gives up at the shutdown deadline.
- `nats`: the NATS/JetStream client, stream definitions and subject names, plus the cluster configs (`nats-*.conf`, mounted by `deploy/monolith/compose.yaml`). See [nats/README.md](nats/README.md).
