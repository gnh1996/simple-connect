# 安装指南

> **版本说明**：本指南与 README 描述 `main` 分支行为，自 `v0.2.1` 起的 Release 与之一致；安装更早版本（如 `v0.2.0`）时 SFTP 键位为旧的 `u` 路径上传 / `d` 下载。

## Linux / macOS

### 一键安装（推荐）

从 GitHub Releases 下载预编译二进制，安装为全局命令 `simple-ssh`：

```bash
# 最新 Release
curl -fsSL https://raw.githubusercontent.com/gnh1996/simple-connect/main/scripts/install.sh | sh -s -- --release

# 指定版本（可用版本见 https://github.com/gnh1996/simple-connect/releases）
curl -fsSL https://raw.githubusercontent.com/gnh1996/simple-connect/main/scripts/install.sh | sh -s -- --release v0.2.1

# 自定义安装目录（默认 ~/.local/bin）
INSTALL_DIR=/usr/local/bin curl -fsSL https://raw.githubusercontent.com/gnh1996/simple-connect/main/scripts/install.sh | sh -s -- --release
```

管道执行时必须使用 `sh -s --` 传递参数（`curl ... | sh --release` 无效）。

### 脚本参数

| 参数 | 说明 |
|---|---|
| 无参数 | 从源码构建（需 Go 工具链） |
| `--release` | 下载最新 Release 预编译二进制 |
| `--release vX.Y.Z` | 下载指定版本 |
| `--release=vX.Y.Z` | 同上（等号形式） |
| 环境变量 `INSTALL_DIR` | 自定义安装目录，默认 `~/.local/bin` |

## Windows（PowerShell）

> 注意：不要使用 `irm ... | iex` 单行命令——该模式会触发 Windows Defender 的 AMSI 启发式拦截（典型的下载执行特征），可能被直接隔离。请使用下面的两步式安装。安装脚本为纯 ASCII（英文输出），任何 PowerShell 版本（5.1 / 7）都能正确解析。

### 两步式安装（推荐）

```powershell
# ① 下载安装脚本到本地
Invoke-WebRequest -Uri https://raw.githubusercontent.com/gnh1996/simple-connect/main/scripts/install.ps1 `
  -OutFile "$env:TEMP\simple-connect-install.ps1"

# ② 移除下载来源标记并执行（安装到 %LOCALAPPDATA%\simple-connect，自动加入用户 PATH）
Unblock-File "$env:TEMP\simple-connect-install.ps1"
powershell -ExecutionPolicy Bypass -File "$env:TEMP\simple-connect-install.ps1" -Release latest
# 若你使用 PowerShell 7，最后一行可换为：
# pwsh -ExecutionPolicy Bypass -File "$env:TEMP\simple-connect-install.ps1" -Release latest
```

脚本会自动下载对应平台的预编译二进制并加入用户 PATH，安装后新开终端直接输入 `simple-ssh` 启动。

### 脚本参数

| 参数 | 说明 |
|---|---|
| 无参数 | 从源码构建（需 Go 工具链） |
| `-Release latest` | 下载最新 Release 预编译二进制 |
| `-Release vX.Y.Z` | 下载指定版本 |
| `-UsePrebuilt` | 使用 `dist/simple-connect-windows-amd64.exe` 本地预编译产物 |
| `-InstallDir D:\tools\simple-ssh` | 自定义安装目录，默认 `%LOCALAPPDATA%\simple-connect` |

### 手动安装（不执行任何脚本，最不易触发安全拦截）

```powershell
# 下载二进制
New-Item -ItemType Directory -Force "$env:LOCALAPPDATA\simple-connect" | Out-Null
Invoke-WebRequest -Uri https://github.com/gnh1996/simple-connect/releases/download/v0.2.1/simple-connect-windows-amd64.exe `
  -OutFile "$env:LOCALAPPDATA\simple-connect\simple-ssh.exe"
Unblock-File "$env:LOCALAPPDATA\simple-connect\simple-ssh.exe"

