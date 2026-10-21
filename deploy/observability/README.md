# Observability: SigNoz

SigNoz stores the traces, metrics and logs the binaries send with OpenTelemetry (OTLP).
What the binaries send, and how, is in `platform/telemetry/README.md`.

- `casting.yaml`: the install, for SigNoz's Foundry installer (`foundryctl`), which renders it
  into Compose files under `pours/` (git ignores it, with `casting.yaml.lock`). Change
  `casting.yaml`, never `pours/`: each render overwrites it. SigNoz stopped shipping
  Compose files in v0.130.
- Images are pinned (SigNoz v0.145.0, collector v0.144.13, ClickHouse 25.12.5); Foundry
  defaults the collector to `:latest`. Upgrade SigNoz and the collector together.
- Patches move the UI to `127.0.0.1:3301` (8080 is the gateway's) and publish OTLP on
  `127.0.0.1:4317` (gRPC) and `127.0.0.1:4318` (HTTP). ClickHouse's 9000 (RustFS's port)
  isn't published.

## Running it

```sh
scripts/signoz.sh          # render and start, or apply a changed casting.yaml
scripts/signoz.sh status
scripts/signoz.sh logs ingester
scripts/signoz.sh down     # stop, keeping the data (named volumes)
```

UI at http://localhost:3301. Binaries on Windows send to `localhost:4317`/`4318`.

## Setup on Windows (once)

SigNoz runs on **Docker Engine inside WSL 2**, not Docker Desktop: SigNoz documents
ClickHouse Keeper crashing (exit 139) under Docker Desktop. Docker Desktop keeps running
the app's dependencies (`deploy/monolith`); the two engines are separate, so containers
in one can't join the other's networks, but both publish to Windows' `localhost`.

1. Docker Engine in the WSL distro (Ubuntu): `curl -fsSL https://get.docker.com | sudo sh`,
   then `sudo usermod -aG docker $USER` and `wsl --shutdown`. (The apt package called
   `docker` is an unrelated tray utility.)
2. foundryctl, into `~/.local/bin`: `curl -fsSL https://signoz.io/foundry.sh | bash`.
3. `scripts/signoz.sh`, then sign up at http://localhost:3301. **Nothing is ingested until
   the first account exists:** the collector gets its pipelines from the SigNoz server
   (OpAMP), which refuses until there is an organization ("cannot create agent without
   orgId"). The account lives in SigNoz's own Postgres volume, so this is once per volume.
4. WSL stops a distro soon after its last session closes, and SigNoz with it. Keep it
   running with `instanceIdleTimeout=-1` under `[general]` in `%UserProfile%\.wslconfig`
   (then `wsl --shutdown`), or a `wsl.exe -- sleep infinity` left running.

## Checking it

Send a span from Windows and look for service `modulith-smoke-test` in the UI:

```sh
now=$(date +%s)
curl -X POST http://localhost:4318/v1/traces -H 'Content-Type: application/json' -d '{"resourceSpans":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"modulith-smoke-test"}}]},"scopeSpans":[{"spans":[{"traceId":"5b8efff798038103d269b633813fc60c","spanId":"eee19b7ec3c1b174","name":"smoke-test","kind":1,"startTimeUnixNano":"'$now'000000000","endTimeUnixNano":"'$((now+1))'000000000"}]}]}]}'
```

A 200 with no span in the UI means the collector is still on its placeholder pipelines:
see step 3.
