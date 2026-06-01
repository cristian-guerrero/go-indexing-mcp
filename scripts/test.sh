#!/usr/bin/env bash
# Run go-indexing-mcp tests (no cache).
# Requires zig cc for CGO with SQLite + tree-sitter.
set -euo pipefail

export CGO_ENABLED=1
export CC="zig cc"

go test -count=1 -tags sqlite_fts5 ./...
