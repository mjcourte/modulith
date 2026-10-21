# Telemetry: plan

What's left. What's built and decided is in [README.md](README.md); SigNoz itself is in
`deploy/observability`.

## Next

1. **See an edit's trace from the browser.** Once the web app can open and edit a project,
   run the monolith with SigNoz and `MODULITH_TRACE_HOT_RATE=1`, and check one edit is one
   trace in the UI: `ws.edit` → `document.apply` → `document.save`. Only the pieces are
   tested (the NATS hop over the real cluster; the WebSocket's spans); nothing yet runs the
   whole path, gateway to documents, as one trace.
2. **Keep SigNoz running on this machine.** WSL stops the distro soon after its last session
   closes, and SigNoz with it. `instanceIdleTimeout=-1` under `[general]` in
   `%UserProfile%\.wslconfig` (then `wsl --shutdown`), or a keep-alive in
   `scripts/signoz.sh up` if WSL ignores the setting (some builds do).
3. **Logs without a context.** Many `slog.Error`/`Warn` calls take no context, so their
   records reach SigNoz attached to no trace. Move the ones on request and job paths to
   `slog.*Context` as they're touched.
4. **Resource attributes.** `service.version` (the build's git revision, from
   `debug.ReadBuildInfo`) and `deployment.environment` (dev, prod) on every signal, so
   traces can be compared across releases and environments.

## Later

- **Production collector.** On ECS, an OTel Collector next to the services (sidecar or one
  per cluster) forwards to SigNoz, so services don't need to know where it runs. Decide how
  self-hosted SigNoz runs there (its ClickHouse needs operating). Related: the security
  audit's open item on authenticating service-to-service traffic covers the collector's
  OTLP port too.
- **Tail sampling** (keep every failed or slow trace, sample the rest). Head sampling decides
  when a span starts, so it can't keep "only the edits that failed"; it needs our own
  collector in front of SigNoz's (whose config SigNoz manages over OpAMP). When to act:
  production traffic, or a failure the logs and error-rate metrics don't explain.
- **Browser tracing.** `apps/web` with the OTel web SDK, continuing traces into the gateway
  (its `traceparent` on REST calls and WebSocket messages). When the frontend has real flows.
- **Continuous profiling.** Pyroscope, or OTel profiles once SigNoz supports them. Until then
  `MODULITH_DEBUG_ADDR` and `go tool pprof` on demand.

## Known gaps (accepted)

- The SuperTokens SDK's own calls to its core aren't traced: its HTTP client isn't ours.
- `cmd/admin` isn't instrumented (a dev tool).
- otelaws' 1.4 MB per binary (README, "Found on the way").
