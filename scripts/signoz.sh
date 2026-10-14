#!/usr/bin/env bash
# Runs SigNoz (deploy/observability) on Docker Engine: inside WSL 2 when run
# from Windows (ClickHouse Keeper crashes under Docker Desktop), directly on
# Linux. Needs foundryctl in ~/.local/bin there (see deploy/observability/README.md).
#
#   scripts/signoz.sh          render casting.yaml and start (or apply changes)
#   scripts/signoz.sh status   the containers
#   scripts/signoz.sh logs     follow the logs (one service: logs ingester)
#   scripts/signoz.sh down     stop, keeping the data
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../deploy/observability"

# foundryctl: no usage analytics, no update checks.
foundry='~/.local/bin/foundryctl --no-ledger --no-updater'
compose='docker compose -f pours/deployment/compose.yaml'
case "${1:-up}" in
	up) cmd="$foundry cast -f casting.yaml" ;;
	status) cmd="$compose ps -a" ;;
	logs) cmd="$compose logs -f ${2:-}" ;;
	down) cmd="$compose down" ;;
	*)
		echo "usage: $0 [up|status|logs [service]|down]" >&2
		exit 2
		;;
esac

if command -v wsl.exe >/dev/null; then
	# Windows: run in the default WSL distro, in this folder. MSYS_NO_PATHCONV
	# stops Git Bash rewriting the Linux paths in cmd.
	MSYS_NO_PATHCONV=1 exec wsl.exe --cd "$(pwd -W 2>/dev/null || pwd)" -- bash -lc "$cmd"
fi
exec bash -c "$cmd"
