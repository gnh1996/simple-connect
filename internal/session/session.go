package session

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sync/atomic"

	"github.com/charmbracelet/x/term"
	"golang.org/x/crypto/ssh"

	sshc "simple-connect/internal/ssh"
)

// ErrDetach 表示用户在会话中按热键请求切换到 SFTP
var ErrDetach = errors.New("用户请求切换到 SFTP")

// 会话中唤起 SFTP 的热键序列：Ctrl+X 后按 f
const (
	detachCtrlX = 0x18
	detachFKey  = 'f'
)

// StartInteractive 建立自建交互式 SSH 会话。
// 认证已由 sshc.Connect 使用保存的凭据完成；这里负责将本地终端置为 raw 模式，
// 与远程 PTY 做字节透传，结束后恢复终端。
// 会话中按 Ctrl+X f 会中断会话并返回 ErrDetach。
func StartInteractive(c *sshc.Client) error {
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
func runSession(c *sshc.Client, in io.Reader, out, errOut io.Writer, rows, cols int) error {
	termName := os.Getenv("TERM")
	if termName == "" {
		termName = "xterm-256color"
	}
	s, err := c.NewTerminalSession(termName, rows, cols)
	if err != nil {
		return fmt.Errorf("请求远程 PTY 失败: %w", err)
	}
	defer s.Close()

	s.Stdout = out
	s.Stderr = errOut
	inPipe, err := s.StdinPipe()
	if err != nil {
		return fmt.Errorf("获取会话输入失败: %w", err)
	}

	if err := s.Shell(); err != nil {
		return fmt.Errorf("启动远程 shell 失败: %w", err)
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
		return ErrDetach
	}
	if err == nil {
		return nil
	}
	// 远程进程正常退出（含 exit 0/非 0、信号）视为正常结束
	var exitErr *ssh.ExitError
	var missingErr *ssh.ExitMissingError
	if errors.As(err, &exitErr) || errors.As(err, &missingErr) {
		return nil
	}
	if errors.Is(err, io.EOF) {
		return nil
	}
	return fmt.Errorf("会话异常结束: %w", err)
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
