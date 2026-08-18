//go:build windows

package session

import (
	"io"
	"os"
)

// newInteractiveInput 创建会话期间的 stdin 输入源。
// Windows 控制台无法非阻塞读取，detach 后残留的读取 goroutine 会在下次按键时自行退出，
// 代价是 TUI 重新启动后首个按键可能被吞（一次性）。
func newInteractiveInput() (io.Reader, func()) {
	return os.Stdin, func() {}
}
