package tui

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"simple-connect/internal/model"
	sshc "simple-connect/internal/ssh"
	"simple-connect/internal/testutil"
)

// TestSFTPConnectingFeedbackAndBusyReset 拨号期间应有明确反馈并置 busy；
// 失败与指纹确认分支必须复位 busy（否则 t/p/x/r 被永久挡住）。见 3.1。
func TestSFTPConnectingFeedbackAndBusyReset(t *testing.T) {
	s := testStore(t)
	h := &model.Host{Name: "t", Host: "10.0.0.1", User: "root", Auth: model.AuthPassword}
	_ = s.Add(h)

	m := newSFTPModel(s, h, "", nil)
	_ = m.Init()
	if !m.busy || m.status == "" {
		t.Fatalf("Init 应显示连接中并置 busy，实际 busy=%v status=%q", m.busy, m.status)
	}

	// 拨号失败：busy 复位 + 错误提示
	next, _ := m.Update(sftpConnMsg{err: errors.New("dial tcp: connection refused")})
	if next.busy {
		t.Fatal("连接失败后 busy 应复位")
	}
	if !strings.Contains(next.err, "连接失败") {
		t.Fatalf("应显示连接失败，实际 %q", next.err)
	}

	// 指纹确认态：busy 复位，y 重连时再置回
	m2 := newSFTPModel(s, h, "", nil)
	m2.trustHostKey = func(*sshc.UnknownHostKeyError) error { return nil } // 禁止触碰真实 known_hosts
	_ = m2.Init()
	uk := &sshc.UnknownHostKeyError{Hostname: "10.0.0.1:22", Fingerprint: "SHA256:abc"}
	next2, _ := m2.Update(sftpConnMsg{err: uk})
	if next2.busy {
		t.Fatal("指纹确认期间 busy 应复位")
	}
	if next2.pendingKey == nil {
		t.Fatal("应进入指纹确认态")
	}
	next2, cmd := next2.handleKey(press("y").(tea.KeyPressMsg))
	if cmd == nil {
		t.Fatal("确认信任后应重新拨号")
	}
	if !next2.busy || !strings.Contains(next2.status, "重连") {
		t.Fatalf("重连时应重新置 busy 并提示，实际 busy=%v status=%q", next2.busy, next2.status)
	}
}

// TestRenderProgressClamp done>total 不得 panic，进度条宽度固定并显示 done/total（2.5）。
func TestRenderProgressClamp(t *testing.T) {
	got := renderProgress("x.bin", 30, 10)
	if !strings.Contains(got, "100%") {
		t.Fatalf("done>total 应钳位到 100%%，实际 %q", got)
	}
	if n := strings.Count(got, "█") + strings.Count(got, "░"); n != 20 {
		t.Fatalf("进度条宽度应恒为 20，实际 %d", n)
	}
	if !strings.Contains(got, "30 B/10 B") {
		t.Fatalf("应显示 done/total，实际 %q", got)
	}

	// total 未知：只显示已传字节，不出现百分比/进度条
	got = renderProgress("x.bin", 42, 0)
	if strings.Contains(got, "%") || strings.Contains(got, "░") {
		t.Fatalf("total=0 应退化为已传字节展示，实际 %q", got)
	}
}

