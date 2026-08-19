# SSH 会话兼容对齐清单（OpenSSH 行为基线）

> 本清单定义 simple-connect 自建 SSH 会话必须对齐 OpenSSH 客户端的行为基线。
> **任何会话/连接层改动前先过一遍本清单；AI 生成或修改会话层代码时必须遵守。**
> 覆盖范围：`internal/ssh`、`internal/session`、`internal/testutil`。

## 1. 会话初始化序列（顺序不可乱）

`pty-req` → `env` → `shell`，与 OpenSSH 客户端一致：

1. **pty-req**：`RequestPty(term, rows, cols, modes)`，modes 必须是第 2 节完整集
2. **env**：仅透传 locale 白名单（第 3 节），逐个 `Setenv`
3. **shell**：`Shell()` 启动远程 shell

**为什么**：sshd 只有建立 PTY 后才会正确应用环境变量；env 必须发生在 shell 启动前，否则远程登录 shell 拿不到 locale/环境。

### 挂起 / 恢复（Ctrl+X f 唤起 SFTP）

- **detach 只挂起透传，不关闭 SSH 连接与远程 shell**：不调 `Session.Close()`、不关闭 `StdinPipe`（避免发 EOF），`oscTracker` 置静音丢弃挂起期间输出（继续消费防远端阻塞）。SSH 连接、shell 进程、工作目录全部保持原样。
- **恢复**（`Handle.Resume`）：**原样恢复**——重新 `MakeRaw`、重建输入源与 SIGWINCH 监听、发一次当前尺寸 `WindowChange`（尺寸通知无语义副作用）；**不清屏、不发任何字节**（画面保持 detach 时状态，用户按键后自然刷新）。仅**首次进入**会话时本地清屏一次。
- **首次进入注入 cwd 追踪钩子**：经 `StdinPipe` 发送一条 `export PROMPT_COMMAND=...` / zsh `precmd_functions` 拼接命令（见第 3 节），让 shell 在每次提示符前（空闲时刻）上报目录，绕开 sshd AcceptEnv 对 PROMPT_COMMAND 的拒绝。
- **`Wait()` 只调一次**：`StartInteractive` 启动一次 goroutine 收 `Session.Wait()` 结果，挂起/恢复循环经 channel `select` 复用；detach 挂起期间 goroutine 阻塞，会话真正结束时返回。
- **SFTP 复用同一 SSH 连接**（`sftp.NewClient` 开新 channel，不重新认证）；`Conn.Close` 只关 SFTP 通道，不影响会话。

## 2. TerminalModes 完整集（`internal/ssh/client.go` NewTerminalSession）

**开关类模式必须显式发送，共 40 项**。未发送的项保留服务器 tty 默认值（唯一不可控来源）：老系统/网络设备/堡垒机默认可能关闭关键项。

### 必须为 1

| Opcode | 模式 | 为什么 |
|---|---|---|
| 42 | `IUTF8` | line discipline 按 UTF-8 处理，退格按字符删除；缺失→按字节删、光标错位 |
| 53 | `ECHO` | 远程回显，缺失→输入不可见 |
| 54 | `ECHOE` | 退格回显刷新 |
| 55 | `ECHOK` | 行杀回显 |
| 61 | `ECHOKE` | 行杀优化回显 |
| 60 | `ECHOCTL` | 控制字符显示为 ^X |
| 51 | `ICANON` | 行缓冲编辑（vim 依赖原始输入时会自行关闭，此处与 OpenSSH 对齐） |
| 59 | `IEXTEN` | 扩展编辑功能 |
| 50 | `ISIG` | 中断/挂起信号（Ctrl+C/Z 生效） |
| 38 | `IXON` | 输出流控（Ctrl+S/Q） |
| 41 | `IMAXBEL` | 行满响铃而非丢弃输入 |
| 36 | `ICRNL` | 回车→换行（本地 Enter 发 \r，链路关键） |
| 70 | `OPOST` | 输出后处理（\n→\r\n 由远程 tty 完成，本地 raw 不再负责） |
| 72 | `ONLCR` | 换行输出转回车换行 |
| 91 | `CS8` | 8-bit clean，UTF-8 必需 |
| 128/129 | `ISPEED/OSPEED` | 38400，与 OpenSSH 对齐 |

