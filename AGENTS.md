# 项目规则

## 项目简介

simple-connect 是一个使用 Go 开发的 SSH/SFTP 连接管理 TUI 应用：
- TUI 管理连接配置（增删改查、过滤、在线/离线状态检测）
- Enter 使用**保存的凭据**建立自建交互会话（x/crypto/ssh 认证 + 本地终端透传，免密码输入）
- 内嵌 SFTP 文件浏览器（**双栏：本地 | 远程**，Tab 切换焦点，Enter 进目录；`t` 传输光标条目、`p` 输入路径传输，均按焦点栏定向：本地=上传、远程=下载），支持异步上传/下载/删除与多选批量传输

## 技术栈（固定版本）

| 模块 | 依赖 | 版本 |
|---|---|---|
| TUI 框架 | `charm.land/bubbletea/v2` | v2.0.9 |
| 组件 | `charm.land/bubbles/v2`（textinput 等） | 最新 |
| 样式 | `github.com/charmbracelet/lipgloss` | 最新 |
| 终端 | `github.com/charmbracelet/x/term` | 最新 |
| SSH | `golang.org/x/crypto/ssh` | 最新 |
| SSH config | `github.com/kevinburke/ssh_config` | 最新 |
| SFTP | `github.com/pkg/sftp` | 最新 |
| 密钥存储 | `github.com/zalando/go-keyring` | 最新 |

## 目录结构

```
main.go                  # 入口：TUI ⇄ 自建 SSH 会话 ⇄ SFTP 页循环（会话中热键可唤起 SFTP）
internal/
  model/                 # 数据模型（Host、AuthType）
  store/                 # 持久化：JSON 配置 + Secrets 接口（keyring / 文件兜底）
  ssh/                   # SSH 客户端、状态探活、known_hosts、agent 检测、~/.ssh/config
  sftp/                  # SFTP 传输与浏览逻辑：Dial/List/Remove/Transfer/Upload/Download
  session/               # 自建交互会话：raw 模式 + PTY 透传 + resize（unix SIGWINCH / windows 轮询）
  exec/                  # 调用系统 ssh/sftp（跨平台，后备降级用）
  applog/                # 轻量错误日志落盘（用户缓存目录，0600）
  tui/                   # Bubble Tea 界面（root/list/form/sftp 页面）
  testutil/              # 测试辅助：内存 SSH/SFTP 服务器（含 shell/keyboard-interactive）
```

## 连接机制（重点）

