//go:build !windows

package store

import (
	"os"

	"golang.org/x/sys/unix"
)

// fileLock 持有对锁文件的 flock 句柄。
// 锁与"打开文件描述"绑定：同一进程内不同 fd 的 flock 也会互斥，
// 因此可同时用于跨进程与进程内多实例的并发控制。
type fileLock struct {
	f *os.File
}

// acquireLock 打开锁文件并加锁。shared=true 为共享锁（并发读），否则为排他锁（读-改-写）。
func acquireLock(path string, shared bool) (*fileLock, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	how := unix.LOCK_EX
	if shared {
		how = unix.LOCK_SH
	}
	if err := unix.Flock(int(f.Fd()), how); err != nil {
		_ = f.Close()
		return nil, err
	}
	return &fileLock{f: f}, nil
}

// release 解锁并关闭文件（进程退出时内核自动释放，无残留锁风险）
func (l *fileLock) release() {
	if l == nil || l.f == nil {
		return
	}
	_ = unix.Flock(int(l.f.Fd()), unix.LOCK_UN)
	_ = l.f.Close()
	l.f = nil
}