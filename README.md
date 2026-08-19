# simple-connect

轻量级 SSH/SFTP 连接管理 TUI：管理连接配置、保存凭据免密直连，并可在 SSH 会话中一键唤起内嵌 SFTP。多连接管理不在本工具职责内——那是终端复用器（tmux、zellij）的事。

## 特性

- **连接管理**：增删改查、实时过滤、并发在线/离线状态检测
- **免密直连**：密码存入系统 keyring（无 keyring 时文件兜底 0600），自建交互会话直接认证，无需二次输入
- **会话内唤起 SFTP**：会话中按 `Ctrl+X f` 挂起会话（**不关闭 SSH 连接与远程 shell**），SFTP 复用同一连接（免重新认证），退出后原样恢复同一会话
- **双栏 SFTP 浏览**：本地 | 远程双栏，Tab 切换焦点，支持目录递归的上传/下载/删除、多选批量传输、拖拽上传、路径跳转与 Tab 补全
- **cwd 追踪**：远程 shell 经 OSC 133 上报当前目录，会话唤起 SFTP 时自动定位到当前所在目录
- **~/.ssh/config 融合**：支持 `Host` 别名、`User`、`Port`、`IdentityFile`、`ProxyJump` 跳板链
- **ssh-agent 引导**：检测 agent 与密钥状态，未就绪时给出 `ssh-add` 提示与二次确认
- **跨平台**：Linux / macOS / Windows（Windows 下自动隐藏控制台窗口）

## 定位与边界

simple-connect 定位为**单连接管理**工具：

- 一个时间点只维护一个活动 SSH 会话 / SFTP 会话
- **不支持多连接并发管理、多窗口/多标签、会话拆分**——这些场景请交给终端复用器（tmux、zellij），simple-connect 可以直接跑在它们内部

## 安装

### 一键安装（推荐）

安装为全局命令 `simple-ssh`：

**Linux / macOS**

```bash
# 从 GitHub Releases 下载预编译二进制（无需 Go 工具链）
curl -fsSL https://raw.githubusercontent.com/gnh1996/simple-connect/main/scripts/install.sh | sh -- --release

# 或指定版本
curl -fsSL https://raw.githubusercontent.com/gnh1996/simple-connect/main/scripts/install.sh | sh -- --release v0.1.0

# 自定义安装目录
INSTALL_DIR=/usr/local/bin curl -fsSL https://raw.githubusercontent.com/gnh1996/simple-connect/main/scripts/install.sh | sh -- --release
```

**Windows**（PowerShell）

> 注意：不要使用 `irm ... | iex` 单行命令——该模式会触发 Windows Defender 的 AMSI 启发式拦截（典型的下载执行特征），可能被直接隔离。请使用下面的两步式安装。安装脚本已内置 UTF-8 BOM，Windows PowerShell 5.1（`powershell`）与 PowerShell 7（`pwsh`）均可正确执行。

```powershell
# ① 下载安装脚本到本地
Invoke-WebRequest -Uri https://raw.githubusercontent.com/gnh1996/simple-connect/main/scripts/install.ps1 `
  -OutFile "$env:TEMP\simple-connect-install.ps1"

# ② 移除下载来源标记并执行（安装到 %LOCALAPPDATA%\simple-connect，自动加入用户 PATH）
Unblock-File "$env:TEMP\simple-connect-install.ps1"
powershell -ExecutionPolicy Bypass -File "$env:TEMP\simple-connect-install.ps1" -Release v0.1.0
# 若你使用 PowerShell 7，最后一行可换为：
# pwsh -ExecutionPolicy Bypass -File "$env:TEMP\simple-connect-install.ps1" -Release v0.1.0
```

脚本会自动下载对应平台的预编译二进制并加入用户 PATH，安装后新开终端直接输入 `simple-ssh` 启动。

**手动安装（不执行任何脚本，最不易触发安全拦截）**

```powershell
# 下载二进制
New-Item -ItemType Directory -Force "$env:LOCALAPPDATA\simple-connect" | Out-Null
Invoke-WebRequest -Uri https://github.com/gnh1996/simple-connect/releases/download/v0.1.0/simple-connect-windows-amd64.exe `
  -OutFile "$env:LOCALAPPDATA\simple-connect\simple-ssh.exe"
Unblock-File "$env:LOCALAPPDATA\simple-connect\simple-ssh.exe"

