#!/usr/bin/env bash
# Starts the web app's dev server (apps/web) on http://localhost:3000. It proxies
# /api and /auth to the gateway: start scripts/modulith.sh first, with
# MODULITH_PUBLIC_URL=http://localhost:3000.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../apps/web"

# Node's default Windows install location; harmless elsewhere.
[ -d "/c/Program Files/nodejs" ] && PATH="/c/Program Files/nodejs:$PATH"
export PATH

if ! command -v npm >/dev/null; then
	echo "npm not found on PATH" >&2
	exit 1
fi
[ -d node_modules ] || npm install
exec npm run dev
