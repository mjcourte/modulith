#!/usr/bin/env bash
# Starts the dev admin UI (cmd/admin) on http://localhost:8081. It makes its
# changes through a running gyoza: start scripts/gyoza.sh first.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/env.sh"

export GYOZA_ADMIN=1
exec go run ./cmd/admin
