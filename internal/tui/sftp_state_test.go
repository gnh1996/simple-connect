package tui

import (
	"os"
	"path"
	"path/filepath"
	"testing"

	tea "charm.land/bubbletea/v2"

	sftpc "simple-connect/internal/sftp"
	"simple-connect/internal/testutil"
)

// TestSFTPRefreshKeepsSelectionByName 回归：多选按条目名（稳定标识）而非列表下标保存。
// 刷新后目录顺序变化（新增条目排在前面）不应让选中错位到别的文件。
func TestSFTPRefreshKeepsSelectionByName(t *testing.T) {
	env := testutil.StartSFTP(t)
	m := newTestSFTPModel(t, env)
	defer m.close()
	m.focus = paneLocal

	root := t.TempDir()
	m.localCwd = root
	_ = os.WriteFile(filepath.Join(root, "a.txt"), []byte("a"), 0o644)
	_ = os.WriteFile(filepath.Join(root, "b.txt"), []byte("b"), 0o644)

	next, _ := m.Update(m.loadLocal()())
	next.localCursor = indexOfName(next.localEntries, "b.txt")
	next, _ = next.handleKey(pressKey(tea.KeySpace).(tea.KeyPressMsg))
	if next.selCount() != 1 {
		t.Fatalf("应选中 1 项，实际 %d", next.selCount())
	}

	// 新增 "0.txt" 使其排在 b.txt 之前，刷新后下标整体位移
	_ = os.WriteFile(filepath.Join(root, "0.txt"), []byte("0"), 0o644)
	next, _ = next.Update(next.loadLocal()())

	sel := next.selectedEntries()
	if len(sel) != 1 || sel[0].Name() != "b.txt" {
		names := make([]string, 0, len(sel))
		for _, e := range sel {
			names = append(names, e.Name())
		}
		t.Fatalf("刷新后选中应按名字保持为 [b.txt]，实际 %v", names)
	}
}

// TestSFTPTransferNotRestartableByListMsg 回归：busy 语义过载——传输进行中若有列表
// 消息到达（用户导航触发 loadList），不得把"传输中"误判为空闲而允许再发起一次传输。
func TestSFTPTransferNotRestartableByListMsg(t *testing.T) {
	env := testutil.StartSFTP(t)
	m := newTestSFTPModel(t, env)
	connect(t, m)
	defer m.close()
	m.cwd = env.Root
	m.focus = paneLocal
	_ = os.WriteFile(filepath.Join(m.localCwd, "u.txt"), []byte("u"), 0o644)
	m, _ = m.Update(m.loadLocal()())

	// 模拟一个仍在进行中的传输
	m.transfer = sftpc.NewTransfer("big.bin", false)

	// 传输期间列表消息到达（例如用户触发了导航）
	m, _ = m.Update(sftpListMsg{kind: paneRemote, path: m.cwd, entries: m.entries})
	if m.requestTransfer(filepath.Join(m.localCwd, "u.txt"), path.Join(m.cwd, "u.txt"), true) != nil {
		t.Fatal("传输进行中不得再发起新传输")
	}
}

// TestSFTPTransferBlocksNavigation 回归：传输进行中应屏蔽导航按键，避免与传输并发
// 改写远程列表状态。
func TestSFTPTransferBlocksNavigation(t *testing.T) {
	env := testutil.StartSFTP(t)
	m := newTestSFTPModel(t, env)
	connect(t, m)
	defer m.close()
	m.cwd = env.Root
	m.focus = paneRemote

	m.transfer = sftpc.NewTransfer("big.bin", false)
	before := m.cwd
	next, _ := m.handleKey(pressKey(tea.KeyBackspace).(tea.KeyPressMsg))
	if next.cwd != before {
		t.Fatalf("传输进行中 Backspace 不应改变 cwd（%q → %q）", before, next.cwd)
	}
}

// TestSFTPPromptCompletionStaleResultDiscarded 回归：g/p 共用输入框后，上一个输入
// 模式的迟到补全结果不得写入当前模式的输入框。
func TestSFTPPromptCompletionStaleResultDiscarded(t *testing.T) {
	env := testutil.StartSFTP(t)
	m := newTestSFTPModel(t, env)
	defer m.close()
	m.focus = paneLocal

	root := t.TempDir()
	m.localCwd = root
	_ = os.WriteFile(filepath.Join(root, "alpha.txt"), []byte("x"), 0o644)

	// p 模式发起补全（命令先不执行）
	next, _ := m.openPath()
	next.promptIn.SetValue("al")
	_, cmd := next.promptComplete()
	if cmd == nil {
		t.Fatal("p 模式 Tab 应产生补全命令")
	}
	// Esc 取消 p，改开 g（模式与输入均变化）
	next, _ = next.handleKey(pressKey(tea.KeyEsc).(tea.KeyPressMsg))
	next, _ = next.openGoto()

	// 迟到的 p 补全结果到达：必须被丢弃
	next, _ = next.Update(cmd())
	if want := root; next.promptIn.Value() != want {
		t.Fatalf("陈旧补全结果不应写入 g 输入框，期望 %q 实际 %q", want, next.promptIn.Value())
	}
}
