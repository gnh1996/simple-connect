// Package applog 提供轻量的错误日志落盘能力，便于事后诊断 TUI 运行期错误。
//
// 日志默认写入 <用户缓存目录>/simple-connect/simple-connect.log（文件 0600、目录 0700）。
// 任何初始化失败都只降级为“不落盘”，绝不阻断主流程；终端提示由调用方负责。
package applog

import (
	"io"
	"log"
	"os"
	"path/filepath"
)

const (
	appDirName  = "simple-connect"
	logFileName = "simple-connect.log"
	// maxLogSize 单个日志文件上限；超出后滚动为 .1（仅保留一份备份），避免无限增长。
	maxLogSize = 1 << 20 // 1MiB
)

var (
	logger    = log.New(io.Discard, "", 0)
	logCloser io.Closer
)

// Init 打开默认日志文件。获取用户缓存目录失败或打开失败时静默降级（不记录）。
func Init() {
	dir, err := os.UserCacheDir()
	if err != nil || dir == "" {
		return
	}
	_ = open(filepath.Join(dir, appDirName), maxLogSize)
}

// Close 关闭日志文件（幂等）。关闭后 Errorf 变为无操作。
func Close() {
	if logCloser != nil {
		_ = logCloser.Close()
		logCloser = nil
	}
	logger = log.New(io.Discard, "", 0)
}

// Errorf 写一条错误日志；未初始化或初始化失败时为无操作。
func Errorf(format string, args ...any) {
	logger.Printf(format, args...)
}

// open 打开 dir 下的日志文件并切换 logger。dir 不存在时按 0700 创建。
// limit > 0 且现有文件超过 limit 时，先滚动为 .1。
func open(dir string, limit int64) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(dir, logFileName)
	rotate(path, limit)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	logger = log.New(f, "", log.LstdFlags)
	logCloser = f
	return nil
}

// rotate 当 path 超过 limit 时重命名为 path+".1"（覆盖旧备份）。
func rotate(path string, limit int64) {
	if limit <= 0 {
		return
	}
	info, err := os.Stat(path)
	if err != nil || info.Size() <= limit {
		return
	}
	_ = os.Rename(path, path+".1")
}