- **自建会话**：`sshc.Connect`（读 keyring 保存的密码/密钥认证）→ `session.StartInteractive`（本地终端 raw 模式 + 远程 PTY 字节透传）。不做终端渲染，本地终端即渲染器，vim/tmux 与系统 ssh 表现一致。
- **热键唤起 SFTP（挂起而非断开）**：会话中按 `Ctrl+X f` 只**挂起**透传并返回 `ErrDetach`——**不关闭 SSH 连接与远程 shell**，`oscTracker` 静音丢弃挂起期间输出（防污染 SFTP 页），目录/进程/连接全部保持。main 进入该主机 SFTP 页（`sftpLoop`），SFTP **复用同一 SSH 连接**（`sftpc.NewConnFromSSH`，免重新认证），`q` 后 `sess.Resume()` **原样恢复**同一会话（不清屏、不发任何字节，仅发一次尺寸同步 `WindowChange`；仅首次进入时在**请求 PTY 前**清屏（`clearScreen`，早于登录 banner，避免 banner 被擦除 + 空白闪烁）并注入 cwd 钩子），再次挂起则继续循环。`Session.Wait()` 只起一次 goroutine，经 channel 复用（`Handle.waitDone`）；detach 不调 `s.Close()`、不关 `StdinPipe`（避免 EOF）。输入经 `StdinPipe()` 手动泵（`pumpInput`，勿用 `s.Stdin`，会因内部 stdin goroutine 阻塞 `Wait` 死锁）；unix 下基于 `poll(2)` + 自管道阻塞读（`pollInput`，detach 时向自管道写字节唤醒干净退出，无 10ms 轮询延迟），Windows 阻塞读（detach 后残留读取 goroutine，下次按键自行退出，可能吞一键）。
- **cwd 追踪（SSH 路径带到 SFTP）**：SSH 协议不提供 cwd 查询，靠 shell 在提示符前上报 OSC 133;cwd（`oscTracker` 解析，**解析后将该序列从输出中剔除**，不污染终端）。**注意 sshd 默认 `AcceptEnv` 拒绝 PROMPT_COMMAND**（env 注入失效），因此**首次进入会话时经 stdin 注入钩子命令**（`cwdHookCommand`）：先定义 `_sc_cwd` 函数，bash 追加 PROMPT_COMMAND（`${PROMPT_COMMAND:+...}` 展开，**`set -u` 安全**）、zsh 走 `precmd_functions` 追加（eval 包裹防 POSIX 语法错）、sh 静默降级，变量引用一律 `${VAR:-}`；远程 tmux 内用 passthrough 序列上报。回显隐藏靠 PTY 层 `ECHO=0`（pty-req 即关 ECHO）：**bash 的 readline 尊重该标志故注入命令不回显；zsh 的 ZLE 始终重绘，钩子命令与 `stty echo` 会在首屏留下约 4 行（观感问题，功能无影响）**。`stty echo` 由 `restoreRemoteEcho` **单独成行**发送（非 POSIX shell 解析钩子行失败时不致 ECHO 永久关闭而盲打）；钩子注入（`injectCwdHook`）与回显恢复各自记状态（`hookInjected`/`echoRestored`），写入失败时下次 Resume **只重试失败项**（已成功字节不重发，避免 PROMPT_COMMAND 重复追加）；清屏单独记 `cleared`，由 `clearScreen` 在 `Handle.start()` 请求 PTY **之前**执行（若晚于 PTY 建立，会擦掉已到达的登录 banner 并留下约 0.6s 空白，表现为连接闪烁），不受注入结果影响；命令本体无控制序列（避免干扰 readline）；成功后仅发送一次，恢复时不发。SFTP 打开时用 `sess.Cwd()` 定位远程栏。**oscTracker 跨 Write 边界处理易踩坑**：`scan` 在「无 ESC 序列」分支（`i<0`）不得向 `t.buf` 保留已透传数据（否则登录横幅等纯文本跨边界重复输出、光标错位）；`Write` 合并残留时须拷贝到独立切片避免与 `t.buf` 共享底层数组。**只对「`\x1b]133;cwd=` 前缀或其路径未收尾」的字节做跨边界保留（≤10 字节），其余一切（含 pi 这类 TUI 的 OSC 8/133、同步输出 `\x1b[?2026h..l`、光标定位/隐藏序列）一律立即原样透传，绝不因等某个 BEL 而扣住大段尾部**——否则会把每按键整帧差分重绘 TUI 的输出拆包延迟，残帧重绘擦掉输入法 preedit → 拼音选词阶段字母闪烁（系统 ssh 无字节处理故正常）。改前必看 `TestOSCTrackerNoDuplicateOnBoundary` 与 `TestOSCTrackerPiterminalNoHold`。
- **~/.ssh/config**：`sshc.Connect` 会合并 config（`Host` 别名、`User`、`Port`、`IdentityFile`、`ProxyJump`），跳板经隧道链式连接，跳板复用目标保存的密码。**注意：必须基于解析后的 `*ssh_config.Config` 取值（`sshConfigGetter`），禁止直接用 `ssh_config.Get`**——后者在无匹配条目时返回默认值（Port=22、IdentityFile=~/.ssh/identity），会把不在 config 中的主机改错端口和认证方式（严重 bug，已有回归测试 `TestResolveSSHConfigNoConfigMatch`）。
- **认证**：`authMethods` = 私钥 + 密码 + keyboard-interactive（保存密码应答）+ ssh-agent，依次尝试。
- **内嵌 SFTP**：列表页进入用 `sshc.ConnectRaw` 独立连接（不合并 config，避免测试污染与意外行为）；会话挂起唤起复用 `sess.SSHClient()`（`NewConnFromSSH`，不重新认证）。
- **降级**：自建会话失败时 main 提示是否降级用系统 `ssh`。
- **TerminalModes** 与 OpenSSH 对齐（ECHO/ECHOE/ICRNL/ONLCR/OPOST/CS8 等）。
- **会话/SSH 层改动前必读 `docs/ssh-compat.md`**（覆盖 `internal/ssh`、`internal/session`、`internal/testutil`）：定义了与 OpenSSH 对齐的行为基线（pty-req/env/shell 顺序、40 项 TerminalModes、locale 白名单、`term.GetSize` 的 `(width,height)`→`(rows,cols)` 尺寸换算、known_hosts 等）。**任何会话/连接层行为变更须同步该文档与对应回归测试（含 `TestResolveTermSize`）**。

