//go:build !windows

package session

import (
	"os"
	"os/signal"
	"syscall"

	"github.com/charmbracelet/x/term"
	"golang.org/x/crypto/ssh"
)

// startResize 监听 SIGWINCH，将本地终端尺寸变化转发到远程 PTY
func startResize(s *ssh.Session) func() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGWINCH)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-ch:
				if rows, cols, err := term.GetSize(os.Stdout.Fd()); err == nil {
					_ = s.WindowChange(rows, cols)
				}
			case <-done:
				return
			}
		}
	}()
	return func() {
		signal.Stop(ch)
		close(done)
	}
}
