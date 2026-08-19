//go:build windows

package store

import (
	"os"

	"golang.org/x/sys/windows"
)

// fileLock 持有对锁文件的 LockFileEx 句柄。
// 字节范围锁按句柄生效：同一进程内不同句柄的锁也会互斥，
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
	ol := &windows.Overlapped{}
	flags := uint32(0)
	if !shared {
		flags = windows.LOCKFILE_EXCLUSIVE_LOCK
	}
	if err := windows.LockFileEx(windows.Handle(f.Fd()), flags, 0, 1, 0, ol); err != nil {
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
	ol := &windows.Overlapped{}
	_ = windows.UnlockFileEx(windows.Handle(l.f.Fd()), 0, 1, 0, ol)
	_ = l.f.Close()
	l.f = nil
}