## 开发规范

- **依赖固定**：bubbletea 必须使用 `charm.land/bubbletea/v2@v2.0.9`，禁止引入 v1（`github.com/charmbracelet/bubbletea`）。
- **Bubble Tea v2 API 约定**（与 v1 差异大，务必遵守）：
  - `View()` 返回 `tea.View`（用 `tea.NewView()` 构造，整页模式设置 `v.AltScreen = true`）。
  - `Init()` 只返回 `tea.Cmd`；`Update(Msg) (Model, Cmd)`。
  - `tea.Quit()` 返回的是 `Msg` 不是 `Cmd`，需包装：`tea.Cmd(func() tea.Msg { return tea.Quit() })`。
  - 按键通过 `tea.KeyPressMsg` 分发；特殊键用 `tea.KeyEnter`/`tea.KeyEsc`/`tea.KeyTab` 等 Code 常量判断，可打印字符用 `Key.Text`，Shift 组合用 `k.Mod.Contains(tea.ModShift)`。
- **中文渲染**：禁止用 `fmt.Sprintf("%-Ns", ...)` 做列对齐（CJK 双宽字符会导致错位），一律用 `padRight()`（基于 runewidth，见 `internal/tui/styles.go`）。
- **文本输入框**：统一使用 `textInput()` 工厂函数创建，需调用 `SetStyles` 禁用光标闪烁（否则每个按键会触发 530ms tick 阻塞测试与渲染）。
- **分层约定**：SFTP 传输与浏览逻辑必须放 `internal/sftp` 包（`Dial`/`List`/`Remove`/`Transfer`/`Upload`/`Download`/`FormatSize`），该包不依赖 bubbletea；TUI 层只负责调度（`tea.Tick` 进度轮询）与渲染。
- **异步操作**：SFTP 上传/下载必须通过 `sftpc.Upload`/`sftpc.Download` 在 goroutine 中执行，进度写入带 mutex 的 `sftpc.Transfer`，TUI 层用 `tea.Tick` 轮询回传，禁止阻塞 Update 循环。
- **传输取消与原子写入**：传输**不支持离页后台继续，也不支持再次进入恢复进度**（`Transfer` 绑定 SFTP 页模型，离页即关闭连接）。上传/下载统一写 `<目标>.part` 临时文件，成功后改名为最终目标（远程优先 `PosixRename`），取消/失败时删除临时文件，避免半截目标文件。`Transfer.Cancel()` 为协作式取消（读写前检查 ctx，`Canceled()` 区分用户取消与真实错误）；TUI 传输中 `q`/`Ctrl+C` 先弹退出确认，确认后请求取消并轮询等待，取消完成（临时文件已清理）才回列表，超时约 5s 兜底强制离开。
- **传输前扫描（覆盖冲突 + 总量）**：`sftpc.ScanUpload`/`ScanDownload`/`BatchScan` 一次扫描同时产出冲突列表与总字节数，TUI 存入 `pendingTransfer.total` 并用 `Transfer.SetTotal` 预设，传输函数（`UploadPath`/`DownloadPath`/`BatchTransfer`）检测到已预设则跳过开传前的目录统计。上传目录的冲突检测改为「一次远程 Walk 建已存在文件集合 + 一次本地 Walk 比对」（旧实现是逐文件远程 Stat 的 N 次往返）；远程 Walk 循环内须检查 `walker.Err()`。覆盖确认取消靠 `overwriteSeq` 作废迟到结果。
- **下载并发读**：`downloadFile` 必须直接 `rf.WriteTo(progressWriter{w: f, t: t})`——进度计数与取消检查放在**写侧**。禁止把 `*sftp.File` 包成只暴露 `Read` 的 Reader（会挡住 `io.WriterTo`，退回 32KB 串行读，回归基准 `BenchmarkDownloadBufSize` 会暴露）；取消非瞬时（WriteTo 收敛在途读 goroutine），与上层约 5s 兜底一致。
- **列表页状态与探测**：列表状态（过滤词/光标 ID/在线状态 map）通过 `ListState` 快照在 `main` 的 SSH 往返间保留（`NewRootWithListState`/`Root.ListState()`）；`reload` 按 host ID 恢复光标、保留 status，删除当前项落邻近位置。状态轮询用双世代号：`tickGen` 管续订链（Init/回列表自增并挂新 tick，过期 tick 丢弃——**禁止用 armed 布尔**）、`probeGen` 作废过期探测结果；探测每台主机一个 Cmd（`tea.Batch` 并发、信号量限流 10），探完即亮。列表滚动窗口与列宽按 `WindowSizeMsg` 计算，名称/目标列先 `runewidth.Truncate` 再 `padRight`。
- **表单 View 只读**：`formModel.View()` 不得 mutate 模型（错误提示保留到用户下一次按键的 `Update` 才清）；密码/私钥字段按认证方式二选一显示，Tab/方向键导航只经过可见字段（字段下标常量见 `form.go`）。
- **本地路径用 `filepath`、远程路径用 `path`**：SFTP 本地栏导航（`enterCurrent`/`goUp`/`mkdir`）一律 `filepath.Join`/`localParent`（filepath.Dir），Windows 盘符路径下 `path.Dir` 会直接坏；远程栏保持 POSIX `path`。
- **并发安全**：跨 goroutine 共享状态（传输进度等）必须加锁，禁止在 Cmd 闭包内直接修改 UI 模型字段（存在数据竞争）。
- **关联匹配优先数据库/服务端处理**：SFTP 目录读取、删除等一律走 `pkg/sftp` 原生接口，禁止本地缓存后拼接。
- **凭据安全**：密码不落明文配置（hosts.json），经 `store.Secrets` 接口存入系统 keyring，无 keyring 时兜底文件（0600），并在 UI 显示"密码明文存储"警告。
- **免密引导**：密码认证且已保存密码时 `connectHint()` 返回空（自建会话直连免密）；仅当未保存密码、密钥未入 agent、agent 未运行时进入"待确认"状态（Enter 二次确认 / Esc 取消）。agent 检测在 `internal/ssh/agent_check.go`（跨平台：unix 走 `SSH_AUTH_SOCK`，Windows 走 OpenSSH agent 命名管道），仅本地毫秒级检查，禁止在连接后阻塞。

