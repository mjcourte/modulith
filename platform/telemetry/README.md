# Telemetry

Traces, logs and metrics for every binary, sent with OpenTelemetry (OTLP/gRPC) to SigNoz
(`deploy/observability`). What's next: [PLAN.md](PLAN.md).

## Status (2026-10-14): every binary instrumented

`cmd/gyoza`, `storage`, `iam`, `documents`, `gateway` and `mediaprocessor` send traces, logs
and metrics; `cmd/admin` (a dev tool) doesn't. Checked end to end against SigNoz: spans and
logs from a binary on Windows, and a span from a Docker Desktop container.

| File | What |
| --- | --- |
| `telemetry.go` | `Setup` (tracer, logger and meter providers, propagator, runtime metrics), `LogHandler`, `Shutdown`, `ServerOption`/`DialOption` (gRPC) |
| `settings.go` | `Settings`, `SettingsFromEnv` (`GYOZA_OTLP_ENDPOINT`, `GYOZA_TRACE_HOT_RATE`, `GYOZA_DEBUG_ADDR`) |
| `sampler.go` | `Hot` and the hot-path sampler |
| `http.go` | `HTTPHandler`, `SetRoute`, `HTTPTransport` |
| `span.go` | `End(span, &err)`: end a span, failed if the function returned an error |
| `debug.go` | `StartDebug`: pprof on its own listener |

## Decisions

- **OpenTelemetry in the code, SigNoz only as the backend.** Nothing imports a SigNoz
  package; another backend (SigNoz Cloud, Jaeger, Tempo) is configuration. SigNoz keeps
  traces, logs and metrics in one place (ClickHouse): one extra technology for all three.
  Self-hosted, in production too.
- **Off unless configured.** No `GYOZA_OTLP_ENDPOINT`: `Setup` installs nothing, OTel's
  global providers stay no-ops, and `go run`, tests and CI need nothing. Settings are read
  through `platform/config`, not the SDK's `OTEL_*` variables, which would miss `.env`.
- **Transports first, logic second.** Most spans come from library instrumentation at the
  boundaries; hand-written spans only where no other span shows the work.
- **Context is the carrier**, in-process (the monolith) and through gRPC metadata and NATS
  headers (distributed), so the same code traces both deployments.
- **Profiling is span timings plus on-demand pprof.** SigNoz has no continuous profiling.
- **Naming:** `service.name` is the binary; each package has its own tracer and meter
  (`gyoza/services/documents/internal/wal`), so the monolith's spans say who made them.

## What's traced

| Where | How |
| --- | --- |
| gRPC, both ends | `ServerOption`/`DialOption` on every server and client connection in the mains: a call between services is one trace |
| Gateway HTTP | `HTTPHandler` around the whole handler (sign-in included), skipping `GET /api/ws`; spans named after the route (`GET /api/projects/{id}`, via `SetRoute`), never the raw path |
| WebSocket | a root span per message: `ws.join` (kept), `ws.edit` and `ws.recheck` (hot); none for `userstate` or `leave` |
| NATS | the trace rides in the message headers (`gnats.InjectTrace`/`TraceContext`; `Delta.Msg(ctx)`, snapshot pointers). documents applies a delta in `document.apply`, inside the publisher's trace; the gateway's feed makes a `ws.fanout` span per delta, **linked** to the publisher's (one message, many deliveries) |
| Postgres | otelpgx in `platform/postgres.Connect`; spans named after sqlc's `-- name: X`, with the SQL but never the values. Only inside a recording span: no orphan spans from migrations, none under an unsampled edit |
| S3 | otelaws on the SDK client (`storage/internal/blob`). Presigned URLs aren't traced: the browser uses them |
| Outgoing HTTP | `HTTPTransport` on the SuperTokens core client (account deletion) and documents' snapshot transfer |
| Hand-written | `document.load` (with `gyoza.deltas_replayed`), `document.save` (a trace of its own when the debounce saves); a root span per cleanup run (`storage.cleanup`, `iam.cleanup`); `iam.delete_project`/`_org`/`_user`, `iam.finish_clone` |

