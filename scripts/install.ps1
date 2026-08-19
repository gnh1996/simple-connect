# simple-connect install script (Windows)
# Installs as global command simple-ssh.exe.
#
# Usage (PowerShell):
#   powershell -ExecutionPolicy Bypass -File scripts\install.ps1                # build from source (needs Go)
#   powershell -ExecutionPolicy Bypass -File scripts\install.ps1 -Release       # download latest GitHub Release
#   powershell -ExecutionPolicy Bypass -File scripts\install.ps1 -Release v0.1.0
#   powershell -ExecutionPolicy Bypass -File scripts\install.ps1 -UsePrebuilt   # use prebuilt binary in dist/
#   powershell -ExecutionPolicy Bypass -File scripts\install.ps1 -InstallDir D:\tools\simple-ssh
#
# NOTE: This script is intentionally pure ASCII. Windows PowerShell 5.1 decodes
# .ps1 files without BOM using the system ANSI codepage, so non-ASCII text can
# corrupt parsing. Keep ALL output/comments ASCII.

param(
    [switch]$UsePrebuilt,
    [string]$Release = "",
    [string]$InstallDir = ""
)

$ErrorActionPreference = "Stop"

# Repo for -Release downloads.
$Repo = "gnh1996/simple-connect"

# Default install dir: %LOCALAPPDATA%\simple-connect
if ([string]::IsNullOrWhiteSpace($InstallDir)) {
    $InstallDir = Join-Path $env:LOCALAPPDATA "simple-connect"
}

# Locate project root (script lives under scripts/).
$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$ProjectRoot = Split-Path -Parent $ScriptDir
$OutExe = Join-Path $InstallDir "simple-ssh.exe"

# Create install dir.
New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null

function Get-RemoteFile {
    param([string]$Url, [string]$Out)
    [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
    Invoke-WebRequest -Uri $Url -OutFile $Out -UseBasicParsing
}

if ($Release) {
    # ---- Download prebuilt binary from GitHub Releases ----
    $Url = if ($Release -eq "latest") {
        "https://github.com/$Repo/releases/latest/download/simple-connect-windows-amd64.exe"
    } else {
        "https://github.com/$Repo/releases/download/$Release/simple-connect-windows-amd64.exe"
    }
    Write-Host "==> Downloading prebuilt version ($Release, windows/amd64)..."
    Write-Host "    $Url"
    Get-RemoteFile -Url $Url -Out $OutExe
    # Strip Mark-of-the-Web so SmartScreen does not block it.
    Unblock-File -Path $OutExe -ErrorAction SilentlyContinue
} elseif ($UsePrebuilt) {
    # ---- Use prebuilt binary from dist/ ----
    $Prebuilt = Join-Path $ProjectRoot "dist\simple-connect-windows-amd64.exe"
    if (-not (Test-Path $Prebuilt)) {
        Write-Host "ERROR: prebuilt binary not found: $Prebuilt" -ForegroundColor Red
        Write-Host "Build it first: go build -o dist\simple-connect-windows-amd64.exe ." -ForegroundColor Red
        exit 1
    }
    Write-Host "==> Using prebuilt binary ..."
    Copy-Item $Prebuilt $OutExe -Force
} else {
    # ---- Build from source ----
    $go = Get-Command go -ErrorAction SilentlyContinue
    if (-not $go) {
        Write-Host "ERROR: Go toolchain not found, cannot build from source." -ForegroundColor Red
        Write-Host ""
        Write-Host "Use the two-step install instead (avoids irm|iex, so Defender does not block it):" -ForegroundColor Yellow
        Write-Host '  Invoke-WebRequest -Uri "https://raw.githubusercontent.com/gnh1996/simple-connect/main/scripts/install.ps1" -OutFile "$env:TEMP\simple-connect-install.ps1"' -ForegroundColor Cyan
        Write-Host '  Unblock-File "$env:TEMP\simple-connect-install.ps1"' -ForegroundColor Cyan
        Write-Host '  powershell -ExecutionPolicy Bypass -File "$env:TEMP\simple-connect-install.ps1" -Release v0.1.0' -ForegroundColor Cyan
        exit 1
    }
    Write-Host "==> Building simple-ssh.exe from source ..."
    Push-Location $ProjectRoot
    try {
        & go build -o $OutExe .
        if ($LASTEXITCODE -ne 0) { throw "go build failed (exit code $LASTEXITCODE)" }
    } finally {
        Pop-Location
    }
}

# Add install dir to user PATH (persisted).
$userPath = [Environment]::GetEnvironmentVariable("Path", "User")
if ($userPath -notmatch [regex]::Escape($InstallDir)) {
    $newPath = if ([string]::IsNullOrWhiteSpace($userPath)) {
        $InstallDir
    } else {
        "$userPath;$InstallDir"
    }
    [Environment]::SetEnvironmentVariable("Path", $newPath, "User")
    Write-Host "==> Added $InstallDir to user PATH."
    Write-Host "    Open a new terminal and run simple-ssh; for this session run:"
    Write-Host "      `$env:Path = \"$InstallDir;`$env:Path\""
} else {
    Write-Host "==> $InstallDir already in user PATH."
}

Write-Host "==> Installed: $OutExe"
Write-Host "    Run simple-ssh to start."