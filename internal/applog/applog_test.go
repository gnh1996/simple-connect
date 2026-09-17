package applog

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// resetLogger 将全局 logger 恢复为丢弃，避免用例间相互污染。
func resetLogger(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		Close()
		logger = log.New(io.Discard, "", 0)
	})
}

// TestOpenWritesLogFile 验证 open 创建目录/文件、权限正确且 Errorf 写入内容。
func TestOpenWritesLogFile(t *testing.T) {
	resetLogger(t)
	dir := filepath.Join(t.TempDir(), "simple-connect")

	if err := open(dir, maxLogSize); err != nil {
		t.Fatalf("open: %v", err)
	}
	Errorf("hello %d", 42)
	Close()

	path := filepath.Join(dir, logFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取日志: %v", err)
	}
	if !strings.Contains(string(data), "hello 42") {
		t.Fatalf("日志内容缺少写入文本: %q", data)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat 日志: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("日志文件权限应为 0600，实际 %o", perm)
	}
	di, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat 目录: %v", err)
	}
	if perm := di.Mode().Perm(); perm != 0o700 {
		t.Fatalf("日志目录权限应为 0700，实际 %o", perm)
	}
}

// TestOpenRotatesWhenExceedsLimit 验证超出上限时旧日志滚动为 .1。
func TestOpenRotatesWhenExceedsLimit(t *testing.T) {
	resetLogger(t)
	dir := t.TempDir()
	path := filepath.Join(dir, logFileName)
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 100)), 0o600); err != nil {
		t.Fatalf("预置日志: %v", err)
	}

	if err := open(dir, 16); err != nil {
		t.Fatalf("open: %v", err)
	}
	defer Close()

	bak, err := os.ReadFile(path + ".1")
	if err != nil {
		t.Fatalf("读取备份日志: %v", err)
	}
	if len(bak) != 100 {
		t.Fatalf("备份日志应保留旧内容(100 字节)，实际 %d", len(bak))
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat 现日志: %v", err)
	}
	if fi.Size() != 0 {
		t.Fatalf("滚动后现日志应为新建空文件，实际 %d 字节", fi.Size())
	}
}

// TestErrorfBeforeInitDoesNotPanic 验证未初始化/已关闭时 Errorf 为无操作。
func TestErrorfBeforeInitDoesNotPanic(t *testing.T) {
	logger = log.New(io.Discard, "", 0)
	Errorf("should be discarded %d", 1)
	Close()
	Errorf("after close should be discarded")
}