So a browser edit is one trace: `ws.edit` → NATS → `document.apply` → the snapshot save
(storage gRPC, S3) when it triggers one.

**Not traced, on purpose:** `CompleteUpload` and copying a project's files (their Postgres
and S3 spans already show the cost), image sniffing (a header: microseconds), asset-use
syncing (gRPC calls).

## High-volume paths

Document traffic would swamp SigNoz at one span or log line per message. Spans have no
levels; sampling is their equivalent.

- **Hot-path sampler:** a root span started with `telemetry.Hot` is kept at
  `GYOZA_TRACE_HOT_RATE` (default 0.01; `1` while debugging); every other root is kept.
  Parent-based: the decision travels in `traceparent`, so a trace is kept or dropped whole,
  in every service. `Hot` is an attribute given at span start, so there's no central list
  of hot names to keep in sync.
- **Per-message logs are `Debug`.** `LogHandler` applies `GYOZA_LOG_LEVEL` to the export too,
  so at `info` they never leave the process. Failures on hot paths stay `Warn`/`Error`.
- **Presence has no spans**, only a counter.
- **Volume is answered by metrics**, aggregated in the process.

## Logs

`LogHandler` sends every record to the text handler (stderr) and, at the same level, to
SigNoz through the `otelslog` bridge. A record written with a context (`slog.InfoContext`)
is attached to that context's span; one without isn't.

## Metrics

Exported once a minute. Free: Go runtime (memory, GC, goroutines) and request rate, errors
and duration from otelgrpc, otelhttp and otelpgx. Ours:

| Metric | What |
| --- | --- |
| `gyoza.ws.sockets`, `gyoza.ws.projects` | open WebSockets; projects with one joined |
| `gyoza.ws.messages` by `type` | browser messages, presence included. Unknown types count as `unknown`, so a client can't create series |
| `gyoza.documents.loaded` | documents open in the replica |
| `gyoza.documents.deltas` by `outcome` | consumer deltas: applied, dropped, retried |
| `gyoza.documents.consumer.pending` | the consumer's lag behind the stream (read from JetStream at each collection) |

## Profiling

`GYOZA_DEBUG_ADDR` (off when unset; keep it on localhost) serves `net/http/pprof` on its own
listener and mux: `go tool pprof http://localhost:6060/debug/pprof/profile` (CPU, 30s) or
`.../heap`. Span timings say which call is slow; pprof says why.

## Conventions

- **Attributes:** `gyoza.project_id`, `gyoza.org_id`, `gyoza.seq`, `enduser.id` (an opaque
  SuperTokens ID). **Never** emails, names, document payloads, presigned URLs or tokens.
- **Errors:** a span is marked failed only for failures (5xx, `codes.Internal`). Refusals
  (forbidden, not joined, a bad delta) are a `gyoza.refused` attribute, or every probe would
  look like an outage. `End(span, &err)` is for work whose errors are all failures (loads,
  saves, jobs).
- **Tests check contracts only:** a gRPC call is one trace, a delta's trace crosses NATS,
  route naming, the sampler's decisions, the WebSocket volume and error rules, and the
  metric series guard. No tests on individual span names beyond those.

## Deploy

Images need only environment: the SDK is in the static binary. Compose sets
`GYOZA_OTLP_ENDPOINT=http://host.docker.internal:4317` in every container: SigNoz runs in
WSL's Docker, so Docker Desktop's containers reach it through the host, where WSL forwards
the port. `GYOZA_CONTAINER_OTLP_ENDPOINT=` (empty) turns it off.

## Found on the way

- **NATS consumer tests and leader election.** A `DeliverNew` consumer on the 3-node cluster
  fixes where it starts only once its leader is elected, ~60ms after it exists (longer
  under load); the tests slept 200ms, so publishes in the gap were skipped when the whole
  suite ran. They now wait for the leader (`waitForConsumer`). Production is unaffected:
  documents' consumer is deliver-all, and the gateway's feed missing a delta in its first
  milliseconds is harmless (a join loads the document).
- **otelaws adds 1.4 MB** to a binary (it links the DynamoDB, SNS and SQS SDKs for their
  attributes). Kept; a small hand-written S3 middleware would avoid it if size matters.
