//go:build windows

package session

import (
	"io"
	"os"
	"sync"
	"time"

	"golang.org/x/sys/windows"
)

// cancelableStdin 是 Windows 下可被停止的 stdin 输入源。
//
// Windows 控制台/管道无法非阻塞读取，会话结束（远程 shell 退出）或 detach 时
// 阻塞在 ReadFile 的读取线程必须被唤醒：否则它会在回到 TUI 后继续等待，把用户
// 按下的第一个键读走（该键既到不了 TUI，也会被写向已结束的会话），表现为"吞键"。
//
// 唤醒方式与 bubbletea/ultraviolet 的 Windows cancelreader 相同：CancelIoEx
// 取消该句柄上挂起的同步读取。取消导致 Read 返回 ERROR_OPERATION_ABORTED，
// 这里统一转换为 io.EOF（pumpInput 见 stop 已关闭时不会据此向远程发 EOF）。
//
// CancelIoEx 与 Read 发起之间存在极小窗口（取消先到、读取后到），因此取消方在
// 读取线程确认退出前重试取消；inRead/canceled 均由 mu 保护，不存在"看到未在读、
// 随后又进入阻塞读"的漏判。
type cancelableStdin struct {
	f *os.File

	mu       sync.Mutex
	canceled bool
	inRead   bool
}

// cancelIoRetryInterval 是补偿「取消早于读取发起」窗口的重试间隔。
// 仅在读取线程仍在 ReadFile 中时循环，正常路径一次调用即结束。
const cancelIoRetryInterval = 2 * time.Millisecond

// cancelIoRetryLimit 是补偿重试的上限：避免 CancelIoEx 持续失败时无限重试。
const cancelIoRetryLimit = time.Second

func newInteractiveInput() (io.Reader, func()) {
	return newCancelableInput(os.Stdin), func() {}
}

// newCancelableInput 包装任意文件句柄（测试用管道模拟 stdin）。
func newCancelableInput(f *os.File) *cancelableStdin {
	return &cancelableStdin{f: f}
}

// setStop 在 stop 关闭时取消挂起的读取，使 Read 立即返回 io.EOF。
func (r *cancelableStdin) setStop(stop chan struct{}) {
	go func() {
		<-stop
		r.mu.Lock()
		r.canceled = true
		inRead := r.inRead
		r.mu.Unlock()
		if !inRead {
			return // 读取泵已退出（或尚未发起读取，Read 会直接看到 canceled）
		}
		deadline := time.Now().Add(cancelIoRetryLimit)
		for {
			// 句柄可能属于控制台或管道；两者均支持 CancelIoEx 取消同步读取。
			_ = windows.CancelIoEx(windows.Handle(r.f.Fd()), nil)
			r.mu.Lock()
			inRead = r.inRead
			r.mu.Unlock()
			if !inRead || time.Now().After(deadline) {
				return
			}
			time.Sleep(cancelIoRetryInterval)
		}
	}()
}

// Read 阻塞读取 stdin；被 setStop 取消时返回 io.EOF。
func (r *cancelableStdin) Read(p []byte) (int, error) {
	r.mu.Lock()
	if r.canceled {
		r.mu.Unlock()
		return 0, io.EOF
	}
	r.inRead = true
	r.mu.Unlock()

	n, err := r.f.Read(p)

	r.mu.Lock()
	r.inRead = false
	canceled := r.canceled
	r.mu.Unlock()
	if err != nil && canceled {
		// CancelIoEx 的 ERROR_OPERATION_ABORTED 不是输入流结束：不向远程发 EOF
		return 0, io.EOF
	}
	return n, err
}
