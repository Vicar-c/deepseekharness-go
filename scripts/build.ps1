# Build the Go rewrite with version metadata injected via ldflags.
# Usage: .\scripts\build.ps1 [-Version 0.1.0-dev] [-Output bin\dsh.exe]
[CmdletBinding()]
param(
    [string]$Version = "0.1.0-dev",
    [string]$Output = "bin\dsh.exe"
)

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
Push-Location $root
try {
    if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
        throw "go is not on PATH; open a new terminal (the toolchain is installed under %LOCALAPPDATA%\go\bin)"
    }
    $commit = "unknown"
    try {
        $commit = (& git rev-parse --short HEAD 2>$null | Select-Object -First 1)
    } catch {
        $commit = "unknown"
    }
    if ([string]::IsNullOrWhiteSpace($commit)) { $commit = "unknown" }
    $buildTime = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ssZ")
    $ldflags = @(
        "-s", "-w",
        "-X", "github.com/Vicar-c/deepseekharness-go/internal/version.Version=$Version",
        "-X", "github.com/Vicar-c/deepseekharness-go/internal/version.Commit=$commit",
        "-X", "github.com/Vicar-c/deepseekharness-go/internal/version.BuildTime=$buildTime"
    ) -join " "

    Write-Host "building $Output (version=$Version commit=$commit)"
    & go build -trimpath -ldflags $ldflags -o $Output ./cmd/dsh
    if ($LASTEXITCODE -ne 0) { throw "go build failed with exit code $LASTEXITCODE" }
    Write-Host "built $((Resolve-Path $Output).Path)"
} finally {
    Pop-Location
}