### 必须显式发 0（防止服务器默认开启造成意外行为）

`ECHONL`(56)、`IXOFF`(40，输入流控，开启后 Ctrl+S 挂起输入)、`IXANY`(39)、`INLCR`(34)、`IGNCR`(35)、`IGNPAR`(30)、`PARMRK`(31)、`INPCK`(32)、`ISTRIP`(33)、`IUCLC`(37)、`OCRNL`(73)、`ONOCR`(74)、`ONLRET`(75)、`OLCUC`(71)、`CS7`(90)、`PARENB`(92)、`PARODD`(93)、`NOFLSH`(57)、`TOSTOP`(58)、`XCASE`(52)、`PENDIN`(62)

### 控制字符类：不发送

`VINTR`/`VQUIT`/`VERASE`/`VKILL`/`VEOF`/`VSUSP` 等**禁止硬编码**：不同系统控制字值不同（如 VERASE 在多数系统为 127，但非全部），发送硬编码值会覆盖服务器特殊配置。保留服务器默认。

## 3. env 透传策略（`internal/session/session.go` sessionEnv）

**白名单（对齐 OpenSSH 默认 `SendEnv LANG LC_*`）**：

```
LANG LC_ALL LC_CTYPE LC_MESSAGES LC_COLLATE LC_TIME
LC_NUMERIC LC_MONETARY LC_PAPER LC_NAME LC_ADDRESS
LC_TELEPHONE LC_MEASUREMENT LC_IDENTIFICATION
```

规则：
- **仅透传非空值**（空值会覆盖服务器已有环境，破坏性）
- **禁止透传 TERM**：本机 TERM（尤其 tmux-256color 等复合值）不代表远程能力，会话内 TERM 由 `os.Getenv("TERM")` 单独处理（有 xterm-256color fallback）
- **禁止伪造 SSH_TTY/SSH_CLIENT/SSH_CONNECTION**：这些是 sshd 服务器设置的变量，客户端 env 请求无法（也不应）伪造
- **禁止全量透传环境**：原始环境含路径、代理、会话特定变量，泄露语义到远程
- **cwd 追踪（会话目录上报，OSC 133;cwd）**：双层机制，见下

### cwd 追踪（`internal/session/session.go`）

目标：让远程 shell 在**每次提示符前（空闲时刻）**输出 `ESC ]133;cwd=<PWD> BEL`，`oscTracker` 解析（**解析后将该 OSC 序列从输出中剔除**，对齐 Tabby，不污染终端），detach 时作为 SFTP 页初始定位目录。**注意：sshd 默认 `AcceptEnv` 仅接受 `LANG LC_*`，`PROMPT_COMMAND` 的 env 请求会被服务器静默拒绝**，因此以 shell 内注入为主：

1. **shell 内注入（主路径，绕开 AcceptEnv）**：首次进入会话时经 `StdinPipe` 发送一行钩子命令（`cwdHookCommand`）：
   - **bash**：定义 `_sc_cwd` 上报函数（检测 `$TMUX`，tmux 内用 passthrough 序列 `\ePtmux;\e\e]133;cwd=%s\007\e\\`），`export PROMPT_COMMAND="_sc_cwd; ${PROMPT_COMMAND}"`——**追加保留用户已有钩子**（powerline 等）；
   - **zsh**：`eval '_sc_cwd(){ ...; }; precmd_functions+=(_sc_cwd);'`——`precmd_functions` **追加不覆盖**（兼容 oh-my-zsh 等）；zsh 专属语法经 `eval` 包裹，避免 POSIX sh 解析报错；
   - **sh/dash**：`$BASH_VERSION`/`$ZSH_VERSION` 均未定义，两分支短路静默（POSIX 无提示符前钩子机制，降级不跟踪）。
   - 仅发送一次（首次进入）；SFTP 恢复时不发（原样恢复）。**回显隐藏靠 PTY 层 ECHO=0**（见 `ssh.Client.NewTerminalSession`）：pty-req 时即关闭 ECHO，注入命令从第一条起**完全不回显、无任何引导行残留**；命令末尾 `stty echo` 恢复交互回显，随后 `clear` 清除注入命令处理时产生的空行（bash 在 ECHO=0 下处理输入行会输出 `\x1b[?2004l\r\r\n` 多一个换行），使提示符干净出现在屏幕顶部（与 run() 首次清屏行为一致，横幅本就不保留）。命令本体**不含清行/清屏控制序列**（会干扰 bash readline 导致光标错位/输入不可见）；残余副作用：history 记录一条命令。
