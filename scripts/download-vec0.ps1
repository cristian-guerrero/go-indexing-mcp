<#
.SYNOPSIS
    Download sqlite-vec loadable extension for go-indexing-mcp.
    Downloads vec0.dll (Windows) or vec0.so (Linux) from GitHub releases.
#>
param(
    [Parameter(Mandatory=$false)]
    [ValidateSet("windows-x86_64", "linux-x86_64", "linux-aarch64", "macos-x86_64", "macos-aarch64")]
    [string]$Platform = ""
)

$ErrorActionPreference = "Stop"

$version = "0.1.9"
$repo = "asg017/sqlite-vec"
$baseUrl = "https://github.com/$repo/releases/download/v$version"

if ($Platform -eq "") {
    switch ([Environment]::OSVersion.Platform) {
        "Win32NT" { $Platform = "windows-x86_64" }
        "Unix" {
            $arch = (uname -m)
            $os = (uname -s)
            if ($os -eq "Darwin") {
                if ($arch -eq "arm64") { $Platform = "macos-aarch64" }
                else { $Platform = "macos-x86_64" }
            } else {
                if ($arch -eq "aarch64") { $Platform = "linux-aarch64" }
                else { $Platform = "linux-x86_64" }
            }
        }
        default { throw "Unsupported platform" }
    }
}

$libDir = "$env:USERPROFILE\.go-mcp\indexing\lib"
New-Item -ItemType Directory -Path $libDir -Force | Out-Null

$archiveName = "sqlite-vec-$version-loadable-$Platform.tar.gz"
$url = "$baseUrl/$archiveName"
$tmpArchive = "$env:TEMP\$archiveName"

Write-Host "Downloading sqlite-vec v$version ($Platform)..." -ForegroundColor Green
Write-Host "URL: $url" -ForegroundColor Gray

Invoke-WebRequest -Uri $url -OutFile $tmpArchive -UseBasicParsing

Write-Host "Extracting..." -ForegroundColor Gray
tar -xzf $tmpArchive -C $libDir

$extracted = Get-ChildItem $libDir -File
Write-Host "Extracted files:" -ForegroundColor Gray
$extracted | ForEach-Object { Write-Host "  $($_.Name) ($([math]::Round($_.Length/1KB, 1)) KB)" }

Remove-Item $tmpArchive -Force -ErrorAction SilentlyContinue

Write-Host "Done! sqlite-vec extension installed at $libDir" -ForegroundColor Green
