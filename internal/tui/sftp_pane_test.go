package tui

import (
	"os"
	"path"
	"path/filepath"
	"testing"

	tea "charm.land/bubbletea/v2"

	"simple-connect/internal/testutil"
)

// TestSFTPRemoteCwdFromSession 验证热键唤起时远程栏定位到会话内跟踪的目录
func TestSFTPRemoteCwdFromSession(t *testing.T) {
	env := testutil.StartSFTP(t)
	m := newTestSFTPModel(t, env)
	m.remoteCwd = "/var/log/nginx"
	connect(t, m)
	defer m.close()

	if m.cwd != "/var/log/nginx" {
		t.Fatalf("SFTP 应定位到会话 cwd /var/log/nginx，实际 %q", m.cwd)
	}
}

// TestSFTPDualPaneFocus 验证双栏结构：默认焦点远程栏，Tab 切换焦点
func TestSFTPDualPaneFocus(t *testing.T) {
	env := testutil.StartSFTP(t)
	m := newTestSFTPModel(t, env)
	connect(t, m)
	defer m.close()

	if m.focus != paneRemote {
		t.Fatalf("默认焦点应在远程栏，实际 %d", m.focus)
	}

	// Tab 切到本地
	next, _ := m.handleKey(pressKey(tea.KeyTab).(tea.KeyPressMsg))
	if next.focus != paneLocal {
		t.Fatalf("Tab 后焦点应在本地栏，实际 %d", next.focus)
	}
	// 再 Tab 切回远程
	next, _ = next.handleKey(pressKey(tea.KeyTab).(tea.KeyPressMsg))
	if next.focus != paneRemote {
		t.Fatalf("二次 Tab 焦点应回远程栏，实际 %d", next.focus)
	}
}

// TestSFTPLocalNavigation 本地栏浏览：列目录、进目录、上级、排序
func TestSFTPLocalNavigation(t *testing.T) {
	env := testutil.StartSFTP(t)
	m := newTestSFTPModel(t, env)
	defer m.close()

	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "subdir"), 0o755)
	_ = os.WriteFile(filepath.Join(root, "a.txt"), []byte("x"), 0o644)
	m.localCwd = root
	m.focus = paneLocal

	// 刷新本地列表
	cmd := m.loadLocal()
	lm, ok := cmd().(sftpListMsg)
	if !ok || lm.err != nil {
		t.Fatalf("本地列表失败: %v", lm.err)
	}
	next, _ := m.Update(lm)
	if len(next.localEntries) != 2 {
		t.Fatalf("本地应列出 2 项，实际 %d", len(next.localEntries))
	}
	// 目录优先
	if !next.localEntries[0].IsDir() || next.localEntries[0].Name() != "subdir" {
		t.Fatalf("本地目录应排在最前: %v", next.localEntries[0].Name())
	}

	// 进入目录（返回刷新列表命令）
	next.localCursor = 0
	next, c := next.enterCurrent()
	if c == nil {
		t.Fatal("进入目录应产生刷新命令")
	}
	if next.localCwd != path.Join(root, "subdir") {
		t.Fatalf("本地应进入 %s，实际 %s", path.Join(root, "subdir"), next.localCwd)
	}

	// 上级
	next, _ = next.goUp()
	if next.localCwd != root {
		t.Fatalf("本地应返回 %s，实际 %s", root, next.localCwd)
	}
}

// TestSFTPLocalEnterUpload 焦点本地栏对文件 Enter → 上传到远程栏当前目录
func TestSFTPLocalEnterUpload(t *testing.T) {
	env := testutil.StartSFTP(t)
	m := newTestSFTPModel(t, env)
	connect(t, m)
	defer m.close()
	m.cwd = env.Root

	// 本地源文件
	src := filepath.Join(m.localCwd, "pane-src.bin")
	content := make([]byte, 4096)
	for i := range content {
		content[i] = byte(i % 253)
	}
	if err := os.WriteFile(src, content, 0o644); err != nil {
		t.Fatal(err)
	}

	// 本地栏刷新并选中文件，Enter 上传
	m.focus = paneLocal
	lm := m.loadLocal()
	next, _ := m.Update(lm())
	for i, e := range next.localEntries {
		if e.Name() == "pane-src.bin" {
			next.localCursor = i
		}
	}
	next, start := next.enterCurrent()
	if start == nil {
		t.Fatal("本地栏对文件 Enter 应触发上传命令")
	}
	m = driveProgress(t, next, start)

	// 校验远程出现文件
	if b, err := os.ReadFile(filepath.Join(env.Root, "pane-src.bin")); err != nil || string(b) != string(content) {
		t.Fatalf("远程上传校验失败: %v", err)
	}
}

// TestSFTPRemoteEnterDownload 焦点远程栏对文件 Enter → 下载到本地栏当前目录
func TestSFTPRemoteEnterDownload(t *testing.T) {
	env := testutil.StartSFTP(t)
	m := newTestSFTPModel(t, env)
	connect(t, m)
	defer m.close()
	m.cwd = env.Root

	// 预置远程文件
	if err := os.WriteFile(filepath.Join(env.Root, "dl.bin"), []byte("download-me"), 0o644); err != nil {
		t.Fatal(err)
	}
	m.entries = nil
	lm := m.loadList()
	next, _ := m.Update(lm())
	for i, e := range next.entries {
		if e.Name() == "dl.bin" {
			next.cursor = i
		}
	}

	// 焦点远程栏（默认），Enter 下载到本地栏当前目录
	if next.focus != paneRemote {
		t.Fatalf("焦点应在远程栏: %d", next.focus)
	}
	next, start := next.enterCurrent()
	if start == nil {
		t.Fatal("远程栏对文件 Enter 应触发下载命令")
	}
	m = driveProgress(t, next, start)

	if b, err := os.ReadFile(filepath.Join(m.localCwd, "dl.bin")); err != nil || string(b) != "download-me" {
		t.Fatalf("下载校验失败: %v", err)
	}
}