2. **env 注入兜底（宽松服务器场景）**：`Setenv("PROMPT_COMMAND", oscPromptCommand)` 仍保留——若服务器 `AcceptEnv` 恰好允许，直接生效且不产生 history 副作用；被拒绝则忽略错误（无阻塞）。

**为什么**：不传 → 远程 shell 落到 C/POSIX locale：
- readline 按字节处理输入，命令行重绘（redisplay）光标位置错乱 → **输入时跳行首**
- `wcwidth` 将 CJK 按宽度 1 计算，终端实际渲染宽度 2 → **ls/vim 对齐错乱**
- 大量程序（git status、grep --color 等）行为退化

**现实约束**：服务器 `sshd_config` 默认 `AcceptEnv LANG LC_*`；若关闭，`Setenv` 返回错误——**忽略错误继续**（不阻塞连接，与 OpenSSH 一致）。

## 4. known_hosts 策略（`internal/ssh/client.go` hostKeyCallback）

- 连接前解析 `~/.ssh/known_hosts`（knownhosts.New）；**文件不存在时先创建空文件**（否则 New 对不存在文件报错，而空 known_hosts 才能触发首次连接分支）
- 匹配成功 → 放行；不匹配（KeyError 且 Want 非空）→ 报错拒绝，提示检查 known_hosts
- 首次连接（Want 为空）→ 返回 `UnknownHostKeyError`（含 SHA256 指纹与公钥），由调用方展示并征得用户确认后 `TrustHostKey` 追加并重连（**对齐 OpenSSH ask 模式，不再静默信任**）：
  - main 会话路径（`startSSH`）：TUI 已退出、终端恢复非 raw，直接终端打印指纹提示 y/N，确认后信任并重连
  - TUI SFTP 列表页路径（`sftpModel.pendingKey`）：页面进入确认态展示指纹，y/Enter 信任并重连，Esc/n/q 取消
- 追加前确保 `~/.ssh` 存在（0700）
- **`knownhosts.New` 失败直接显式报错**，禁止静默降级 `InsecureIgnoreHostKey`——静默关闭校验是任何安全组件的禁忌
- 注：knownhosts 数据库在**创建回调时一次性读入文件**，信任追加后必须重新连接（新建回调）才生效

## 5. window-change 时序（`internal/session/resize_*.go`）

- 启动时 `term.GetSize` 一次作为 PTY 初始尺寸
- unix：`SIGWINCH` → `WindowChange(rows, cols)`；Windows：500ms 轮询尺寸变更
- 已知差距（可接受）：OpenSSH 会发送两次初始 window-change（连接后立即 + 稍后），本实现只随 pty-req 发一次。对现代服务器无感知差异

### 尺寸换算（易错，务必遵守）

- **`term.GetSize` 返回 `(width, height)`**，而 `RequestPty` / `WindowChange` 的参数语义是 `(rows, cols)`——**二者顺序相反**。
- 一律经 `resolveTermSize(width, height) (rows, cols)` 换算（`internal/session/session.go`），禁止直接把 GetSize 返回值当 rows/cols 使用。
- 尺寸非法（≤0）时回退 24×80。