# 加入用户 PATH（持久化）
$dir = "$env:LOCALAPPDATA\simple-connect"
$p = [Environment]::GetEnvironmentVariable("Path", "User")
[Environment]::SetEnvironmentVariable("Path", "$p;$dir", "User")

# 新开终端输入 simple-ssh 启动；当前会话可先执行：
$env:Path += ";$dir"
```

**校验文件完整性（可选）**

每个 Release 附有 `SHA256SUMS` 文件，可校验下载的二进制未被篡改：

```powershell
Get-FileHash "$env:LOCALAPPDATA\simple-connect\simple-ssh.exe" -Algorithm SHA256
# 将输出与 https://github.com/gnh1996/simple-connect/releases/download/v0.1.0/SHA256SUMS 比对
```

**Defender 误报怎么办**

simple-connect 是开源的未签名工具，首次下载运行时 Defender 可能提示"未知发布者"或误报隔离（常见于新发布的二进制，云信誉尚未建立）。处理方式：

- **误报申诉（推荐）**：Windows 安全中心 → 病毒和威胁防护 → 保护历史记录 → 找到被隔离的文件 → 操作 → 还原并选择"添加到排除项"，再按提示提交误报申诉，几周后云信誉恢复正常。
- **临时信任**：可在"病毒和威胁防护"→"排除项"中添加 `%LOCALAPPDATA%\simple-connect` 目录。
- 你随时可以对照源码（`go build`）自行构建，或对比 `SHA256SUMS` 确认二进制与官方一致。

### 源码构建

需要 Go 1.21+：

```bash
go build -o simple-ssh .
```

仓库内脚本也支持源码构建 / 使用本地预编译产物：

**Linux / macOS**

```bash
./scripts/install.sh                 # 源码构建（需 Go）
./scripts/install.sh --release       # 下载 GitHub 最新 Release
./scripts/install.sh --release v0.1.0
INSTALL_DIR=/usr/local/bin ./scripts/install.sh   # 自定义安装目录
```

**Windows**（PowerShell）

```powershell
powershell -ExecutionPolicy Bypass -File scripts\install.ps1                    # 源码构建（需 Go）
powershell -ExecutionPolicy Bypass -File scripts\install.ps1 -Release           # 下载 GitHub 最新 Release
powershell -ExecutionPolicy Bypass -File scripts\install.ps1 -Release v0.1.0    # 指定版本
powershell -ExecutionPolicy Bypass -File scripts\install.ps1 -UsePrebuilt       # 使用 dist/ 预编译二进制
```

## 使用

### 列表页

连接列表，展示在线状态、目标与认证方式；`/` 进入过滤。

| 按键 | 功能 |
|---|---|
| `↑`/`↓`、`Tab`/`Shift+Tab` | 移动光标 |
| `Enter` | 连接（未保存凭据时二次确认） |
| `f` | 进入该主机的 SFTP |
| `a` / `e` | 新增 / 编辑连接 |
| `d` | 删除（`y` 确认） |
| `s` | 刷新在线状态 |
| `/` | 过滤（`Esc` 清除） |
| `q` | 退出 |

### 表单页

添加 / 编辑连接。字段：名称、主机、端口、用户名、认证方式（密码/私钥）、密码、私钥路径、本地目录。

| 按键 | 功能 |
|---|---|
| `Enter` / `↓` / `Tab` | 下一个字段（最后一个字段 `Enter` 保存） |
| `↑` / `Shift+Tab` | 上一个字段 |
| `←` / `→` | 切换认证方式 |
| `Esc` | 取消返回 |

### SSH 会话

使用保存的凭据建立自建交互会话（PTY 透传，vim/tmux 表现与系统 ssh 一致）。

| 按键 | 功能 |
|---|---|
| `Ctrl+X f` | 挂起会话唤起 SFTP（连接与远程进程保持，退出后恢复原会话） |

### SFTP 页

双栏文件浏览：本地（左） | 远程（右）。

| 按键 | 功能 |
|---|---|
| `Tab` | 切换焦点栏 |
| `Enter` | 进入目录；文件：本地栏=上传、远程栏=下载；有选中项=批量传输 |
| `Space` | 多选 |
| `g` | 路径跳转（`Tab` 补全循环，`Enter` 跳转，`Esc` 取消） |
| `↑`/`↓`、`PgUp`/`PgDn`、`Home`/`End` | 移动光标 |
| `Backspace` | 上级目录 |
| `u` | 路径上传（可直接拖拽文件/目录到终端） |
| `n` | 新建目录 |
| `d` | 下载当前远程文件/目录（递归） |
| `x` | 删除（单个/批量确认 `y/N`） |
| `r` | 刷新 |
| `q` | 返回（会话唤起场景返回原会话） |
| `Esc` | 取消确认 / 清除多选 |

## 核心机制：会话 ⇄ SFTP 循环

```
列表 ──Enter──▶ SSH 会话 ──Ctrl+X f──▶ 挂起 ──▶ SFTP（复用同一连接）──q──▶ 恢复原会话 ──▶ ...
                  │                                                        ▲
                  └────────────── 会话正常结束，返回列表 ────────────────────┘
