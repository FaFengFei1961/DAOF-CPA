# DAOF AI Hub local launcher example for PowerShell.
# Keep machine-specific paths in start.ps1, which is intentionally ignored.

$ErrorActionPreference = "Stop"

# Run from the project root even when the script is invoked from elsewhere.
Set-Location $PSScriptRoot

# SQLite uses github.com/mattn/go-sqlite3, so CGO must be enabled.
$env:CGO_ENABLED = "1"

# Configure a C compiler only if the caller has not already set one.
if (-not $env:CC) {
    $gccCandidates = @(
        "C:\TDM-GCC-64\bin\gcc.exe",
        "C:\msys64\ucrt64\bin\gcc.exe",
        "C:\msys64\mingw64\bin\gcc.exe"
    )
    foreach ($candidate in $gccCandidates) {
        if (Test-Path $candidate) {
            $env:CC = $candidate
            break
        }
    }
}

# Local runtime files. The application creates them on first startup if missing.
if (-not $env:DAOF_KEY_PATH) {
    $env:DAOF_KEY_PATH = "./data/daof.key"
}
if (-not $env:DAOF_DB_PATH) {
    $env:DAOF_DB_PATH = "./data/daofa-hub.db"
}

New-Item -ItemType Directory -Force -Path "./data" | Out-Null

Write-Host "Starting DAOF AI Hub"
Write-Host "  CGO_ENABLED   = $env:CGO_ENABLED"
Write-Host "  CC            = $env:CC"
Write-Host "  DAOF_KEY_PATH = $env:DAOF_KEY_PATH"
Write-Host "  DAOF_DB_PATH  = $env:DAOF_DB_PATH"
Write-Host ""

# main.go uses app.Static("/", "./ui/dist") — the frontend bundle must exist
# before launching the backend; otherwise the SPA returns 404 on every request.
# We refuse to start instead of auto-building so build failures don't get buried.
if (-not (Test-Path "ui/dist/index.html")) {
    Write-Host "ERROR: ui/dist/index.html missing — backend would serve 404." -ForegroundColor Red
    Write-Host ""
    Write-Host "Build the frontend first:" -ForegroundColor Cyan
    Write-Host "  cd ui; npm install; npm run build"
    Write-Host "Or run the full reset (includes build):" -ForegroundColor Cyan
    Write-Host "  .\reset.ps1 -NoConfirm"
    exit 1
}

go run main.go