// TestSFTPLocalNavigationUsesFilepath 本地栏导航使用 filepath 语义（2.4）。
func TestSFTPLocalNavigationUsesFilepath(t *testing.T) {
	base := filepath.Join("/tmp", "sc-local", "a", "b")
	m := &sftpModel{localCwd: base, selLocal: map[int]struct{}{}, selRemote: map[int]struct{}{}}
	next, cmd := m.goUp()
	if cmd == nil {
		t.Fatal("上一级应刷新本地列表")
	}
	if want := filepath.Dir(base); next.localCwd != want {
		t.Fatalf("goUp 应使用 filepath.Dir，期望 %q 实际 %q", want, next.localCwd)
	}

	// 根目录再向上应原地不动
	root := &sftpModel{localCwd: "/", selLocal: map[int]struct{}{}, selRemote: map[int]struct{}{}}
	nextRoot, cmd := root.goUp()
	if cmd != nil || nextRoot.localCwd != "/" {
		t.Fatalf("根目录向上应原地不动，实际 %q cmd=%v", nextRoot.localCwd, cmd != nil)
	}

	// 进入子目录应使用 filepath.Join（本地路径不带 POSIX 混用）
	m3 := &sftpModel{
		localCwd:       "/tmp/sc-local",
		localEntries:   []fs.FileInfo{benchFileInfo{name: "sub", dir: true}},
		selLocal:       map[int]struct{}{},
		selRemote:      map[int]struct{}{},
		localConfirmID: -1, confirmID: -1,
	}
	next3, cmd := m3.enterCurrent()
	if cmd == nil {
		t.Fatal("进入目录应加载本地列表")
	}
	if want := filepath.Join("/tmp/sc-local", "sub"); next3.localCwd != want {
		t.Fatalf("enterCurrent 应使用 filepath.Join，期望 %q 实际 %q", want, next3.localCwd)
	}
}

// TestReadLocalInfosKeepsBrokenSymlink 断链符号链接不应从本地列表静默消失（8.3）。
func TestReadLocalInfosKeepsBrokenSymlink(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "ok.txt"), []byte("x"), 0o644)
	if err := os.Symlink(filepath.Join(dir, "missing-target"), filepath.Join(dir, "broken")); err != nil {
		t.Skipf("当前环境无法创建符号链接: %v", err)
	}
	infos, err := readLocalInfos(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, info := range infos {
		names = append(names, info.Name())
	}
	if !strings.Contains(strings.Join(names, ","), "broken") {
		t.Fatalf("断链符号链接不应消失，实际条目: %v", names)
	}
}

// TestSFTPTransferDoneKeepsCursor 传输完成后保持光标位置，不再跳回顶部（3.7.3）。
func TestSFTPTransferDoneKeepsCursor(t *testing.T) {
	env := testutil.StartSFTP(t)
	m := newTestSFTPModel(t, env)
	connect(t, m)
	defer m.close()

	for i := 0; i < 5; i++ {
		if err := os.WriteFile(filepath.Join(env.Root, fmt.Sprintf("f%02d.txt", i)), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	m.cwd = env.Root
	m.entries = nil
	m, _ = m.Update(m.loadList()())
	if len(m.entries) != 5 {
		t.Fatalf("应列出 5 项，实际 %d", len(m.entries))
	}

	m.focus = paneRemote
	m.cursor = 3
	next, cmd := m.enterCurrent()
	if cmd == nil {
		t.Fatal("文件上 Enter 应触发下载覆盖检测")
	}
	ow := cmd().(sftpOverwriteCheckMsg)
	if ow.err != nil {
		t.Fatalf("覆盖检测失败: %v", ow.err)
	}
	next, start := next.Update(ow)
	if start == nil {
		t.Fatal("本地无冲突应直接启动下载")
	}
	next = driveProgress(t, next, start)
	if next.cursor != 3 {
		t.Fatalf("传输完成后光标应保持 3，实际 %d", next.cursor)
	}
}

// TestSFTPInitConnStatusClearedAfterConnect 连接成功后应清除"正在连接…"状态
// （否则状态行常驻，bodyHeight 也被挤掉一行）。
func TestSFTPInitConnStatusClearedAfterConnect(t *testing.T) {
	env := testutil.StartSFTP(t)
	m := newTestSFTPModel(t, env)
	connect(t, m) // connect 内部走 Init → sftpConnMsg → 两条列表消息
	if m.status == "正在连接…" || m.busy {
		t.Fatalf("连接完成后应清除连接中状态与 busy，实际 status=%q busy=%v", m.status, m.busy)
	}
}
