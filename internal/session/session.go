package session

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"

	"github.com/charmbracelet/x/term"
	"golang.org/x/crypto/ssh"

	sshc "simple-connect/internal/ssh"
)

// ErrDetach 表示用户在会话中按热键请求切换到 SFTP
var ErrDetach = errors.New("用户请求切换到 SFTP")

// oscPromptCommand 让远程 bash 在每次提示符前输出当前目录（OSC 133;cwd，iTerm2 shell
// integration 兼容）。仅通过会话 env 注入，不修改远程任何文件；
// 若远程 .bashrc 覆盖 PROMPT_COMMAND（powerline 等），自动失跟踪并安全降级。
const oscPromptCommand = `printf '\033]133;cwd=%s\007' "$PWD"`

// localeEnvKeys 透传到远程会话的环境变量（对齐 OpenSSH 客户端默认 SendEnv LANG LC_*）。
// 不传时远程落到 C/POSIX locale：readline 按字节处理输入，中文/非 ASCII 输入后
// 命令行重绘光标错位（表现为跳行首），CJK 宽度对齐也全乱。
var localeEnvKeys = []string{
	"LANG", "LC_ALL", "LC_CTYPE", "LC_MESSAGES", "LC_COLLATE",
	"LC_TIME", "LC_NUMERIC", "LC_MONETARY", "LC_PAPER", "LC_NAME",
	"LC_ADDRESS", "LC_TELEPHONE", "LC_MEASUREMENT", "LC_IDENTIFICATION",
}

// sessionEnv 收集本机需透传到远程会话的环境变量（仅取非空值）
func sessionEnv() map[string]string {
	out := map[string]string{}
	for _, k := range localeEnvKeys {
		if v := os.Getenv(k); v != "" {
			out[k] = v
		}
	}
	return out
}

// oscTracker 从输出字节流中提取 shell integration 的 cwd 标记：
// `ESC ] 133;cwd=<path> BEL`。路径来自远程输出，仅用于 UI 展示/定位，
// 绝不拼入本机 shell 执行。
type oscTracker struct {
	mu   sync.Mutex
	cwd  string
	buf  []byte // 跨 Write 边界残留（含序列头部但未收尾的部分）
	out  io.Writer
}

func newOSCTracker(out io.Writer) *oscTracker {
	return &oscTracker{out: out, buf: make([]byte, 0, 256)}
}

func (t *oscTracker) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	n, err := t.out.Write(p)
	data := append(t.buf, p...)
	t.scan(data)
	return n, err
}

// scan 扫描并提取 OSC 133;cwd 序列；保留尾部最多 oscMaxKeep 字节防序列跨界
func (t *oscTracker) scan(data []byte) {
	const oscMaxKeep = 4096
	t.buf = t.buf[:0]
	for len(data) > 0 {
		i := bytes.IndexByte(data, 0x1b)
		if i < 0 {
			if len(data) > oscMaxKeep {
				data = data[len(data)-oscMaxKeep:]
			}
			t.buf = append(t.buf, data...)
			return
		}
		j := bytes.IndexByte(data[i:], 0x07) // BEL 结束符
		if j < 0 {
			if len(data[i:]) > oscMaxKeep {
				data = data[i+len(data[i:])-oscMaxKeep:]
			}
			t.buf = append(t.buf, data[i:]...)
			return
		}
		seq := data[i : i+j]
		if len(seq) > 1 && bytes.HasPrefix(seq[1:], []byte("]133;cwd=")) {
			t.cwd = string(seq[1+len("]133;cwd="):])
		}
		data = data[i+j+1:]
	}
}

// Cwd 返回最近一次跟踪到的远程工作目录（未跟踪到返回空串）
func (t *oscTracker) Cwd() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.cwd
}

// 会话中唤起 SFTP 的热键序列：Ctrl+X 后按 f
const (
	detachCtrlX = 0x18
	detachFKey  = 'f'
)

// StartInteractive 建立自建交互式 SSH 会话。
// 认证已由 sshc.Connect 使用保存的凭据完成；这里负责将本地终端置为 raw 模式，
// 与远程 PTY 做字节透传，结束后恢复终端。
// 会话中按 Ctrl+X f 会中断会话并返回 ErrDetach。
// 返回 (跟踪到的远程工作目录, error)；cwd 为空串表示未跟踪到（可安全降级）。
func StartInteractive(c *sshc.Client) (string, error) {
	fd := os.Stdin.Fd()
	old, err := term.MakeRaw(fd)
	if err == nil {
		defer func() { _ = term.Restore(fd, old) }()
	}
	in, cleanup := newInteractiveInput()
	if cleanup != nil {
		defer cleanup()
	}
	rows, cols, _ := term.GetSize(os.Stdout.Fd())
	if rows <= 0 || cols <= 0 {
		rows, cols = 24, 80
	}
	return runSession(c, in, os.Stdout, os.Stderr, rows, cols)
}

