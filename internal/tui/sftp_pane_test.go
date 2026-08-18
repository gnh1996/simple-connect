package tui

import (
	"os"
	"path"
	"path/filepath"
	"strings"
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

// TestSFTPDualPaneRender 验证双栏左右并排渲染：本地/远程标题在同一行、列表行带分隔线、
// 且不因栏宽计算溢出换行（曾上下堆叠 + 右栏被挤出换行）。
func TestSFTPDualPaneRender(t *testing.T) {
	env := testutil.StartSFTP(t)
	m := newTestSFTPModel(t, env)
	connect(t, m)
	defer m.close()

	m.cwd = env.Root
	m.localCwd = t.TempDir()
	_ = os.WriteFile(filepath.Join(m.localCwd, "local.txt"), []byte("x"), 0o644)
	_ = os.WriteFile(filepath.Join(env.Root, "remote.txt"), []byte("y"), 0o644)

	// 刷新两侧列表
	lm := m.loadLocal()
	m, _ = m.Update(lm())
	rl := m.loadList()
	m, _ = m.Update(rl())

	// 模拟 100x30 终端
	m.width, m.height = 100, 30
	content := m.View().Content

	// 本地与远程标题应在同一行（中间只有 " │ "，无换行）
	idxLocal := strings.Index(content, "本地")
	idxRemote := strings.Index(content, "远程")
	if idxLocal < 0 || idxRemote < 0 {
		t.Fatalf("标题应同时包含本地/远程，实际: %q", content)
	}
	sep := strings.Index(content[idxLocal:], " │ ")
	if sep < 0 {
		t.Fatalf("标题行应含分隔线，实际: %q", content)
	}
	// 分隔线到换行之间应出现 "远程"（说明同排）
	lineEnd := strings.Index(content[idxLocal:], "\n")
	if lineEnd < 0 {
		t.Fatal("标题行应换行")
	}
	if !strings.Contains(content[idxLocal:idxLocal+lineEnd], "远程") {
		t.Fatalf("本地与远程标题应左右同排，实际: %q", content[idxLocal:idxLocal+lineEnd])
	}

	// 列表条目两侧都有
	if !strings.Contains(content, "local.txt") || !strings.Contains(content, "remote.txt") {
		t.Fatalf("两栏应各显示条目，实际: %q", content)
	}
}

// TestSFTPComputePaneLayout 验证窄屏下列宽压缩（不折行）：宽度充足时含大小/时间列，
// 过窄时按序丢弃。
func TestSFTPComputePaneLayout(t *testing.T) {
	full := computePaneLayout(40)
	if !full.showSize || !full.showTime {
		t.Fatalf("宽栏应含大小+时间列: %+v", full)
	}
	noTime := computePaneLayout(20)
	if noTime.showTime {
		t.Fatalf("窄栏应丢弃时间列: %+v", noTime)
	}
	noSize := computePaneLayout(10)
	if noSize.showTime || noSize.showSize {
		t.Fatalf("极窄栏应丢弃大小+时间列: %+v", noSize)
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

func indexOfName(entries []os.FileInfo, name string) int {
	for i, e := range entries {
		if e.Name() == name {
			return i
		}
	}
	return -1
}

// TestSFTPGotoLocalJump 本地栏 g 打开路径输入、Enter 跳转
func TestSFTPGotoLocalJump(t *testing.T) {
	env := testutil.StartSFTP(t)
	m := newTestSFTPModel(t, env)
	defer m.close()
	m.focus = paneLocal

	target := filepath.Join(m.localCwd, "goto-dir")
	_ = os.MkdirAll(filepath.Join(target, "sub"), 0o755)
	_ = os.WriteFile(filepath.Join(target, "f.txt"), []byte("x"), 0o644)

	next, _ := m.openGoto()
	if next.mode != modeGoto {
		t.Fatalf("g 应进入路径跳转模式，实际 %d", next.mode)
	}
	next.gotoIn.SetValue(target)
	next, cmd := next.gotoJump()
	if cmd == nil {
		t.Fatal("本地跳转应产生刷新命令")
	}
	// 执行刷新并应用
	lm, ok := cmd().(sftpListMsg)
	if !ok {
		t.Fatalf("刷新命令应返回 sftpListMsg，实际 %T", lm)
	}
	next, _ = next.Update(lm)
	if next.localCwd != target {
		t.Fatalf("应跳转到 %s，实际 %s", target, next.localCwd)
	}
	if next.mode != modeBrowse {
		t.Fatalf("跳转后应回浏览模式，实际 %d", next.mode)
	}
	if len(next.localEntries) != 2 {
		t.Fatalf("目标目录应列出 2 项，实际 %d", len(next.localEntries))
	}
}

// TestSFTPGotoLocalInvalid 本地跳转无效路径应报错且不退出输入
func TestSFTPGotoLocalInvalid(t *testing.T) {
	env := testutil.StartSFTP(t)
	m := newTestSFTPModel(t, env)
	defer m.close()
	m.focus = paneLocal

	next, _ := m.openGoto()
	next.gotoIn.SetValue("/nonexistent/xyz")
	next, cmd := next.gotoJump()
	if cmd != nil {
		t.Fatal("无效路径不应产生刷新命令")
	}
	if next.mode != modeGoto {
		t.Fatalf("无效路径应留在输入模式，实际 %d", next.mode)
	}
	if next.err == "" {
		t.Fatal("应提示路径错误")
	}
}

// TestSFTPGotoRemoteJump 远程栏路径跳转（异步校验）
func TestSFTPGotoRemoteJump(t *testing.T) {
	env := testutil.StartSFTP(t)
	m := newTestSFTPModel(t, env)
	connect(t, m)
	defer m.close()
	m.focus = paneRemote
	m.cwd = env.Root

	target := filepath.Join(env.Root, "jump-dir")
	_ = os.MkdirAll(target, 0o755)
	_ = os.WriteFile(filepath.Join(target, "a.txt"), []byte("x"), 0o644)

	next, _ := m.openGoto()
	next.gotoIn.SetValue(target)
	next, cmd := next.gotoJump()
	if cmd == nil {
		t.Fatal("远程跳转应产生命令")
	}
	jm, ok := cmd().(sftpGotoJumpMsg)
	if !ok {
		t.Fatalf("跳转命令应返回 sftpGotoJumpMsg，实际 %T", jm)
	}
	next, _ = next.Update(jm)
	if next.cwd != target {
		t.Fatalf("远程应跳转到 %s，实际 %s", target, next.cwd)
	}
	if next.mode != modeBrowse {
		t.Fatalf("跳转后应回浏览模式，实际 %d", next.mode)
	}
	if len(next.entries) != 1 {
		t.Fatalf("目标目录应列出 1 项，实际 %d", len(next.entries))
	}
}

// TestSFTPGotoCompleteLocal 本地 Tab 补全：候选计算、自动补全、循环切换
func TestSFTPGotoCompleteLocal(t *testing.T) {
	env := testutil.StartSFTP(t)
	m := newTestSFTPModel(t, env)
	defer m.close()
	m.focus = paneLocal

	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "alpha.txt"), []byte("x"), 0o644)
	_ = os.WriteFile(filepath.Join(root, "alpine.txt"), []byte("x"), 0o644)
	_ = os.WriteFile(filepath.Join(root, "beta.txt"), []byte("x"), 0o644)
	m.localCwd = root

	m.mode = modeGoto
	m.gotoIn.SetValue("al")
	next, cmd := m.gotoComplete()
	cm, ok := cmd().(sftpGotoCompleteMsg)
	if !ok {
		t.Fatalf("补全命令应返回 sftpGotoCompleteMsg，实际 %T", cm)
	}
	if cm.err != nil {
		t.Fatalf("补全失败: %v", cm.err)
	}
	if len(cm.cands) != 2 {
		t.Fatalf("应匹配 2 个候选，实际 %v", cm.cands)
	}
	next, _ = next.Update(cm)
	if len(next.gotoCandidates) != 2 {
		t.Fatalf("候选应写入模型，实际 %v", next.gotoCandidates)
	}
	want1 := filepath.Join(root, "alpha.txt")
	if next.gotoIn.Value() != want1 {
		t.Fatalf("首次补全应填入 %s，实际 %s", want1, next.gotoIn.Value())
	}

	// Tab 循环到第二个
	next, _ = next.gotoComplete()
	if next.gotoIn.Value() != filepath.Join(root, "alpine.txt") {
		t.Fatalf("二次 Tab 应填入 alpine.txt，实际 %s", next.gotoIn.Value())
	}
	// 再 Tab 回绕到第一个
	next, _ = next.gotoComplete()
	if next.gotoIn.Value() != want1 {
		t.Fatalf("三次 Tab 应回绕到 %s，实际 %s", want1, next.gotoIn.Value())
	}
}

// TestSFTPGotoCompleteRemote 远程 Tab 补全走 sftp 服务端列表
func TestSFTPGotoCompleteRemote(t *testing.T) {
	env := testutil.StartSFTP(t)
	m := newTestSFTPModel(t, env)
	connect(t, m)
	defer m.close()
	m.focus = paneRemote
	m.cwd = env.Root

	_ = os.WriteFile(filepath.Join(env.Root, "alpha.txt"), []byte("x"), 0o644)
	_ = os.WriteFile(filepath.Join(env.Root, "alpine.txt"), []byte("x"), 0o644)
	_ = os.WriteFile(filepath.Join(env.Root, "beta.txt"), []byte("x"), 0o644)

	m.mode = modeGoto
	m.gotoIn.SetValue("al")
	next, cmd := m.gotoComplete()
	cm, ok := cmd().(sftpGotoCompleteMsg)
	if !ok {
		t.Fatalf("补全命令应返回 sftpGotoCompleteMsg，实际 %T", cm)
	}
	if cm.err != nil || len(cm.cands) != 2 {
		t.Fatalf("远程补全异常: err=%v cands=%v", cm.err, cm.cands)
	}
	next, _ = next.Update(cm)
	want1 := path.Join(env.Root, "alpha.txt")
	if next.gotoIn.Value() != want1 {
		t.Fatalf("远程补全应填入 %s，实际 %s", want1, next.gotoIn.Value())
	}
}

// TestSFTPGotoEsc 路径输入 Esc 取消回到浏览模式
func TestSFTPGotoEsc(t *testing.T) {
	env := testutil.StartSFTP(t)
	m := newTestSFTPModel(t, env)
	defer m.close()

	next, _ := m.openGoto()
	next, _ = next.handleKey(pressKey(tea.KeyEsc).(tea.KeyPressMsg))
	if next.mode != modeBrowse {
		t.Fatalf("Esc 应取消输入，实际 %d", next.mode)
	}
}

// TestSFTPMultiSelectBatchUpload 本地多选（文件+目录）Enter 批量上传，文件夹递归
func TestSFTPMultiSelectBatchUpload(t *testing.T) {
	env := testutil.StartSFTP(t)
	m := newTestSFTPModel(t, env)
	connect(t, m)
	defer m.close()
	m.focus = paneLocal
	m.cwd = env.Root

	// 本地源：f1.txt + dir/n.txt（嵌套目录）
	_ = os.WriteFile(filepath.Join(m.localCwd, "f1.txt"), []byte("111"), 0o644)
	_ = os.MkdirAll(filepath.Join(m.localCwd, "dir", "sub"), 0o755)
	_ = os.WriteFile(filepath.Join(m.localCwd, "dir", "n.txt"), []byte("nested"), 0o644)

	// 刷新本地列表并多选 f1.txt 与 dir
	lm := m.loadLocal()
	next, _ := m.Update(lm())
	next.selLocal[indexOfName(next.localEntries, "f1.txt")] = struct{}{}
	next.selLocal[indexOfName(next.localEntries, "dir")] = struct{}{}
	if next.selCount() != 2 {
		t.Fatalf("应选中 2 项，实际 %d", next.selCount())
	}

	next, start := next.startBatch(true)
	if start == nil {
		t.Fatal("批量上传应产生进度命令")
	}
	next = driveProgress(t, next, start)
	if next.err != "" {
		t.Fatalf("批量上传失败: %s", next.err)
	}
	// 校验远程（含递归目录）
	checkDisk(t, filepath.Join(env.Root, "f1.txt"), "111")
	checkDisk(t, filepath.Join(env.Root, "dir", "n.txt"), "nested")
	if next.hasSel() {
		t.Fatal("批量传输后应清空选中")
	}
}

// TestSFTPMultiSelectBatchDownload 远程多选 Enter 批量下载（含目录递归）
func TestSFTPMultiSelectBatchDownload(t *testing.T) {
	env := testutil.StartSFTP(t)
	m := newTestSFTPModel(t, env)
	connect(t, m)
	defer m.close()
	m.cwd = env.Root

	_ = os.WriteFile(filepath.Join(env.Root, "r1.txt"), []byte("r1"), 0o644)
	_ = os.MkdirAll(filepath.Join(env.Root, "rdir", "sub"), 0o755)
	_ = os.WriteFile(filepath.Join(env.Root, "rdir", "n.txt"), []byte("rn"), 0o644)

	// 刷新远程列表并多选 r1.txt 与 rdir（默认焦点远程栏）
	m.entries = nil
	rl := m.loadList()
	next, _ := m.Update(rl())
	next.selRemote[indexOfName(next.entries, "r1.txt")] = struct{}{}
	next.selRemote[indexOfName(next.entries, "rdir")] = struct{}{}

	next, start := next.startBatch(false)
	if start == nil {
		t.Fatal("批量下载应产生进度命令")
	}
	next = driveProgress(t, next, start)
	if next.err != "" {
		t.Fatalf("批量下载失败: %s", next.err)
	}
	checkDisk(t, filepath.Join(next.localCwd, "r1.txt"), "r1")
	checkDisk(t, filepath.Join(next.localCwd, "rdir", "n.txt"), "rn")
}

// TestSFTPMultiSelectBatchDelete 多选批量删除（远程）
func TestSFTPMultiSelectBatchDelete(t *testing.T) {
	env := testutil.StartSFTP(t)
	m := newTestSFTPModel(t, env)
	connect(t, m)
	defer m.close()
	m.cwd = env.Root

	_ = os.WriteFile(filepath.Join(env.Root, "d1.txt"), []byte("x"), 0o644)
	_ = os.WriteFile(filepath.Join(env.Root, "d2.txt"), []byte("y"), 0o644)

	m.entries = nil
	rl := m.loadList()
	next, _ := m.Update(rl())
	next.selRemote[indexOfName(next.entries, "d1.txt")] = struct{}{}
	next.selRemote[indexOfName(next.entries, "d2.txt")] = struct{}{}

	// x → 批量删除确认；y 确认执行
	next, _ = next.handleKey(press("x").(tea.KeyPressMsg))
	if !next.confirmBatch {
		t.Fatal("有选中项按 x 应进入批量删除确认")
	}
	next, dc := next.handleKey(press("y").(tea.KeyPressMsg))
	if dc == nil {
		t.Fatal("确认批量删除应产生命令")
	}
	dc()
	if _, err := os.Stat(filepath.Join(env.Root, "d1.txt")); !os.IsNotExist(err) {
		t.Fatal("d1.txt 应已删除")
	}
	if _, err := os.Stat(filepath.Join(env.Root, "d2.txt")); !os.IsNotExist(err) {
		t.Fatal("d2.txt 应已删除")
	}
}

// TestSFTPMultiSelectBatchDeleteLocal 多选批量删除（本地）
func TestSFTPMultiSelectBatchDeleteLocal(t *testing.T) {
	env := testutil.StartSFTP(t)
	m := newTestSFTPModel(t, env)
	defer m.close()
	m.focus = paneLocal

	root := t.TempDir()
	m.localCwd = root
	_ = os.WriteFile(filepath.Join(root, "l1.txt"), []byte("x"), 0o644)
	_ = os.WriteFile(filepath.Join(root, "l2.txt"), []byte("y"), 0o644)

	lm := m.loadLocal()
	next, _ := m.Update(lm())
	next.selLocal[indexOfName(next.localEntries, "l1.txt")] = struct{}{}
	next.selLocal[indexOfName(next.localEntries, "l2.txt")] = struct{}{}

	next, _ = next.handleKey(press("x").(tea.KeyPressMsg))
	if !next.confirmBatch {
		t.Fatal("本地有选中项按 x 应进入批量删除确认")
	}
	next, dc := next.handleKey(press("y").(tea.KeyPressMsg))
	if dc == nil {
		t.Fatal("确认批量删除应产生命令")
	}
	dc()
	if _, err := os.Stat(filepath.Join(root, "l1.txt")); !os.IsNotExist(err) {
		t.Fatal("l1.txt 应已删除")
	}
	if _, err := os.Stat(filepath.Join(root, "l2.txt")); !os.IsNotExist(err) {
		t.Fatal("l2.txt 应已删除")
	}
}

// TestSFTPSelectionRender 选中条目渲染显示 ● 标记
func TestSFTPSelectionRender(t *testing.T) {
	env := testutil.StartSFTP(t)
	m := newTestSFTPModel(t, env)
	defer m.close()
	m.focus = paneLocal

	root := t.TempDir()
	m.localCwd = root
	_ = os.WriteFile(filepath.Join(root, "sel.txt"), []byte("x"), 0o644)
	lm := m.loadLocal()
	next, _ := m.Update(lm())
	next.selLocal[indexOfName(next.localEntries, "sel.txt")] = struct{}{}

	next.width, next.height = 100, 30
	content := next.View().Content
	if !strings.Contains(content, "●") {
		t.Fatalf("选中条目应渲染 ● 标记，实际: %q", content)
	}
	if !strings.Contains(content, "已选中 1 项") {
		t.Fatalf("应显示选中数量提示，实际: %q", content)
	}
}

func checkDisk(t *testing.T, p, want string) {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil || string(b) != want {
		t.Fatalf("文件 %s 校验失败: err=%v", p, err)
	}
}