**踩坑史**：曾把 `term.GetSize` 的 `(width, height)` 直接当 `(rows, cols)` 使用，导致远程 PTY 行列颠倒（如 30×120 被请求成 120×30），远程按错误列宽换行，`ls` 输出后光标乱跑、退格重绘错乱；系统 ssh 用正确尺寸故表现正常。改动会话初始化逻辑时必须经 `TestResolveTermSize`（第 8 节）验证。

## 6. 认证行为（`internal/ssh/client.go` authMethods）

尝试顺序：**私钥 → 密码（password + keyboard-interactive 同密码）→ ssh-agent**，全部失败才报错。

- **k-i 盲答限制**：`passwordAnswer` 仅当「单个提示且不回显输入」时才回填密码（未提供回显信息时默认视为不回显）；多提示（OTP/堡垒机二次验证）或回显输入提示一律中止，避免用密码应答验证码提示导致多次失败锁账号。中止时该认证方式失败，回落到其余认证方法（如 agent）
- **跳板机复用目标密码**：`dialWithJumps` 假设跳板与目标凭据相同（文档化假设）。凭据不同时认证失败，报错信息已指明跳板环节

## 7. 输入链路（`internal/session/`）

- 热键 Ctrl+X f：前缀状态机 `detachScanner`，正确处理跨 Read 边界、`\x18x`、`\x18\x18`；detach 后字节不转发。**detach 为挂起而非中断**：不关闭会话/输入管道，输出静音（见第 1 节挂起/恢复）
- **pollInput**：unix 下基于 poll(2) + 自管道（self-pipe）**阻塞读**——同时监听 stdin fd 与停止管道读端，detach 时向自管道写一字节唤醒阻塞的 poll 并返回 EOF，干净退出；无 10ms 忙等轮询延迟、无粘贴吞吐瓶颈（原 ≈51KB/s）。`syscall.Read` 在 EOF 时返回 `(0, nil)`，必须显式转为 `io.EOF`（否则 pumpInput 空读死循环）。自管道创建失败（极罕见）退回忙等轮询兜底。Windows 维持阻塞读（detach 后残留读取 goroutine，下次按键自行退出，可能吞一键）
- Enter 键：本地 raw 后 `\r`(0x0D) 直传远程 → 依赖远程 `ICRNL=1` 转 `\n`（第 2 节）

## 8. 回归测试对照（`internal/testutil`）

内存 SSH 服务器可记录：窗口尺寸、env 请求、pty-req 模式段。

**修改会话初始化逻辑必须跑通：**

```bash
go test ./internal/session/ -count=1 -run 'TestSession' -v
go test -race ./internal/session/ ./internal/sftp/ ./internal/tui/ -count=1
```