# 加入用户 PATH（持久化）
$dir = "$env:LOCALAPPDATA\simple-connect"
$p = [Environment]::GetEnvironmentVariable("Path", "User")
[Environment]::SetEnvironmentVariable("Path", "$p;$dir", "User")

# 新开终端输入 simple-ssh 启动；当前会话可先执行：
$env:Path += ";$dir"
```

### Defender 误报处理

simple-connect 是开源的未签名工具，首次下载运行时 Defender 可能提示"未知发布者"或误报隔离（常见于新发布的二进制，云信誉尚未建立）。处理方式：

- **误报申诉（推荐）**：Windows 安全中心 → 病毒和威胁防护 → 保护历史记录 → 找到被隔离的文件 → 操作 → 还原并选择"添加到排除项"，再按提示提交误报申诉，几周后云信誉恢复正常。
- **临时信任**：可在"病毒和威胁防护"→"排除项"中添加 `%LOCALAPPDATA%\simple-connect` 目录。
- 你随时可以对照源码（`go build`）自行构建，或对比 `SHA256SUMS` 确认二进制与官方一致。

## 升级 / 重复安装

安装脚本可重复执行，升级无需先卸载：新版本覆盖同一安装路径（Linux/macOS 为 `~/.local/bin/simple-ssh`，Windows 为 `%LOCALAPPDATA%\simple-connect\simple-ssh.exe`）。连接配置（`~/.config/simple-connect/hosts.json`）与 keyring 中的密码独立于二进制文件，升级不会丢失。

- **原子替换**：先写入安装目录下的临时文件，成功后一次 rename 覆盖；下载/构建失败会自动清理临时文件，现有安装保持原样，不会留下损坏的半截二进制。
- **Linux / macOS**：可替换正在运行的二进制（运行中的进程继续使用旧版本），重启 `simple-ssh` 后生效。
- **Windows**：运行中的 exe 被系统锁定，安装前请先退出 `simple-ssh`；脚本会检测运行中的进程并提前报错，替换失败时旧版本不受影响。
- **降级 / 回滚**：重装时指定旧版本 tag 即可，如 `--release v0.2.1` / `-Release v0.2.1`。
- **符号链接**：若 `simple-ssh` 是符号链接（如 `/usr/local/bin/simple-ssh -> ~/.local/bin/simple-ssh`），脚本替换的是链接指向的真实文件，链接本身保持不变。

## 校验文件完整性（可选）

每个 Release 附有 `SHA256SUMS` 文件，可校验下载的二进制未被篡改：

```powershell
Get-FileHash "$env:LOCALAPPDATA\simple-connect\simple-ssh.exe" -Algorithm SHA256
# 将输出与 https://github.com/gnh1996/simple-connect/releases/download/v0.2.1/SHA256SUMS 比对
```

## 源码构建

需要 Go 1.26+（`go.mod` 要求 `go 1.26.4`，发布工作流使用 Go 1.26）：

```bash
go build -o simple-ssh .
```

仓库内脚本也支持源码构建 / 使用本地预编译产物：

**Linux / macOS**

```bash
./scripts/install.sh                 # 源码构建（需 Go）
./scripts/install.sh --release       # 下载 GitHub 最新 Release
./scripts/install.sh --release v0.2.1
INSTALL_DIR=/usr/local/bin ./scripts/install.sh   # 自定义安装目录
```

**Windows**（PowerShell）

```powershell
powershell -ExecutionPolicy Bypass -File scripts\install.ps1                    # 源码构建（需 Go）
powershell -ExecutionPolicy Bypass -File scripts\install.ps1 -Release latest    # 下载 GitHub 最新 Release
powershell -ExecutionPolicy Bypass -File scripts\install.ps1 -Release v0.2.1    # 指定版本
powershell -ExecutionPolicy Bypass -File scripts\install.ps1 -UsePrebuilt       # 使用 dist/ 预编译二进制
```
