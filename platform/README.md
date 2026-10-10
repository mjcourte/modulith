# Platform

Shared Go plumbing used by every Go service: config, PostgreSQL, NATS (embedded in monolith mode, external when distributed), auth/session verification, telemetry, and in-process vs gRPC client wiring.

Domain logic does not belong here; it lives in `services/`.

## Packages

- `postgres`: `Connect(ctx, url, appName)` opens the shared pgx pool, sets the session settings every service relies on (`application_name`, `plan_cache_mode=force_custom_plan`), and pings so a bad URL fails at startup.

Config is plain environment variables read in each `cmd/` main for now (`GYOZA_DATABASE_URL`). Add a config package once there is more than a handful of settings.
