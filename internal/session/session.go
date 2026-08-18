package session

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/charmbracelet/x/term"
	"golang.org/x/crypto/ssh"

	sshc "simple-connect/internal/ssh"
)

// StartInteractive 建立自建交互式 SSH 会话。
// 认证已由 sshc.Connect 使用保存的凭据完成；这里负责将本地终端置为 raw 模式，
// 并与远程 PTY 做字节透传，结束后恢复终端。
func StartInteractive(c *sshc.Client) error {
	fd := os.Stdin.Fd()
	old, err := term.MakeRaw(fd)
	if err == nil {
		defer func() { _ = term.Restore(fd, old) }()
	}
	rows, cols, _ := term.GetSize(os.Stdout.Fd())
	if rows <= 0 || cols <= 0 {
		rows, cols = 24, 80
	}
	return runSession(c, os.Stdin, os.Stdout, os.Stderr, rows, cols)
}

// runSession 核心透传逻辑（in/out 可注入以便测试）
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

	s.Stdin = in
	s.Stdout = out
	s.Stderr = errOut

	if err := s.Shell(); err != nil {
		return fmt.Errorf("启动远程 shell 失败: %w", err)
	}

	stopResize := startResize(s)
	defer stopResize()

	err = s.Wait()
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
