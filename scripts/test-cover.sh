#!/usr/bin/env bash
# Run go-indexing-mcp tests with coverage profiling.
# Generates coverage.out and an HTML report.
set -euo pipefail

export CGO_ENABLED=1
export CC="zig cc"

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
COVER_OUT="${ROOT}/coverage.out"
COVER_HTML="${ROOT}/coverage.html"

go test -count=1 -tags sqlite_fts5 -coverprofile="${COVER_OUT}" ./...

go tool cover -html="${COVER_OUT}" -o "${COVER_HTML}"

echo "Coverage data: ${COVER_OUT}"
echo "HTML report:   ${COVER_HTML}"