- `TestSessionEnvForward`：locale 白名单透传 / 空值不传 / 非白名单不传
- `TestSessionEnvPromptCommandRejected`：PROMPT_COMMAND env 请求被拒绝（对齐真实 OpenSSH AcceptEnv）
- `TestSessionCwdHookInjected`：首次进入会话 shell 内注入 cwd 钩子（bash/zsh/tmux passthrough/清除序列）
- `TestOSCTracker`：OSC 133;cwd 解析 + 从输出中剔除；非目标序列原样透传
- `TestOSCTrackerPiterminalNoHold`/`TestOSCTrackerPiTerminalSplitBoundary`/`TestOSCTrackerCwdPrefixHeldOnly`：**只对 cwd 前缀片段（≤10 字节）跨 Write 保留，非 cwd 的 OSC（OSC 8/133、同步输出、光标序列）即使 BEL 在下一 chunk 也立即原样透传**——防 pi 这类每按键整帧差分重绘 TUI 输出被拆包延迟导致输入法 preedit 闪烁（见第 3 节）
- `TestSessionPtyIUTF8`：IUTF8=1 / IMAXBEL=1 / IXOFF=0 / 基础模式俱全
- `TestResolveTermSize`：`(width, height)`→`(rows, cols)` 换算顺序 / 非法尺寸回退 24×80（PTY 尺寸颠倒的回归防线）
- `TestSessionDetachKeepsConnection`：detach 挂起后同一 SSH 连接仍可再建会话（不重连的回归防线）
- `TestSessionResume`：detach 挂起 → 恢复同一会话透传 → EOF 正常结束（挂起/恢复循环回归）
- `TestOSCTrackerQuiet`：静音丢弃输出但消费、OSC cwd 跟踪不受影响
- `TestHostKeyCallbackFirstConnect`/`TestHostKeyTrustFlow`/`TestHostKeyCallbackCorruptKnownHosts`：首次连接返回 `UnknownHostKeyError`（不静默信任）、信任后同指纹放行/异指纹拒绝、known_hosts 解析失败显式报错（不降级 InsecureIgnoreHostKey）
- `TestPasswordAnswer`：k-i 盲答限制——单提示不回显应答、回显/多提示中止
- `TestPollInputDataAndStop`/`TestPollInputStdinEOF`：poll+自管道输入源——数据就绪返回、stop 唤醒返回 EOF、stdin EOF 返回 EOF
- `TestSFTPFirstConnectFingerprintConfirm`：SFTP 页首次连接确认态——y 信任并重连、n 取消、信任失败提示
- 新增行为必须补对应断言（服务端记录能力可扩展）

## 9. 踩坑史（时间线）

