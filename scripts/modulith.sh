#!/usr/bin/env bash
# Starts the monolith (cmd/modulith), first bringing up its dependencies
# (Postgres, RustFS, NATS) from deploy/monolith/compose.yaml unless --no-deps.
# Ctrl+C stops it.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/env.sh"

if [ "${1:-}" != "--no-deps" ]; then
	if command -v docker >/dev/null; then
		docker compose -f deploy/monolith/compose.yaml up -d --wait
	else
		echo "docker not found: skipping compose (pass --no-deps to silence)" >&2
	fi
fi

exec go run ./cmd/modulith
