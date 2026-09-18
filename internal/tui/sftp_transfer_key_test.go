package tui

import (
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"testing"

	tea "charm.land/bubbletea/v2"

	"simple-connect/internal/testutil"
)

// findEntry 返回列表中指定名字的下标（测试辅助）
func findEntry(t *testing.T, entries []fs.FileInfo, name string) int {
	t.Helper()
	for i, e := range entries {
		if e.Name() == name {
			return i
		}
	}
	t.Fatalf("未找到条目 %s", name)
	return -1
}

// TestSFTPTransferKeyLocalUpload t 键：本地栏光标文件/目录 → 上传远程
func TestSFTPTransferKeyLocalUpload(t *testing.T) {
	env := testutil.StartSFTP(t)
	m := newTestSFTPModel(t, env)
	connect(t, m)
	defer m.close()
	m.cwd = env.Root
	m.focus = paneLocal

	if err := os.WriteFile(filepath.Join(m.localCwd, "t-up.bin"), []byte("up"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(m.localCwd, "t-dir")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "nested.txt"), []byte("nested"), 0o644); err != nil {
		t.Fatal(err)
	}

	next, _ := m.Update(m.loadLocal()())
	next.localCursor = findEntry(t, next.localEntries, "t-up.bin")
	next, cmd := next.handleKey(press("t").(tea.KeyPressMsg))
	if cmd == nil {
		t.Fatal("本地栏 t 应触发上传命令")
	}
	next = driveWithOverwriteCheck(t, next, cmd)
	if b, err := os.ReadFile(filepath.Join(env.Root, "t-up.bin")); err != nil || string(b) != "up" {
		t.Fatalf("t 上传文件失败: %v", err)
	}

	// 目录递归上传
	next, _ = next.Update(next.loadLocal()())
	next.localCursor = findEntry(t, next.localEntries, "t-dir")
	next, cmd = next.handleKey(press("t").(tea.KeyPressMsg))
	next = driveWithOverwriteCheck(t, next, cmd)
	if b, err := os.ReadFile(filepath.Join(env.Root, "t-dir", "nested.txt")); err != nil || string(b) != "nested" {
		t.Fatalf("t 递归上传目录失败: %v", err)
	}
}

// TestSFTPTransferKeyRemoteDownload t 键：远程栏光标文件/目录 → 下载本地
func TestSFTPTransferKeyRemoteDownload(t *testing.T) {
	env := testutil.StartSFTP(t)
	m := newTestSFTPModel(t, env)
	connect(t, m)
	defer m.close()
	m.cwd = env.Root
	m.focus = paneRemote

	if err := os.WriteFile(filepath.Join(env.Root, "t-dl.txt"), []byte("dl"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(env.Root, "t-rdir")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "in.txt"), []byte("in"), 0o644); err != nil {
		t.Fatal(err)
	}

	next, _ := m.Update(m.loadList()())
	next.cursor = findEntry(t, next.entries, "t-dl.txt")
	next, cmd := next.handleKey(press("t").(tea.KeyPressMsg))
	next = driveWithOverwriteCheck(t, next, cmd)
	if b, err := os.ReadFile(filepath.Join(next.localCwd, "t-dl.txt")); err != nil || string(b) != "dl" {
		t.Fatalf("t 下载文件失败: %v", err)
	}

	next, _ = next.Update(next.loadList()())
	next.cursor = findEntry(t, next.entries, "t-rdir")
	next, cmd = next.handleKey(press("t").(tea.KeyPressMsg))
	next = driveWithOverwriteCheck(t, next, cmd)
	if b, err := os.ReadFile(filepath.Join(next.localCwd, "t-rdir", "in.txt")); err != nil || string(b) != "in" {
		t.Fatalf("t 递归下载目录失败: %v", err)
	}
}

