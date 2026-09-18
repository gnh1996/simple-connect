package tui

import (
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	sftpc "simple-connect/internal/sftp"
	"simple-connect/internal/testutil"
)

// TestSFTPQuitDuringTransferPrompts 传输中 q 应先弹退出确认，不直接离开，也不取消传输。
func TestSFTPQuitDuringTransferPrompts(t *testing.T) {
	env := testutil.StartSFTP(t)
	m := newTestSFTPModel(t, env)
	connect(t, m)
	defer m.close()

	m.transfer = sftpc.NewTransfer("big.bin", false)
	m.busy = true

	next, cmd := m.handleKey(press("q").(tea.KeyPressMsg))
	if cmd != nil {
		t.Fatal("传输中 q 不应直接产生离开命令")
	}
	if !next.confirmExit {
		t.Fatal("传输中 q 应进入退出确认态")
	}
	if !strings.Contains(strings.Join(next.dynamicLines(), "\n"), "确定退出") {
		t.Fatal("应显示退出确认提示")
	}

	// n 取消退出，恢复浏览且不影响传输
	next, _ = next.handleKey(press("n").(tea.KeyPressMsg))
	if next.confirmExit {
		t.Fatal("n 应取消退出确认")
	}
	if next.transfer == nil || next.transfer.Canceled() {
		t.Fatal("取消退出不应取消传输")
	}
}

// TestSFTPQuitDuringTransferConfirmCancels 确认退出应请求取消传输并进入 exiting 等待态，
// exiting 期间屏蔽其他输入。
func TestSFTPQuitDuringTransferConfirmCancels(t *testing.T) {
	env := testutil.StartSFTP(t)
	m := newTestSFTPModel(t, env)
	connect(t, m)
	defer m.close()

	tr := sftpc.NewTransfer("big.bin", false)
	m.transfer = tr
	m.busy = true

	next, _ := m.handleKey(press("q").(tea.KeyPressMsg))
	next, cmd := next.handleKey(press("y").(tea.KeyPressMsg))
	if !next.exiting {
		t.Fatal("确认退出后应进入 exiting 态")
	}
	if !tr.Canceled() {
		t.Fatal("确认退出应请求取消传输")
	}
	if cmd == nil {
		t.Fatal("确认退出应继续轮询等待取消完成")
	}
	// exiting 期间忽略其他按键
	next2, c2 := next.handleKey(press("x").(tea.KeyPressMsg))
	if next2 != next || c2 != nil {
		t.Fatal("exiting 期间不应处理其他按键")
	}
}

// TestSFTPCtrlCTriggersExit 传输中 Ctrl+C 与 q 同义，进入退出确认。
func TestSFTPCtrlCTriggersExit(t *testing.T) {
	env := testutil.StartSFTP(t)
	m := newTestSFTPModel(t, env)
	connect(t, m)
	defer m.close()

	m.transfer = sftpc.NewTransfer("big.bin", false)
	m.busy = true

	next, cmd := m.handleKey(tea.KeyPressMsg(tea.Key{Code: 'c', Mod: tea.ModCtrl}))
	if cmd != nil {
		t.Fatal("传输中 Ctrl+C 不应直接离开")
	}
	if !next.confirmExit {
		t.Fatal("传输中 Ctrl+C 应进入退出确认态")
	}
}

// TestSFTPProgressExitingLeavesPage exiting 且传输结束后应返回列表（优先于错误/完成提示）。
func TestSFTPProgressExitingLeavesPage(t *testing.T) {
	env := testutil.StartSFTP(t)
	m := newTestSFTPModel(t, env)
	connect(t, m)
	defer m.close()

	// 用一个必然失败的传输构造 finished 状态（不依赖真实传输耗时）
	tr := sftpc.NewTransfer("x", false)
	sftpc.Download(m.conn.Client, tr, filepath.Join(env.Root, "no-such-file"), filepath.Join(t.TempDir(), "x"))
	m.transfer = tr
	m.busy = true
	m.exiting = true

	next, cmd := m.handleProgress()
	if next.transfer != nil || next.exiting {
		t.Fatalf("结束后应清理传输/退出态: transfer=%v exiting=%v", next.transfer, next.exiting)
	}
	if cmd == nil {
		t.Fatal("exiting 结束后应返回离开命令")
	}
	if _, ok := cmd().(backToListMsg); !ok {
		t.Fatalf("exiting 结束应返回 backToListMsg")
	}
}

// TestSFTPProgressCanceledShowsStatus 取消后的传输结束应提示"已取消"而非报错。
func TestSFTPProgressCanceledShowsStatus(t *testing.T) {
	env := testutil.StartSFTP(t)
	m := newTestSFTPModel(t, env)
	connect(t, m)
	defer m.close()

	tr := sftpc.NewTransfer("x", false)
	tr.Cancel()
	sftpc.Download(m.conn.Client, tr, filepath.Join(env.Root, "no-such-file"), filepath.Join(t.TempDir(), "x"))
	m.transfer = tr
	m.busy = true

	next, cmd := m.handleProgress()
	if next.transfer != nil {
		t.Fatal("结束后应清理传输对象")
	}
	if !strings.Contains(next.status, "已取消") {
		t.Fatalf("取消后应提示已取消，实际 %q", next.status)
	}
	if next.err != "" {
		t.Fatalf("取消不应显示错误，实际 %q", next.err)
	}
	if cmd == nil {
		t.Fatal("取消后应刷新两侧列表")
	}
}
