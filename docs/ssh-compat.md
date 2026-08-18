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

**为什么**：不传 → 远程 shell 落到 C/POSIX locale：
- readline 按字节处理输入，命令行重绘（redisplay）光标位置错乱 → **输入时跳行首**
- `wcwidth` 将 CJK 按宽度 1 计算，终端实际渲染宽度 2 → **ls/vim 对齐错乱**
- 大量程序（git status、grep --color 等）行为退化

**现实约束**：服务器 `sshd_config` 默认 `AcceptEnv LANG LC_*`；若关闭，`Setenv` 返回错误——**忽略错误继续**（不阻塞连接，与 OpenSSH 一致）。

## 4. known_hosts 策略（`internal/ssh/client.go` hostKeyCallback）

- 连接前解析 `~/.ssh/known_hosts`（knownhosts.New）
- 匹配成功 → 放行；不匹配（KeyError 且 Want 非空）→ 报错拒绝，提示检查 known_hosts
- 首次连接（Want 为空）→ **现状：静默信任并追加（TODO：改为展示 SHA256 指纹 + 用户确认，对齐 OpenSSH ask 模式）**
- 追加前确保 `~/.ssh` 存在（0700），否则 O_CREATE 静默失败，下次连接退回不校验路径
- **`knownhosts.New` 失败禁止静默降级 `InsecureIgnoreHostKey()`**（TODO：改为显式报错）——静默关闭校验是任何安全组件的禁忌

## 5. window-change 时序（`internal/session/resize_*.go`）

- 启动时 `term.GetSize` 一次作为 PTY 初始尺寸
- unix：`SIGWINCH` → `WindowChange(rows, cols)`；Windows：500ms 轮询尺寸变更
- 已知差距（可接受）：OpenSSH 会发送两次初始 window-change（连接后立即 + 稍后），本实现只随 pty-req 发一次。对现代服务器无感知差异

## 6. 认证行为（`internal/ssh/client.go` authMethods）

尝试顺序：**私钥 → 密码（password + keyboard-interactive 同密码）→ ssh-agent**，全部失败才报错。

- **k-i 盲答限制（TODO）**：当前对所有提示都回填密码。OTP/堡斯机二次验证场景会用密码应答验证码提示，多次失败可能锁账号。目标：仅当 `len(questions)==1 && !echos[0]` 时应答，多提示一律中止
- **跳板机复用目标密码**：`dialWithJumps` 假设跳板与目标凭据相同（文档化假设）。凭据不同时认证失败，报错信息已指明跳板环节

## 7. 输入链路（`internal/session/`）

- 热键 Ctrl+X f：前缀状态机 `detachScanner`，正确处理跨 Read 边界、`\x18x`、`\x18\x18`；detach 后字节不转发
- **pollInput 10ms 忙等轮询（TODO）**：O_NONBLOCK+usleep 轮询有 10ms 级延迟且粘贴吞吐受限（≈51KB/s）。目标：阻塞读 + `poll(2)`/select 带停止信号唤醒
- Enter 键：本地 raw 后 `\r`(0x0D) 直传远程 → 依赖远程 `ICRNL=1` 转 `\n`（第 2 节）

## 8. 回归测试对照（`internal/testutil`）

内存 SSH 服务器可记录：窗口尺寸、env 请求、pty-req 模式段。

**修改会话初始化逻辑必须跑通：**

```bash
go test ./internal/session/ -count=1 -run 'TestSession' -v
go test -race ./internal/session/ ./internal/sftp/ ./internal/tui/ -count=1
```

- `TestSessionEnvForward`：locale 白名单透传 / 空值不传 / 非白名单不传
- `TestSessionPtyIUTF8`：IUTF8=1 / IMAXBEL=1 / IXOFF=0 / 基础模式俱全
- 新增行为必须补对应断言（服务端记录能力可扩展）

## 9. 踩坑史（时间线）

| 日期 | 问题 | 根因 | 修复 |
|---|---|---|---|
| 2026-08 | 远程输入光标跳行首、CJK 对齐错乱 | 不传 locale env（远程 C locale）+ TerminalModes 缺 IUTF8/IMAXBEL 等 27 项 | PR #1：sessionEnv 白名单透传 + 40 项完整模式集 + 回归测试 |
| 早期 | 不在 config 中的主机被改端口/认证方式 | `ssh_config.Get` 无匹配时返回默认值（Port=22、IdentityFile） | 基于解析结果取值（sshConfigGetter），加 `TestResolveSSHConfigNoConfigMatch` 回归 |

## 10. 维护约定

- 本清单随代码演进更新：行为变更必须同步本条 + 第 8 节测试
- "TODO" 条目是已知差距，非当前行为，改动前确认现状（读代码，不信文档——文档可能滞后）