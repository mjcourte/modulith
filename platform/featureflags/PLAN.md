# Feature flags: plan

Nothing is built yet. Flag rollouts scoped by environment, user, org and percentage of users,
behind the [OpenFeature](https://openfeature.dev) API so the backend can change later.

## Decisions

- **Call sites use OpenFeature only** (`github.com/open-feature/go-sdk`), never this package:
  `openfeature.NewDefaultClient().BooleanValue(ctx, "new-editor", false, evalCtx)`. This
  package provides a `Provider` (implements `openfeature.FeatureProvider`) that each `cmd/`
  main registers with `openfeature.SetProvider`.
- **A Postgres table drives the MVP.** Unleash (Postgres, mature UI), Flipt (Go, Git-native
  in v2) and flagd / GO Feature Flag (config files, OpenFeature-native) all have OpenFeature
  providers. Choosing one later means swapping the `SetProvider` call, not the call sites.
  Decision deferred until a flag UI or experiments are needed.
- **Evaluation context:** `targetingKey` = user ID; attributes `orgId` and `env`. The
  gateway builds it from the session; services pass it along with the request.
- **Its own schema, `featureflags`**, though it lives under `platform/`: it is infrastructure
  every service reads, not domain logic. Only this package touches the schema. (Alternative
  if the one-schema-per-service rule should stay strict: `services/featureflags`.)

## Next

1. **Schema.** `internal/db` laid out like `services/storage`: goose migration (embedded,
   version table in the schema), sqlc queries, `go generate`.

   ```sql
   CREATE TABLE featureflags.flags (
       key             text PRIMARY KEY,
       enabled         boolean NOT NULL DEFAULT false,
       environments    text[]  NOT NULL DEFAULT '{}',  -- empty = every environment
       user_ids        text[]  NOT NULL DEFAULT '{}',  -- always on for these users
       org_ids         text[]  NOT NULL DEFAULT '{}',  -- always on for these orgs
       rollout_percent int     NOT NULL DEFAULT 0 CHECK (rollout_percent BETWEEN 0 AND 100),
       description     text    NOT NULL DEFAULT '',
       updated_at      timestamptz NOT NULL DEFAULT now()
   );
   ```

2. **Evaluation**, in order, each result with an OpenFeature `Reason`:
   1. Unknown flag → default, `FLAG_NOT_FOUND` error.
   2. Disabled, or `env` not in `environments` → default, `DISABLED`.
   3. User or org on its allow-list → on, `TARGETING_MATCH`.
   4. `hash(key + ":" + targetingKey) % 100 < rollout_percent` → on, `SPLIT`. Hashing the
      key in means each flag samples a different slice of users; the result is stable per
      user, and raising the percentage only adds users. No `targetingKey` with a partial
      rollout → default, `TARGETING_KEY_MISSING`.
   5. Otherwise off, `DEFAULT`.

   Booleans only. String/number/object lookups return the default with `TYPE_MISMATCH` until
   a flag needs them (a `value jsonb` column then).

3. **Snapshot, not a query per lookup.** The provider evaluates against an in-memory map of
   all flags, reloaded every `MODULITH_FLAGS_REFRESH` (default 30s) as a background job.
   A failed reload keeps the last snapshot and logs; a failed *first* load fails startup.
   The source is an interface (`Postgres`, `Memory`) so tests need no database.
4. **Tests.** One shared suite over the provider with each source (memory always, Postgres
   with `MODULITH_TEST_DATABASE_URL`): env scoping, allow-lists, rollout stability and
   monotonicity (users on at 10% stay on at 20%; roughly the right share over many keys),
   missing targeting key, reload picking up changes. Mutation-check it.
5. **Wiring.** `featureflags.Open(ctx, pool)`: migrate, first load, start the refresher,
   return the provider. `cmd/modulith` first, then the standalone binaries; mains without a
   database (`cmd/mediaprocessor`) keep OpenFeature's no-op provider (defaults everywhere).
6. **Changing flags.** No API for the MVP: SQL, or a page in the dev-only admin UI
   (`apps/admin`), which is exempt from the one-schema rule.

## Later

- **Frontend flags.** The gateway evaluates the flags the web app needs for the session's
  user and serves them (e.g. `GET /api/flags`); the browser never sees targeting rules or
  talks to a flag server.
- **Faster propagation.** A NATS message on change instead of waiting for the next reload.
  When to act: 30s staleness hurts (a kill switch).
- **Telemetry.** OpenFeature hooks can tag the current span with `feature_flag.key` /
  `feature_flag.variant` (OTel semantic conventions).
- **Pick a real backend** (Unleash, Flipt, flagd) when flags need a UI for non-developers,
  audit history, scheduled rollouts or experiments.