## 测试要求

- 所有测试文件与源码同包放置（`internal/tui/root_test.go`、`sftp_e2e_test.go`、`session_test.go` 等）。
- TUI 模型测试用直接 `Update` 驱动（已禁用光标闪烁，无需真实程序）。
- SFTP 测试统一使用 `internal/testutil.StartSFTP`（内存 SSH/SFTP 服务器），远程文件系统限定在 `t.TempDir()`，主机指纹通过 `sshc.WithHostKeyCallback(InsecureIgnoreHostKey())` 注入，禁止触碰真实 `~/.ssh/known_hosts`。
- 交互会话测试使用 `internal/testutil.StartShell`（内存 SSH 服务器，仅 keyboard-interactive 认证 + shell 回显），用 `io.Pipe` 注入 in/out 驱动 `runSession`；热键 detach 用 `TestSessionDetach` 覆盖（Ctrl+X f 返回 `ErrDetach`、序列不转发远程、跨 Read 边界、`\x18x` 原样转发）。
- 注意：测试服务器 `WithServerWorkingDirectory` 只对相对路径生效，测试中远程路径一律基于 `env.Root` 拼绝对路径。
- 注意：`sshc.Connect` 会读取真实 `~/.ssh/config`，**测试一律用 `ConnectRaw`/`sftpc.Dial`** 避免污染；config 合并逻辑用注入的 `configGetter` 单测覆盖。
- 新增功能必须补测试；修改传输/导航逻辑必须跑通 e2e 测试，并可用 `go test -race` 校验并发安全。

## 验证命令

```bash
go build ./...
go vet ./...
go test ./... -count=1
```

## 跨平台注意事项

- 自建会话使用 `charmbracelet/x/term` 设置/恢复本地终端 raw 模式（unix + Windows console）；resize 转发 unix 用 `SIGWINCH`、Windows 用轮询。
- 系统命令 `ssh`/`sftp` 仅作降级后备（`internal/exec/launcher.go`）；Windows 下缺失时提示安装。
- Windows 下通过 `hide_windows.go`（build tag `windows`）隐藏控制台窗口。
- keyring 后端随平台自动切换（macOS Keychain / Linux Secret Service / Windows 凭据管理器）。
