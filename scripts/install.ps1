# simple-connect 安装脚本（Windows）
# 安装为全局命令 simple-ssh.exe。
#
# 用法（PowerShell）：
#   powershell -ExecutionPolicy Bypass -File scripts\install.ps1              # 源码构建（需 Go）
#   powershell -ExecutionPolicy Bypass -File scripts\install.ps1 -Release     # 下载 GitHub 最新 Release
#   powershell -ExecutionPolicy Bypass -File scripts\install.ps1 -Release v0.1.0  # 指定版本
#   powershell -ExecutionPolicy Bypass -File scripts\install.ps1 -UsePrebuilt # 使用 dist/ 预编译二进制
#   powershell -ExecutionPolicy Bypass -File scripts\install.ps1 -InstallDir D:\tools\simple-ssh

param(
    [switch]$UsePrebuilt,
    [string]$Release = "",
    [string]$InstallDir = ""
)

$ErrorActionPreference = "Stop"

# 仓库地址（用于 -Release 下载）
$Repo = "gnh1996/simple-connect"

# 默认安装目录：%LOCALAPPDATA%\simple-connect
if ([string]::IsNullOrWhiteSpace($InstallDir)) {
    $InstallDir = Join-Path $env:LOCALAPPDATA "simple-connect"
}

# 定位项目根目录（脚本位于项目根目录下 scripts/ 子目录）
$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$ProjectRoot = Split-Path -Parent $ScriptDir
$OutExe = Join-Path $InstallDir "simple-ssh.exe"

# 创建安装目录
New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null

function Get-RemoteFile {
    param([string]$Url, [string]$Out)
    [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
    Invoke-WebRequest -Uri $Url -OutFile $Out -UseBasicParsing
}

if ($Release) {
    # ---- 从 GitHub Releases 下载预编译二进制 ----
    $Url = if ($Release -eq "latest") {
        "https://github.com/$Repo/releases/latest/download/simple-connect-windows-amd64.exe"
    } else {
        "https://github.com/$Repo/releases/download/$Release/simple-connect-windows-amd64.exe"
    }
    Write-Host "==> 下载预编译版本（$Release，windows/amd64）..."
    Write-Host "    $Url"
    Get-RemoteFile -Url $Url -Out $OutExe
    # 移除下载来源标记（MOTW），避免 SmartScreen 拦截
    Unblock-File -Path $OutExe -ErrorAction SilentlyContinue
} elseif ($UsePrebuilt) {
    # ---- 使用 dist/ 预编译二进制 ----
    $Prebuilt = Join-Path $ProjectRoot "dist\simple-connect-windows-amd64.exe"
    if (-not (Test-Path $Prebuilt)) {
        Write-Host "错误：未找到预编译二进制 $Prebuilt 。" -ForegroundColor Red
        Write-Host "请先构建：go build -o dist\simple-connect-windows-amd64.exe ." -ForegroundColor Red
        exit 1
    }
    Write-Host "==> 使用预编译二进制 ..."
    Copy-Item $Prebuilt $OutExe -Force
} else {
    # ---- 源码构建 ----
    $go = Get-Command go -ErrorAction SilentlyContinue
    if (-not $go) {
        Write-Host "错误：未找到 go 工具链，无法源码构建。" -ForegroundColor Red
        Write-Host ""
        Write-Host "请改用两步式下载安装（避开 iex 单行命令，防 Defender 拦截）：" -ForegroundColor Yellow
        Write-Host '  Invoke-WebRequest -Uri "https://raw.githubusercontent.com/gnh1996/simple-connect/main/scripts/install.ps1" -OutFile "$env:TEMP\simple-connect-install.ps1"' -ForegroundColor Cyan
        Write-Host '  Unblock-File "$env:TEMP\simple-connect-install.ps1"' -ForegroundColor Cyan
        Write-Host '  powershell -ExecutionPolicy Bypass -File "$env:TEMP\simple-connect-install.ps1" -Release v0.1.0' -ForegroundColor Cyan
        exit 1
    }
    Write-Host "==> 从源码构建 simple-ssh.exe ..."
    Push-Location $ProjectRoot
    try {
        & go build -o $OutExe .
        if ($LASTEXITCODE -ne 0) { throw "go build 失败（退出码 $LASTEXITCODE）" }
    } finally {
        Pop-Location
    }
}

# 将安装目录加入用户 PATH（持久化）
$userPath = [Environment]::GetEnvironmentVariable("Path", "User")
if ($userPath -notmatch [regex]::Escape($InstallDir)) {
    $newPath = if ([string]::IsNullOrWhiteSpace($userPath)) {
        $InstallDir
    } else {
        "$userPath;$InstallDir"
    }
    [Environment]::SetEnvironmentVariable("Path", $newPath, "User")
    Write-Host "==> 已把 $InstallDir 加入用户 PATH。"
    Write-Host "    新开的终端即可输入 simple-ssh 启动程序；当前终端可执行："
    Write-Host "      `$env:Path = \"$InstallDir;`$env:Path\""
} else {
    Write-Host "==> $InstallDir 已在用户 PATH 中。"
}

Write-Host "==> 安装完成：$OutExe"
Write-Host "    现在可以输入 simple-ssh 启动程序。"