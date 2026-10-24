# simple-connect

轻量级 SSH/SFTP 连接管理 TUI：管理连接配置、保存凭据免密直连，并可在 SSH 会话中一键唤起内嵌 SFTP。

> **定位**：单连接管理——同一时间只维护一个活动 SSH/SFTP 会话。多连接并发、多窗口/多标签、会话拆分请交给终端复用器（tmux、zellij），本工具可直接运行在它们内部。

## 特性

- **连接管理**：增删改查、实时过滤、并发在线状态探测（30s 周期，`s` 手动刷新）
- **免密直连**：密码存入 age 加密密码本（默认与密钥文件同目录，可指定 U 盘路径），自建交互会话直接认证；私钥认证支持 ssh-agent 引导
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
>
> 首次启动会在配置目录生成加密密码本 `vault.age` 与密钥 `vault.key`。若密钥已移到 U 盘：`simple-ssh --key-file /mnt/f/vault.key`（Windows：`simple-ssh.exe --key-file E:\vault.key`），也可在 TUI 按 `p` 修改。

### 列表页

标题栏显示当前版本号（发布构建为 git tag，本地构建为 `dev (<commit>)`），展示在线状态、目标与认证方式。

| 按键 | 功能 |
|---|---|
| `↑`/`↓`、`Tab`/`Shift+Tab`、`PgUp`/`PgDn`、`Home`/`End` | 移动光标 |
| `Enter` | 连接（未保存凭据时二次确认） |
| `f` | 进入该主机的 SFTP |
| `a` / `e` | 新增 / 编辑连接 |
| `d` | 删除（`y` 确认） |
| `p` | 密码库设置（查看/修改密钥路径、重置密码库） |
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
| `Ctrl+D` | 编辑密码主机时清除已保存密码（输入新密码可取消） |
| `Esc` | 取消返回 |

### 密码库页（`p`）

查看/修改密钥文件路径，或重置密码库。

| 按键 | 功能 |
|---|---|
| `Enter` | 保存并验证密钥路径（密码本已存在时必须能成功解密才保存） |
| `Ctrl+R` | 重置密码库（删除当前密码本/密钥，生成新密钥对，需输入 `RESET` 确认） |
| `Esc` | 返回列表；启动时密钥不可用则退出 |

> 默认密钥与密码本同目录，列表页会提示「建议独立保管」。把 `vault.key` 移到 U 盘/非同步目录后，在此页修改路径（或启动时 `--key-file <path>`）。密钥丢失无法恢复，只能重置并重新输入服务器密码。

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

- **加密密码本**：密码写入 `vault.age`（age X25519 + AEAD），密钥文件 `vault.key` 默认与密码本同目录（0600）。列表页显示两者路径；密钥在配置目录内时提示「建议独立保管」。保存时保留上一份加密备份 `vault.age.bak`；实际删除密码或重置密码库时会移除该备份。
- **密钥路径**：TUI 按 `p` 可修改密钥路径（保存前会验证能解密当前密码本），也支持启动参数 `--key-file <path>` 一次性覆盖（不写回配置；长期使用请按 `p` 保存）。相对路径按配置目录解析，支持 `~`。
- **旧版本数据不迁移**：新版不读取旧版 `secrets.json` 或系统 keyring，也不会自动删除它们；旧文件不在 `vault.age` 管理范围内，如需保留/清理请自行处理。升级后原主机可能需要重新输入服务器密码。
- **威胁模型**：密钥与密码本同目录时，整目录备份/云同步会同时包含两者；加密主要防“只拿到密码本”、其他用户无权限、随手查看。要隔离备份/同步风险，请把 `vault.key` 移到 U 盘或非同步目录，再用 `p`/`--key-file` 指定路径。密钥文件丢失无法恢复，只能重置密码库并重新输入服务器密码。
- **认证顺序**：私钥认证主机走私钥 + ssh-agent；密码认证主机走 password + keyboard-interactive + ssh-agent。私钥认证不会再因历史残留密码回落到密码认证。
- **主机指纹**：首次连接展示 SHA256 指纹并等待确认（`y` 信任后写入 `~/.ssh/known_hosts` 再重连）；指纹不匹配或 `known_hosts` 解析失败一律拒绝，不静默降级
- **配置文件**：`hosts.json` / `config.json` / `vault.age` / `vault.key` 默认在用户配置目录（0600；Linux `~/.config`、macOS `~/Library/Application Support`、Windows `%AppData%`）；密钥路径可通过 `p`/`--key-file` 指向外部。错误日志在用户缓存目录下同名文件（0600）
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

- **多实例并发编辑配置**：可在 tmux/多页签并行运行多个实例，增删改经文件锁（`hosts.lock` / `vault.lock`）+ 写前重读 + 原子写安全合并；同一主机被多实例同时修改时为后写整体覆盖（不做字段级合并）
- **密钥丢失不可恢复**：`vault.key` 是解密密码本的唯一凭据；丢失后只能在「密码库」页重置（删除旧密码本、生成新密钥、重新输入服务器密码）。建议备份密钥到密码管理器/离线介质，或放到 U 盘并从配置目录移走。
- **同目录备份风险**：默认 `vault.key` 与 `vault.age` 同目录，整目录备份/同步会同时包含两者；需要隔离时请移动密钥并用 `p`/`--key-file` 指定。
- **keyboard-interactive OTP 限制**：仅当单个非回显提示时回填保存的密码；多提示（OTP/堡垒机二次验证）或回显提示一律中止，需改用密钥或手工输入
- **Windows detach 吞键**：Windows 下 stdin 阻塞读取，detach 后残留的读取 goroutine 在下次按键时自行退出，可能吞掉一键
- **SFTP 传输无断点续传**：单个传输失败即中止当前批次；不支持离页后台继续，也不支持再次进入恢复进度（离页即关闭连接）
- **传输取消非瞬时**：传输中确认退出后需等待在途读写收敛并清理临时文件，通常瞬间完成；底层阻塞时约 5s 兜底强制离开（该路径可能残留 `.part` 临时文件）
- **会话首屏残留（zsh）**：注入 cwd 钩子时 zsh 的 ZLE 会重绘出约 4 行文本（仅观感，功能不受影响），详见 [docs/ssh-compat.md](docs/ssh-compat.md)
