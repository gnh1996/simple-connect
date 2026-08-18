# 项目规则

## 项目简介

simple-connect 是一个使用 Go 开发的 SSH/SFTP 连接管理 TUI 应用：
- TUI 管理连接配置（增删改查、过滤、在线/离线状态检测）
- Enter 通过系统 `ssh` 命令启动交互式会话（启动器模式）
- 内嵌 SFTP 文件浏览器，支持异步上传/下载/删除与拖拽上传

## 技术栈（固定版本）

| 模块 | 依赖 | 版本 |
|---|---|---|
| TUI 框架 | `charm.land/bubbletea/v2` | v2.0.8 |
| 组件 | `charm.land/bubbles/v2`（textinput 等） | 最新 |
| 样式 | `github.com/charmbracelet/lipgloss` | 最新 |
| SSH | `golang.org/x/crypto/ssh` | 最新 |
| SFTP | `github.com/pkg/sftp` | 最新 |
| 密钥存储 | `github.com/zalando/go-keyring` | 最新 |

## 目录结构

```
main.go                  # 入口：TUI ⇄ 系统 ssh 的启动器循环
internal/
  model/                 # 数据模型（Host、AuthType）
  store/                 # 持久化：JSON 配置 + Secrets 接口（keyring / 文件兜底）
  ssh/                   # SSH 客户端、连接状态探活、known_hosts 校验
  sftp/                  # SFTP 传输与浏览逻辑：Dial/List/Remove/Transfer/Upload/Download
  exec/                  # 调用系统 ssh/sftp（跨平台）
  tui/                   # Bubble Tea 界面（root/list/form/sftp 页面）
  testutil/              # 测试辅助：内存 SSH/SFTP 服务器
```

## 开发规范

- **依赖固定**：bubbletea 必须使用 `charm.land/bubbletea/v2@v2.0.8`，禁止引入 v1（`github.com/charmbracelet/bubbletea`）。
- **Bubble Tea v2 API 约定**（与 v1 差异大，务必遵守）：
  - `View()` 返回 `tea.View`（用 `tea.NewView()` 构造，整页模式设置 `v.AltScreen = true`）。
  - `Init()` 只返回 `tea.Cmd`；`Update(Msg) (Model, Cmd)`。
  - `tea.Quit()` 返回的是 `Msg` 不是 `Cmd`，需包装：`tea.Cmd(func() tea.Msg { return tea.Quit() })`。
  - 按键通过 `tea.KeyPressMsg` 分发；特殊键用 `tea.KeyEnter`/`tea.KeyEsc`/`tea.KeyTab` 等 Code 常量判断，可打印字符用 `Key.Text`，Shift 组合用 `k.Mod.Contains(tea.ModShift)`。
- **中文渲染**：禁止用 `fmt.Sprintf("%-Ns", ...)` 做列对齐（CJK 双宽字符会导致错位），一律用 `padRight()`（基于 runewidth，见 `internal/tui/styles.go`）。
- **文本输入框**：统一使用 `textInput()` 工厂函数创建，需调用 `SetStyles` 禁用光标闪烁（否则每个按键会触发 530ms tick 阻塞测试与渲染）。
- **分层约定**：SFTP 传输与浏览逻辑必须放 `internal/sftp` 包（`Dial`/`List`/`Remove`/`Transfer`/`Upload`/`Download`/`FormatSize`），该包不依赖 bubbletea；TUI 层只负责调度（`tea.Tick` 进度轮询）与渲染。
- **异步操作**：SFTP 上传/下载必须通过 `sftpc.Upload`/`sftpc.Download` 在 goroutine 中执行，进度写入带 mutex 的 `sftpc.Transfer`，TUI 层用 `tea.Tick` 轮询回传，禁止阻塞 Update 循环。
- **并发安全**：跨 goroutine 共享状态（传输进度等）必须加锁，禁止在 Cmd 闭包内直接修改 UI 模型字段（存在数据竞争）。
- **关联匹配优先数据库/服务端处理**：SFTP 目录读取、删除等一律走 `pkg/sftp` 原生接口，禁止本地缓存后拼接。
- **凭据安全**：密码不落明文配置（hosts.json），经 `store.Secrets` 接口存入系统 keyring，无 keyring 时兜底文件（0600），并在 UI 显示"密码明文存储"警告。

## 测试要求

- 所有测试文件与源码同包放置（`internal/tui/root_test.go`、`sftp_e2e_test.go` 等）。
- TUI 模型测试用直接 `Update` 驱动（已禁用光标闪烁，无需真实程序）。
- SFTP 测试统一使用 `internal/testutil.StartSFTP`（内存 SSH/SFTP 服务器），远程文件系统限定在 `t.TempDir()`，主机指纹通过 `sshc.WithHostKeyCallback(InsecureIgnoreHostKey())` 注入，禁止触碰真实 `~/.ssh/known_hosts`。
- 注意：测试服务器 `WithServerWorkingDirectory` 只对相对路径生效，测试中远程路径一律基于 `env.Root` 拼绝对路径。
- 新增功能必须补测试；修改传输/导航逻辑必须跑通 e2e 测试，并可用 `go test -race` 校验并发安全。

## 验证命令

```bash
go build ./...
go vet ./...
go test ./... -count=1
```

## 跨平台注意事项

- 系统命令 `ssh`/`sftp` 依赖 OpenSSH 客户端；Windows 下缺失时提示安装（`internal/exec/launcher.go`）。
- Windows 下通过 `hide_windows.go`（build tag `windows`）隐藏控制台窗口。
- keyring 后端随平台自动切换（macOS Keychain / Linux Secret Service / Windows 凭据管理器）。
