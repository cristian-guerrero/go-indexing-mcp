#!/usr/bin/env bash
# Build go-indexing-mcp binary for Linux/macOS.
# Requires zig cc for CGO with SQLite + tree-sitter.
set -euo pipefail

VERSION="${GO_VERSION:-dev}"
EXT=""
case "$(uname -s)" in
  MINGW*|MSYS*|CYGWIN*) EXT=".exe" ;;
esac

export CGO_ENABLED=1
export CC="zig cc"

go build -tags sqlite_fts5 \
    -ldflags="-X github.com/cristian-guerrero/go-indexing-mcp/pkg/version.Version=${VERSION}" \
    -o "go-indexing-mcp${EXT}" .

echo "Built: go-indexing-mcp${EXT}"
