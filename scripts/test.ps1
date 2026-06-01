<#
.SYNOPSIS
    Run go-indexing-mcp tests (no cache).
    Uses zig cc as the C compiler for CGO-dependent packages.
#>

$ErrorActionPreference = "Stop"

$env:CGO_ENABLED = "1"
$env:CC = "zig cc"

go test -count=1 -tags sqlite_fts5 ./...

if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
