#!/usr/bin/env bash
# Run go vet on go-indexing-mcp.
# Requires zig cc for CGO with SQLite + tree-sitter.
set -euo pipefail

export CGO_ENABLED=1
export CC="zig cc"

go vet -tags sqlite_fts5 ./...
