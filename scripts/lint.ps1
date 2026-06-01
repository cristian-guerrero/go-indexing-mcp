<#
.SYNOPSIS
    Run go vet on go-indexing-mcp.
    Uses zig cc as the C compiler for CGO-dependent packages.
#>

$ErrorActionPreference = "Stop"

$env:CGO_ENABLED = "1"
$env:CC = "zig cc"

go vet -tags sqlite_fts5 ./...

if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
