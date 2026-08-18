//go:build !windows

package session

import (
	"io"
	"os"
	"syscall"
	"time"
)

// pollInput 非阻塞轮询读取 stdin，便于会话 detach 时干净退出（无需等待按键）
type pollInput struct {
	fd   uintptr
	stop chan struct{}
}

func (p *pollInput) setStop(c chan struct{}) { p.stop = c }

func (p *pollInput) Read(b []byte) (int, error) {
	for {
		select {
		case <-p.stop:
			return 0, io.EOF
		default:
		}
		n, err := syscall.Read(int(p.fd), b)
		if err == syscall.EAGAIN || err == syscall.EWOULDBLOCK {
			time.Sleep(10 * time.Millisecond)
			continue
		}
		return n, err
	}
}

// newInteractiveInput 创建会话期间的 stdin 输入源（unix：非阻塞轮询，detach 时可立即退出）
func newInteractiveInput() (io.Reader, func()) {
	fd := os.Stdin.Fd()
	_ = syscall.SetNonblock(int(fd), true)
	return &pollInput{fd: fd}, func() { _ = syscall.SetNonblock(int(fd), false) }
}
