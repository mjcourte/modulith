#!/usr/bin/env bash
# Regenerates every binding from engine/schema/*.fbs: the Go one
# (internal/engine/fbs, via go generate) and the Rust one
# (engine/core/src/generated). Both are committed, so run this after editing a
# .fbs and commit what changes. The frontend's TypeScript binding joins this
# script when the WASM build needs it (engine/PLAN.md stage 8).
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

# Go's and MSYS2's default Windows install locations; harmless elsewhere.
for dir in "/c/Program Files/Go/bin" "/c/msys64/ucrt64/bin"; do
	[ -d "$dir" ] && PATH="$dir:$PATH"
done
export PATH

for tool in go flatc; do
	if ! command -v "$tool" >/dev/null; then
		echo "$tool not found on PATH" >&2
		exit 1
	fi
done

echo "flatc: $(flatc --version)"

# Go: the //go:generate directives in internal/engine/mock_wire.go call flatc
# and gofmt themselves.
echo "==> go (internal/engine/fbs)"
go generate ./internal/engine

# Rust: --rust-module-root-file emits a mod.rs plus one file per type, using
# relative paths (use super::*), so the tree can live under src/generated
# rather than at the crate root. --gen-object-api matches the Go side's flag,
# giving owned SnapshotT/RegisterT/ValueT types on both sides. The output
# directory is wiped first so a type deleted from the schema doesn't leave a
# stale file behind.
echo "==> rust (engine/core/src/generated)"
out=engine/core/src/generated
rm -rf "$out"
mkdir -p "$out"
(cd engine/schema && flatc --rust --gen-object-api --rust-module-root-file \
	-o "../core/src/generated" common.fbs delta.fbs snapshot.fbs)

echo "done; review and commit what changed"
