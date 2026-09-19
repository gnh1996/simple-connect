# simple-connect

轻量级 SSH/SFTP 连接管理 TUI：管理连接配置、保存凭据免密直连，并可在 SSH 会话中一键唤起内嵌 SFTP。

> **定位**：单连接管理——同一时间只维护一个活动 SSH/SFTP 会话。多连接并发、多窗口/多标签、会话拆分请交给终端复用器（tmux、zellij），本工具可直接运行在它们内部。

## 特性

- **连接管理**：增删改查、实时过滤、并发在线状态探测（30s 周期，`s` 手动刷新）
- **免密直连**：密码存入系统 keyring（无 keyring 时文件兜底 0600），自建交互会话直接认证；私钥认证支持 ssh-agent 引导
- **会话内唤起 SFTP**：`Ctrl+X f` 挂起会话（**不断开 SSH 连接与远程 shell**），SFTP 复用同一连接、免重新认证，退出后原样恢复；远程 cwd 经 OSC 133 自动定位
- **双栏 SFTP**：本地 | 远程，支持目录递归的上传/下载/删除、多选批量传输、路径传输与跳转（Tab 补全）；覆盖冲突确认、`.part` 原子写入、传输中退出确认
- **主机指纹校验**：首次连接展示 SHA256 指纹，确认后写入 `~/.ssh/known_hosts`，不静默信任
- **~/.ssh/config 融合**：`Host` 别名、`User`、`Port`、`IdentityFile`、`ProxyJump` 跳板链
- **跨平台**：Linux / macOS / Windows（自动隐藏控制台窗口）

## 快速开始

**Linux / macOS**

```bash
curl -fsSL https://raw.githubusercontent.com/gnh1996/simple-connect/main/scripts/install.sh | sh -s -- --release
```

**Windows**（PowerShell，两步式以避免 Defender 拦截）

```powershell
Invoke-WebRequest -Uri https://raw.githubusercontent.com/gnh1996/simple-connect/main/scripts/install.ps1 `
  -OutFile "$env:TEMP\simple-connect-install.ps1"
Unblock-File "$env:TEMP\simple-connect-install.ps1"
powershell -ExecutionPolicy Bypass -File "$env:TEMP\simple-connect-install.ps1" -Release latest
```

**源码构建**（需要 Go 1.26+）

```bash
go build -o simple-ssh .
```

> 指定版本、手动安装、`SHA256SUMS` 校验、Defender 误报处理等见 [docs/install.md](docs/install.md)；升级可直接重复执行安装命令（原子替换，失败不影响现有安装，详见该文档“升级 / 重复安装”）。

## 使用

> 本程序是交互式 TUI，必须在真实终端中运行。在 IDE（如 GoLand）里调试需在 Run Configuration 勾选 “Emulate terminal in output console”，否则启动时会直接给出提示。

### 列表页

标题栏显示当前版本号（发布构建为 git tag，本地构建为 `dev (<commit>)`），展示在线状态、目标与认证方式。

| 按键 | 功能 |
|---|---|
| `↑`/`↓`、`Tab`/`Shift+Tab`、`PgUp`/`PgDn`、`Home`/`End` | 移动光标 |
| `Enter` | 连接（未保存凭据时二次确认） |
| `f` | 进入该主机的 SFTP |
| `a` / `e` | 新增 / 编辑连接 |
| `d` | 删除（`y` 确认） |
| `s` | 刷新在线状态 |
| `/` | 过滤（`Enter` 保留过滤，`Esc` 清除） |
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
| `Enter` | 进入目录；文件：本地栏=上传、远程栏=下载；有选中项=批量传输；目标已存在时先检测覆盖并 `y/N` 确认 |
| `t` | 传输光标条目：本地栏=上传、远程栏=下载（文件+目录）；有选中项=批量传输 |
| `p` | 输入路径传输：本地栏=上传本地路径、远程栏=下载远程路径（`Tab` 补全循环、`Enter` 确认、`Esc` 取消） |
| `Space` | 多选 |
| `g` | 路径跳转（`Tab` 补全循环，`Enter` 跳转，`Esc` 取消） |
| `↑`/`↓`、`PgUp`/`PgDn`、`Home`/`End` | 移动光标 |
| `Backspace` | 上级目录 |
| `n` | 新建目录（按焦点栏，本地/远程） |
| `x` | 删除（单个/批量确认 `y/N`） |
| `r` | 刷新 |
| `q` / `Ctrl+C` | 返回（会话唤起场景返回原会话）；传输中先弹退出确认，确认后取消传输并清理临时文件（约 5s 兜底） |
| `Esc` | 取消确认（删除/覆盖/退出）或中断覆盖检测 / 清除多选 |

> 上传/下载（`Enter`/`t`/`p`）统一先做覆盖检测（冲突列出后 `y/N` 确认，`Esc` 可中断扫描），写入 `.part` 临时文件成功后改名，取消或失败时删除临时文件。

## 核心机制：会话 ⇄ SFTP 循环

```
列表 ──Enter──▶ SSH 会话 ──Ctrl+X f──▶ 挂起 ──▶ SFTP（复用同一连接）──q──▶ 恢复原会话 ──▶ ...
                  │                                                        ▲
                  └────────────── 会话正常结束，返回列表 ────────────────────┘