```

- **挂起而非断开**：`Ctrl+X f` 只挂起透传，不关闭 SSH 连接与远程 shell；挂起期间远程输出静音，避免污染 SFTP 页
- **连接复用**：SFTP 基于同一 SSH 连接开新通道，免重新认证；远程目录/进程/环境全部保持
- **原样恢复**：`q` 后不清屏、不发任何字节，仅同步一次终端尺寸，画面与挂起时一致
- **cwd 定位**：远程 bash/zsh 在每次提示符前经 OSC 133 上报目录，解析后从输出中剔除（不污染终端），唤起 SFTP 时定位到当前目录；sh/dash 自动降级不跟踪

## 凭据安全

- 密码经系统 keyring 存储（macOS Keychain / Linux Secret Service / Windows 凭据管理器），不落明文配置
- 无 keyring 时兜底为本地 `secrets.json`（权限 0600），列表页显示「密码明文存储」警告
- 主机配置存于 `~/.config/simple-connect/hosts.json`（0600）
- 私钥认证支持 ssh-agent；会话建立按 私钥 → 密码（password + keyboard-interactive）→ ssh-agent 依次尝试

## ~/.ssh/config 与跳板

`Enter` 连接时自动合并 `~/.ssh/config`：`Host` 别名、`User`、`Port`、`IdentityFile`、`ProxyJump` 链式跳板（跳板复用目标保存的凭据）。列表页直接 `f` 进入 SFTP 时使用独立原始凭据连接，避免意外行为。

## 开发

```
main.go                  # 入口：TUI ⇄ 自建 SSH 会话 ⇄ SFTP 页循环
internal/
  model/                 # 数据模型（Host、AuthType）
  store/                 # 持久化：JSON 配置 + Secrets 接口（keyring / 文件兜底）
  ssh/                   # SSH 客户端、状态探活、known_hosts、agent 检测、~/.ssh/config
  sftp/                  # SFTP 传输与浏览逻辑（Dial/List/Remove/Upload/Download）
  session/               # 自建交互会话：raw 模式 + PTY 透传 + resize + cwd 追踪
  exec/                  # 调用系统 ssh/sftp（降级后备）
  tui/                   # Bubble Tea 界面（root/list/form/sftp 页面）
  testutil/              # 测试辅助：内存 SSH/SFTP 服务器
```

技术栈：Bubble Tea v2、lipgloss、x/crypto/ssh、pkg/sftp、go-keyring。

### 验证

```bash
go build ./...
go vet ./...
go test ./... -count=1
```

会话/传输层改动建议加跑竞态检测：`go test -race ./internal/session/ ./internal/sftp/ ./internal/tui/ -count=1`

> 会话与连接层行为与 OpenSSH 对齐的完整基线见 [docs/ssh-compat.md](docs/ssh-compat.md)。

## 已知限制

- **多实例并发编辑配置**：可在 tmux/多页签并行运行多个实例，增删改经文件锁 + 写前重读 + 原子写安全合并（`go test -race` 覆盖）；同一主机被多实例同时修改时为后写整体覆盖（不做字段级合并）
- **首次连接静默信任主机指纹**：自动追加 `~/.ssh/known_hosts`（未实现 OpenSSH 的指纹确认 ask 模式）
- **keyboard-interactive 盲答**：对所有提示回填保存的密码；OTP/堡垒机二次验证场景可能认证失败，多次失败有锁号风险
- **Windows detach 吞键**：Windows 下 stdin 阻塞读取，detach 后残留的读取 goroutine 在下次按键时自行退出，可能吞掉一键
- **会话输入轮询**：unix 下输入走 10ms 非阻塞轮询，粘贴吞吐受限
- **SFTP 传输无断点续传**：单个传输失败即中止当前批次