# Sourced by the other scripts: run from the repo root, with go and docker on
# PATH. Settings come from .env (platform/config reads it), not from here.

cd "$(dirname "${BASH_SOURCE[0]}")/.." || exit 1

# Go and Docker Desktop's default Windows install locations; harmless elsewhere.
for dir in "/c/Program Files/Go/bin" "${LOCALAPPDATA:+$(cygpath -u "$LOCALAPPDATA" 2>/dev/null)/Programs/DockerDesktop/resources/bin}"; do
	[ -d "$dir" ] && PATH="$dir:$PATH"
done
export PATH

if ! command -v go >/dev/null; then
	echo "go not found on PATH" >&2
	exit 1
fi
if [ ! -f .env ]; then
	echo "no .env: copy .env.example to .env first" >&2
	exit 1
fi