// TestSFTPTransferKeyBatch t 键：有 Space 多选时走批量传输
func TestSFTPTransferKeyBatch(t *testing.T) {
	env := testutil.StartSFTP(t)
	m := newTestSFTPModel(t, env)
	connect(t, m)
	defer m.close()
	m.cwd = env.Root
	m.focus = paneLocal

	for _, n := range []string{"b1.txt", "b2.txt"} {
		if err := os.WriteFile(filepath.Join(m.localCwd, n), []byte(n), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	next, _ := m.Update(m.loadLocal()())
	next.selLocal = map[int]struct{}{
		findEntry(t, next.localEntries, "b1.txt"): {},
		findEntry(t, next.localEntries, "b2.txt"): {},
	}
	next, cmd := next.handleKey(press("t").(tea.KeyPressMsg))
	if cmd == nil {
		t.Fatal("有选中项时 t 应触发批量传输命令")
	}
	next = driveWithOverwriteCheck(t, next, cmd)
	for _, n := range []string{"b1.txt", "b2.txt"} {
		if b, err := os.ReadFile(filepath.Join(env.Root, n)); err != nil || string(b) != n {
			t.Fatalf("批量上传 %s 失败: %v", n, err)
		}
	}
}

// TestSFTPTransferKeyNoEntry 空列表按 t 应无操作、不 panic
func TestSFTPTransferKeyNoEntry(t *testing.T) {
	env := testutil.StartSFTP(t)
	m := newTestSFTPModel(t, env)
	defer m.close()
	m.focus = paneRemote
	m.entries = nil
	next, cmd := m.handleKey(press("t").(tea.KeyPressMsg))
	if cmd != nil {
		t.Fatal("空列表按 t 不应产生命令")
	}
	if next.busy {
		t.Fatal("空列表按 t 不应置忙")
	}
}

// TestSFTPPathUpload p 键：本地栏输入路径 → 上传到远程栏当前目录
func TestSFTPPathUpload(t *testing.T) {
	env := testutil.StartSFTP(t)
	m := newTestSFTPModel(t, env)
	connect(t, m)
	defer m.close()
	m.cwd = env.Root
	m.focus = paneLocal

	src := filepath.Join(m.localCwd, "p-up.txt")
	if err := os.WriteFile(src, []byte("pup"), 0o644); err != nil {
		t.Fatal(err)
	}

	next, _ := m.openPath()
	if next.mode != modePath {
		t.Fatalf("p 应进入 modePath，实际 %d", next.mode)
	}
	if next.pathIn.Value() != "" {
		t.Fatalf("p 不应预填路径，实际 %q", next.pathIn.Value())
	}
	next.pathIn.SetValue(src)
	next, cmd := next.pathJump()
	if next.mode != modeBrowse {
		t.Fatalf("提交后应回到浏览模式，实际 %d", next.mode)
	}
	next = driveWithOverwriteCheck(t, next, cmd)
	if b, err := os.ReadFile(filepath.Join(env.Root, "p-up.txt")); err != nil || string(b) != "pup" {
		t.Fatalf("p 上传失败: %v", err)
	}
}

// TestSFTPPathDownload p 键：远程栏输入路径 → 下载到本地栏当前目录（含目录递归）
func TestSFTPPathDownload(t *testing.T) {
	env := testutil.StartSFTP(t)
	m := newTestSFTPModel(t, env)
	connect(t, m)
	defer m.close()
	m.focus = paneRemote

	remoteFile := filepath.Join(env.Root, "p-dl.txt")
	if err := os.WriteFile(remoteFile, []byte("pdl"), 0o644); err != nil {
		t.Fatal(err)
	}
	remoteDir := filepath.Join(env.Root, "p-dldir")
	if err := os.MkdirAll(remoteDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(remoteDir, "deep.txt"), []byte("deep"), 0o644); err != nil {
		t.Fatal(err)
	}

	next, _ := m.openPath()
	next.pathIn.SetValue(remoteFile)
	next, cmd := next.pathJump()
	next = driveWithOverwriteCheck(t, next, cmd)
	if b, err := os.ReadFile(filepath.Join(next.localCwd, "p-dl.txt")); err != nil || string(b) != "pdl" {
		t.Fatalf("p 下载文件失败: %v", err)
	}

	next, _ = next.openPath()
	next.pathIn.SetValue(remoteDir)
	next, cmd = next.pathJump()
	next = driveWithOverwriteCheck(t, next, cmd)
	if b, err := os.ReadFile(filepath.Join(next.localCwd, "p-dldir", "deep.txt")); err != nil || string(b) != "deep" {
		t.Fatalf("p 递归下载目录失败: %v", err)
	}
}

// TestSFTPPathCompleteLocal p 键本地栏 Tab 补全：候选计算与循环切换
func TestSFTPPathCompleteLocal(t *testing.T) {
	env := testutil.StartSFTP(t)
	m := newTestSFTPModel(t, env)
	defer m.close()
	m.focus = paneLocal

	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "alpha.txt"), []byte("x"), 0o644)
	_ = os.WriteFile(filepath.Join(root, "alpine.txt"), []byte("x"), 0o644)
	m.localCwd = root

	next, _ := m.openPath()
	next.pathIn.SetValue("al")
	// 经 handleKey 的 Tab 分发，验证按键接线
	next, cmd := next.handleKey(pressKey(tea.KeyTab).(tea.KeyPressMsg))
	if cmd == nil {
		t.Fatal("p 模式 Tab 应产生补全命令")
	}
	cm, ok := cmd().(sftpGotoCompleteMsg)
	if !ok {
		t.Fatalf("补全命令应返回 sftpGotoCompleteMsg，实际 %T", cmd())
	}
	if cm.target != modePath || cm.err != nil || len(cm.cands) != 2 {
		t.Fatalf("p 本地补全异常: target=%d err=%v cands=%v", cm.target, cm.err, cm.cands)
	}
	next, _ = next.Update(cm)
	if len(next.pathCandidates) != 2 {
		t.Fatalf("候选应写入 pathCandidates，实际 %v", next.pathCandidates)
	}
	want1 := filepath.Join(root, "alpha.txt")
	if next.pathIn.Value() != want1 {
		t.Fatalf("首次补全应填入 %s，实际 %s", want1, next.pathIn.Value())
	}
	next, _ = next.pathComplete()
	if next.pathIn.Value() != filepath.Join(root, "alpine.txt") {
		t.Fatalf("二次 Tab 应填入 alpine.txt，实际 %s", next.pathIn.Value())
	}
}