| 日期 | 问题 | 根因 | 修复 |
|---|---|---|---|
| 2026-08 | 远程输入光标跳行首、CJK 对齐错乱 | 不传 locale env（远程 C locale）+ TerminalModes 缺 IUTF8/IMAXBEL 等 27 项 | PR #1：sessionEnv 白名单透传 + 40 项完整模式集 + 回归测试 |
| 2026-08 | 远程输入布局乱、ls 输出后光标乱跑、退格错乱（系统 ssh 正常） | `term.GetSize` 返回 `(width, height)` 被当作 `(rows, cols)`，远程 PTY 行列颠倒 | `resolveTermSize` 换算 + `TestResolveTermSize` 回归（见第 5 节） |
| 2026-08 | 会话中唤起 SFTP 会断开 SSH 并重连，重连后 shell 回 home、目录丢失 | detach 时 `Session.Close()` + main 重连 | 挂起/恢复架构（`Handle`）：detach 不关闭连接，输出静音，恢复同一会话；SFTP 复用同一连接；`TestSessionDetachKeepsConnection`/`TestSessionResume` 回归 |
| 2026-08 | SSH 当前目录始终带不到 SFTP（tracker.Cwd 恒空） | `Setenv("PROMPT_COMMAND", ...)` 的 env 请求被 sshd 默认 `AcceptEnv`（仅 LANG/LC_*）拒绝，OSC 从未上报；内存测试服务器宽松接受 env 掩盖了该问题 | cwd 钩子改为首次进入会话时经 stdin **shell 内注入**（bash PROMPT_COMMAND 追加 / zsh precmd_functions 追加 / sh 静默），testutil 服务器对齐真实 OpenSSH 拒绝 PROMPT_COMMAND env；`TestSessionCwdHookInjected`/`TestSessionEnvPromptCommandRejected` 回归 |
| 2026-08 | 注入命令明文回显、OSC 133;cwd 序列透传污染终端 | tty ECHO 无法关闭，注入命令必然回显；tracker 原样透传 OSC 到本地终端；命令内清行（`\033[1A\033[2K`）/清屏（`\033[2J\033[H`）控制序列干扰 bash readline（光标错位、输入不可见） | 发送前 `stty -echo` 关闭 ECHO（注入命令不回显，随后 `stty echo` 恢复），命令本体无控制序列；`oscTracker` 解析后**剔除** OSC 133;cwd 序列（对齐 Tabby），tmux 内用 passthrough 序列；`TestOSCTracker` 过滤断言回归 |
| 2026-08 | 注入命令回显难以隐藏（`stty -echo` 一次性写入靠时序赌注、慢网必漏；确认标记握手虽可靠但残留引导行） | 原实现 `inPipe.Write("stty -echo\r"+cwdHookCommand+"; stty echo\r")`；改确认标记握手后仍残留引导行 `stty -echo; printf ...` | **改 PTY 层 ECHO=0**（`ssh.Client.NewTerminalSession` pty-req 即关 ECHO）：注入命令从第一条起完全**不回显、无任何残留**（无需 stty -echo/握手/引导行），注入末尾 `stty echo` 恢复交互；`TestSessionPtyIUTF8` 断言 ECHO=0 回归 |
| 2026-08 | 注入后多一个空行（bash 在 ECHO=0 下处理输入行输出 `\x1b[?2004l\r\r\n`） | 注入命令被 bash 执行时会额外输出一行换行序列，屏幕出现孤立空行 | 注入命令末尾追加 `clear`，清除注入产生的空行，提示符干净出现在屏幕顶部；`TestRealHostFinalInjection`（临时）真实验证 |
| 2026-08 | 首次连接静默信任主机指纹（安全缺口）；known_hosts 解析失败静默降级 `InsecureIgnoreHostKey`；k-i 对所有提示盲答密码；unix 输入 10ms 忙等轮询 | 早期安全/性能取舍 | ① 首次连接返回 `UnknownHostKeyError`，展示指纹 + 用户确认后 `TrustHostKey` 并重连（main 终端提示 / SFTP 页确认态）；② `knownhosts.New` 失败显式报错；③ `passwordAnswer` 仅单提示不回显时应答；④ unix 输入改 poll(2)+自管道阻塞读；均补回归测试并同步本清单 |
| 2026-08 | 经 simple-connect 用 pi（Ink 差分渲染 TUI）输入中文，拼音选词阶段字母闪烁；系统 ssh 直连不闪 | pi 每按键整帧差分重绘（同步输出 `\x1b[?2026h..l` + 逐行 OSC 8 复位 + 光标 hide/定位）。旧 `scan` 遇到任意 `\x1b]` 且本 chunk 无 BEL 就把整段尾部挂起等 BEL，SSH 分块恰切在 `\x1b]8;;` 与 BEL 之间时会把 pi 的帧拆包延迟，同步输出未闭合时终端收到残帧、重绘时序错乱，擦掉输入法 preedit | `scan` 改为**只对「`\x1b]133;cwd=` 前缀或其路径未收尾」的字节做跨 Write 保留（≤10 字节）**，非 cwd 的 OSC 即使 BEL 在下一 chunk 也立即原样透传（字节序精确，不做语义拼接）。对 pi 流量零保留、完全透明；`TestOSCTrackerPiterminalNoHold` 等回归 |
| 2026-08 | 会话内远程登录横幅（Linux/Welcome/Last login）被重复多份、光标错位、输入不可见 | `oscTracker.scan` 在「无 ESC 序列」分支（`i<0`）把全部已透传数据又写回 `t.buf`，下一次 Write 时 `t.buf+p` 里残留内容被**重复输出**（横幅等纯文本跨 Write 边界重复） | `i<0` 分支不再向 `t.buf` 保留任何字节（跨 Write 的序列起点由 `i>=0 且 j<0` 分支保留）；另 `Write` 合并残留时拷贝到独立切片避免与 `t.buf` 共享底层数组。实测横幅由 6 次降为 1 次；`TestOSCTrackerNoDuplicateOnBoundary` 回归 |
| 早期 | 不在 config 中的主机被改端口/认证方式 | `ssh_config.Get` 无匹配时返回默认值（Port=22、IdentityFile） | 基于解析结果取值（sshConfigGetter），加 `TestResolveSSHConfigNoConfigMatch` 回归 |

## 10. 维护约定

- 本清单随代码演进更新：行为变更必须同步本条 + 第 8 节测试
- "TODO" 条目是已知差距，非当前行为，改动前确认现状（读代码，不信文档——文档可能滞后）