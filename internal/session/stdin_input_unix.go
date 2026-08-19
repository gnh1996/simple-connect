//go:build !windows

package session

import (
	"io"
	"os"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// pollInput 基于 poll(2) + 自管道（self-pipe）的 stdin 输入源：
// 阻塞等待 stdin 可读，同时监听停止管道——detach 时写入管道唤醒阻塞的 poll，
// 干净退出。相比旧的 10ms 忙等轮询，无轮询延迟、无粘贴吞吐瓶颈（原 ≈51KB/s）。
type pollInput struct {
	fd    int
	stopR *os.File // 停止管道读端（poll 监听）
	stopW *os.File // 停止管道写端（setStop goroutine 写入以唤醒 poll）
}

// setStop 注册 detach 停止信号：channel 关闭时向自管道写一字节唤醒阻塞的 poll。
func (p *pollInput) setStop(ch chan struct{}) {
	go func() {
		<-ch
		_, _ = p.stopW.Write([]byte{0})
	}()
}

func (p *pollInput) Read(b []byte) (int, error) {
	fds := []unix.PollFd{
		{Fd: int32(p.fd), Events: unix.POLLIN | unix.POLLHUP | unix.POLLERR},
		{Fd: int32(p.stopR.Fd()), Events: unix.POLLIN | unix.POLLHUP | unix.POLLERR},
	}
	for {
		n, err := unix.Poll(fds, -1)
		if err != nil {
			if err == unix.EINTR {
				continue
			}
			return 0, err
		}
		if n <= 0 {
			continue
		}
		// 停止管道就绪（含挂起/关闭）→ 返回 EOF 让 pumpInput 干净退出
		if fds[1].Revents&(unix.POLLIN|unix.POLLHUP|unix.POLLERR) != 0 {
			return 0, io.EOF
		}
		// stdin 就绪 → 读取数据（fd 为阻塞模式，poll 已保证有数据可读）
		nn, rerr := syscall.Read(int(p.fd), b)
		if nn == 0 && rerr == nil {
			return 0, io.EOF // 管道 EOF（syscall.Read 返回 0,nil，需转 io.EOF）
		}
		if rerr == syscall.EAGAIN || rerr == syscall.EWOULDBLOCK {
			continue
		}
		return nn, rerr
	}
}

// newInteractiveInput 创建会话期间的 stdin 输入源（unix：poll 阻塞 + 自管道唤醒）。
// 自管道创建失败（极罕见）时退回忙等轮询兜底，保证功能可用。
func newInteractiveInput() (io.Reader, func()) {
	fd := int(os.Stdin.Fd())
	r, w, err := os.Pipe()
	if err != nil {
		// 兜底：忙等轮询依赖非阻塞 fd
		_ = syscall.SetNonblock(fd, true)
		return &busyInput{fd: fd}, func() { _ = syscall.SetNonblock(fd, false) }
	}
	return &pollInput{fd: fd, stopR: r, stopW: w}, func() {
		_ = r.Close()
		_ = w.Close()
	}
}

// busyInput 兜底实现：自管道创建失败时退回旧的 10ms 非阻塞忙等轮询。
type busyInput struct {
	fd   int
	stop chan struct{}
}

func (b *busyInput) setStop(c chan struct{}) { b.stop = c }

func (b *busyInput) Read(p []byte) (int, error) {
	for {
		select {
		case <-b.stop:
			return 0, io.EOF
		default:
		}
		n, err := syscall.Read(int(b.fd), p)
		if err == syscall.EAGAIN || err == syscall.EWOULDBLOCK {
			time.Sleep(10 * time.Millisecond)
			continue
		}
		return n, err
	}
}