// TestSFTPPathCompleteRemote p 键远程栏 Tab 补全走 sftp 服务端列表
func TestSFTPPathCompleteRemote(t *testing.T) {
	env := testutil.StartSFTP(t)
	m := newTestSFTPModel(t, env)
	connect(t, m)
	defer m.close()
	m.focus = paneRemote
	m.cwd = env.Root

	_ = os.WriteFile(filepath.Join(env.Root, "alpha.txt"), []byte("x"), 0o644)
	_ = os.WriteFile(filepath.Join(env.Root, "alpine.txt"), []byte("x"), 0o644)

	next, _ := m.openPath()
	next.pathIn.SetValue("al")
	next, cmd := next.pathComplete()
	cm, ok := cmd().(sftpGotoCompleteMsg)
	if !ok {
		t.Fatalf("补全命令应返回 sftpGotoCompleteMsg，实际 %T", cmd())
	}
	if cm.target != modePath || cm.err != nil || len(cm.cands) != 2 {
		t.Fatalf("p 远程补全异常: target=%d err=%v cands=%v", cm.target, cm.err, cm.cands)
	}
	next, _ = next.Update(cm)
	if next.pathIn.Value() != path.Join(env.Root, "alpha.txt") {
		t.Fatalf("远程补全应填入 alpha.txt，实际 %s", next.pathIn.Value())
	}
}

// TestSFTPPathEscCancel p 输入 Esc 取消回到浏览模式
func TestSFTPPathEscCancel(t *testing.T) {
	env := testutil.StartSFTP(t)
	m := newTestSFTPModel(t, env)
	defer m.close()

	next, _ := m.openPath()
	next, _ = next.handleKey(pressKey(tea.KeyEsc).(tea.KeyPressMsg))
	if next.mode != modeBrowse {
		t.Fatalf("Esc 应取消输入，实际 %d", next.mode)
	}
}

// TestSFTPPathEmptyEnterCancel p 空输入 Enter 应取消且不传输
func TestSFTPPathEmptyEnterCancel(t *testing.T) {
	env := testutil.StartSFTP(t)
	m := newTestSFTPModel(t, env)
	defer m.close()
	m.focus = paneLocal

	next, _ := m.openPath()
	next, cmd := next.handleKey(pressKey(tea.KeyEnter).(tea.KeyPressMsg))
	if cmd != nil {
		t.Fatal("空输入 Enter 不应产生传输命令")
	}
	if next.mode != modeBrowse {
		t.Fatalf("空输入 Enter 应回到浏览模式，实际 %d", next.mode)
	}
}
