<#
.SYNOPSIS
    Run go-indexing-mcp tests with coverage profiling.
    Generates coverage.out and an HTML report.
#>

$ErrorActionPreference = "Stop"

$env:CGO_ENABLED = "1"
$env:CC = "zig cc"

$root = Split-Path -Parent $PSScriptRoot
$coverOut = Join-Path $root "coverage.out"
$coverHtml = Join-Path $root "coverage.html"

go test -count=1 -tags sqlite_fts5 -coverprofile="$coverOut" ./...

if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

go tool cover -html="$coverOut" -o "$coverHtml"

Write-Host "Coverage data: $coverOut" -ForegroundColor Green
Write-Host "HTML report:   $coverHtml" -ForegroundColor Green
