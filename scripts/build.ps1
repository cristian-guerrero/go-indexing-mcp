<#
.SYNOPSIS
    Build go-indexing-mcp binary for Windows.
    Uses zig cc as the C compiler (required for CGO with SQLite + tree-sitter).
#>

$ErrorActionPreference = "Stop"

$version = if ($env:GO_VERSION) { $env:GO_VERSION } else { "dev" }

$env:CGO_ENABLED = "1"
$env:CC = "zig cc"

go build -tags sqlite_fts5 `
    -ldflags="-X github.com/cristian-guerrero/go-indexing-mcp/pkg/version.Version=$version" `
    -o go-indexing-mcp.exe .

if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

Write-Host "Built: go-indexing-mcp.exe" -ForegroundColor Green
