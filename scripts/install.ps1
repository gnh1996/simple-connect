# simple-connect 安装脚本（Windows）
# 源码构建（需 Go 工具链）安装为全局命令 simple-ssh.exe；
# 无 Go 环境时可用 -UsePrebuilt 参数直接使用 dist/ 预编译二进制。
#
# 用法（PowerShell）：
#   powershell -ExecutionPolicy Bypass -File scripts\install.ps1
#   powershell -ExecutionPolicy Bypass -File scripts\install.ps1 -UsePrebuilt
#   powershell -ExecutionPolicy Bypass -File scripts\install.ps1 -InstallDir D:\tools\simple-ssh

param(
    [switch]$UsePrebuilt,
    [string]$InstallDir = ""
)

$ErrorActionPreference = "Stop"

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

if ($UsePrebuilt) {
    # 使用预编译二进制（dist/ 目录）
    $Prebuilt = Join-Path $ProjectRoot "dist\simple-connect-windows-amd64.exe"
    if (-not (Test-Path $Prebuilt)) {
        Write-Host "错误：未找到预编译二进制 $Prebuilt 。" -ForegroundColor Red
        Write-Host "请先构建：go build -o dist\simple-connect-windows-amd64.exe ." -ForegroundColor Red
        exit 1
    }
    Write-Host "==> 使用预编译二进制 ..."
    Copy-Item $Prebuilt $OutExe -Force
} else {
    # 检查 go 工具链
    $go = Get-Command go -ErrorAction SilentlyContinue
    if (-not $go) {
        Write-Host "错误：未找到 go 工具链。请安装 Go (https://go.dev/dl/) 后重试，" -ForegroundColor Red
        Write-Host "或改用 -UsePrebuilt 参数直接安装预编译版本。" -ForegroundColor Red
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