```

- **挂起而非断开**：`Ctrl+X f` 只挂起透传，不关闭 SSH 连接与远程 shell；挂起期间远程输出静音（丢弃），避免污染 SFTP 页
- **原样恢复**：`q` 后不清屏、不发任何字节，仅同步一次终端尺寸；仅首次进入时清屏并向远程 shell 注入 cwd 钩子
- **cwd 定位**：远程 bash/zsh 在每次提示符前经 OSC 133 上报目录，解析后从输出中剔除（不污染终端）；sh/dash 自动降级不跟踪

## 安全与兼容

- **免密直连**：密码经系统 keyring 存储（macOS Keychain / Linux Secret Service / Windows 凭据管理器）；无 keyring 时兜底 `secrets.json`（0600），列表页显示「密码明文存储」警告
- **认证顺序**：私钥 → 密码（password + keyboard-interactive）→ ssh-agent；k-i 仅在单个非回显提示时回填密码，多提示（OTP）一律中止
- **主机指纹**：首次连接展示 SHA256 指纹并等待确认（`y` 信任后写入 `~/.ssh/known_hosts` 再重连）；指纹不匹配或 `known_hosts` 解析失败一律拒绝，不静默降级
- **配置文件**：`hosts.json` 在用户配置目录（0600；Linux `~/.config`、macOS `~/Library/Application Support`、Windows `%AppData%`）；错误日志在用户缓存目录下同名文件（0600）
- **~/.ssh/config 融合**：`Enter` 连接时合并 `Host` 别名、`User`、`Port`、`IdentityFile`、`ProxyJump`（跳板复用目标保存的凭据）；列表页 `f` 进入 SFTP 使用独立原始凭据连接，避免意外行为

## 开发

```bash
go build ./...
go vet ./...
go test ./... -count=1
```

会话/传输层改动建议加跑竞态检测：`go test -race ./internal/session/ ./internal/sftp/ ./internal/tui/ -count=1`

目录结构、技术栈与测试约定见 [AGENTS.md](AGENTS.md)；会话与连接层与 OpenSSH 对齐的行为基线见 [docs/ssh-compat.md](docs/ssh-compat.md)。

## 已知限制

- **多实例并发编辑配置**：可在 tmux/多页签并行运行多个实例，增删改经文件锁（`hosts.lock` / `secrets.lock`）+ 写前重读 + 原子写安全合并；同一主机被多实例同时修改时为后写整体覆盖（不做字段级合并）
- **keyboard-interactive OTP 限制**：仅当单个非回显提示时回填保存的密码；多提示（OTP/堡垒机二次验证）或回显提示一律中止，需改用密钥或手工输入
- **Windows detach 吞键**：Windows 下 stdin 阻塞读取，detach 后残留的读取 goroutine 在下次按键时自行退出，可能吞掉一键
- **SFTP 传输无断点续传**：单个传输失败即中止当前批次；不支持离页后台继续，也不支持再次进入恢复进度（离页即关闭连接）
- **传输取消非瞬时**：传输中确认退出后需等待在途读写收敛并清理临时文件，通常瞬间完成；底层阻塞时约 5s 兜底强制离开（该路径可能残留 `.part` 临时文件）
- **会话首屏残留（zsh）**：注入 cwd 钩子时 zsh 的 ZLE 会重绘出约 4 行文本（仅观感，功能不受影响），详见 [docs/ssh-compat.md](docs/ssh-compat.md)