// runSession 核心透传逻辑（in/out 可注入以便测试）。
// 本地输入经热键检测后转发远程；检测到 Ctrl+X f 时中断会话并返回 ErrDetach。
// 返回 (跟踪到的远程工作目录, error)。
func runSession(c *sshc.Client, in io.Reader, out, errOut io.Writer, rows, cols int) (string, error) {
	termName := os.Getenv("TERM")
	if termName == "" {
		termName = "xterm-256color"
	}
	s, err := c.NewTerminalSession(termName, rows, cols)
	if err != nil {
		return "", fmt.Errorf("请求远程 PTY 失败: %w", err)
	}
	defer s.Close()

	// 透传 UTF-8 相关 locale（对齐 OpenSSH 客户端默认 SendEnv LANG LC_*）。
	// 不传时远程落到 C/POSIX locale：readline 按字节处理多字节输入，
	// 中文输入后光标位置计算错乱（表现为跳动/错位），ls/vim 中 CJK 宽度对齐也全乱。
	for k, v := range sessionEnv() {
		_ = s.Setenv(k, v)
	}
	// 注入 cwd 追踪钩子（OSC 133;cwd）：远程 bash 每次提示符前上报当前目录。
	// 被远程 .bashrc 覆盖时自动失跟踪（安全降级）。
	_ = s.Setenv("PROMPT_COMMAND", oscPromptCommand)

	tracker := newOSCTracker(out)
	s.Stdout = tracker
	s.Stderr = errOut
	inPipe, err := s.StdinPipe()
	if err != nil {
		return "", fmt.Errorf("获取会话输入失败: %w", err)
	}

	if err := s.Shell(); err != nil {
		return "", fmt.Errorf("启动远程 shell 失败: %w", err)
	}

	stopResize := startResize(s)
	defer stopResize()

	stop := make(chan struct{})
	if sp, ok := in.(stoppable); ok {
		sp.setStop(stop)
	}
	var detach atomic.Bool
	go pumpInput(s, inPipe, in, stop, &detach)

	err = s.Wait()
	close(stop)
	if detach.Load() {
		return tracker.Cwd(), ErrDetach
	}
	if err == nil {
		return "", nil
	}
	// 远程进程正常退出（含 exit 0/非 0、信号）视为正常结束
	var exitErr *ssh.ExitError
	var missingErr *ssh.ExitMissingError
	if errors.As(err, &exitErr) || errors.As(err, &missingErr) {
		return "", nil
	}
	if errors.Is(err, io.EOF) {
		return "", nil
	}
	return "", fmt.Errorf("会话异常结束: %w", err)
}

// stoppable 支持在 detach 时主动停止的输入源（unix 非阻塞轮询 stdin 实现）
type stoppable interface {
	setStop(chan struct{})
}

// pumpInput 读取本地输入，经热键检测后转发远程会话。
// 检测到 Ctrl+X f 时中断会话（detach）；输入流结束或 stop 关闭时退出并发送 EOF。
func pumpInput(s *ssh.Session, w io.WriteCloser, in io.Reader, stop chan struct{}, detach *atomic.Bool) {
	defer w.Close()
	var sc detachScanner
	buf := make([]byte, 512)
	for {
		select {
		case <-stop:
			return
		default:
		}
		n, err := in.Read(buf)
		if err != nil {
			return
		}
		fwd, hit := sc.feed(buf[:n])
		if len(fwd) > 0 {
			if _, werr := w.Write(fwd); werr != nil {
				return
			}
		}
		if hit {
			detach.Store(true)
			_ = s.Close()
			return
		}
	}
}

// detachScanner 前缀状态机：检测 Ctrl+X f，输出应转发的字节
type detachScanner struct {
	pending bool
}

func (sc *detachScanner) feed(data []byte) (forward []byte, detach bool) {
	for _, b := range data {
		if sc.pending {
			sc.pending = false
			if b == detachFKey {
				return forward, true
			}
			forward = append(forward, detachCtrlX)
		}
		if b == detachCtrlX {
			sc.pending = true
		} else {
			forward = append(forward, b)
		}
	}
	return forward, false
}
