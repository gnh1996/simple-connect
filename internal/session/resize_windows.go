//go:build windows

package session

import (
	"os"
	"time"

	"github.com/charmbracelet/x/term"
	"golang.org/x/crypto/ssh"
)

// startResize Windows 下无 SIGWINCH，改用轮询检测窗口尺寸变化并转发到远程 PTY
func startResize(s *ssh.Session) func() {
	done := make(chan struct{})
	lastR, lastC := 0, 0
	go func() {
		t := time.NewTicker(500 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				if rows, cols, err := term.GetSize(os.Stdout.Fd()); err == nil {
					if rows != lastR || cols != lastC {
						lastR, lastC = rows, cols
						_ = s.WindowChange(rows, cols)
					}
				}
			case <-done:
				return
			}
		}
	}()
	return func() { close(done) }
}
