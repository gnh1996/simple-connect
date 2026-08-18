package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"simple-connect/internal/model"
	"simple-connect/internal/store"
)

func press(text string) tea.Msg {
	if len(text) == 0 {
		return nil
	}
	r := []rune(text)
	return tea.KeyPressMsg(tea.Key{Code: r[0], Text: string(r[0])})
}

func pressKey(code rune) tea.Msg {
	return tea.KeyPressMsg(tea.Key{Code: code})
}

// upd 更新模型并执行返回的命令（文本输入已禁用光标闪烁，无时间开销）
func upd(m *Root, msg tea.Msg) *Root {
	rm, cmd := m.Update(msg)
	r, ok := rm.(*Root)
	if !ok {
		panic("Update 返回类型不是 *Root")
	}
	if cmd != nil {
		rm2, _ := r.Update(cmd())
		r, ok = rm2.(*Root)
		if !ok {
			panic("cmd 处理后返回类型不是 *Root")
		}
	}
	return r
}

func testStore(t *testing.T) *store.Store {
	t.Helper()
	dir := t.TempDir()
	os.Setenv("XDG_CONFIG_HOME", dir)
	t.Cleanup(func() { os.Unsetenv("XDG_CONFIG_HOME") })
	s, err := store.Load()
	if err != nil {
		t.Fatalf("store 加载失败: %v", err)
	}
	return s
}

func TestListRendersHosts(t *testing.T) {
	s := testStore(t)
	h := &model.Host{Name: "服务器A", Host: "10.0.0.1", Port: 22, User: "root", Auth: model.AuthPassword}
	if err := s.Add(h); err != nil {
		t.Fatal(err)
	}
	root := NewRoot(s)
	view := root.View().Content
	if !strings.Contains(view, "服务器A") {
		t.Fatalf("列表应显示主机名，实际: %q", view)
	}
	if !strings.Contains(view, "root@10.0.0.1:22") {
		t.Fatalf("列表应显示连接目标，实际: %q", view)
	}
}

// TestNewSFTPRootFromSession 验证会话唤起：初始即 SFTP 页，q 返回 ActionResumeSSH
func TestNewSFTPRootFromSession(t *testing.T) {
	s := testStore(t)
	h := &model.Host{Name: "测试机", Host: "10.0.0.1", User: "root", Auth: model.AuthPassword}
	_ = s.Add(h)

	root := NewSFTPRoot(s, h)
	if root.page != pageSFTP || root.sftp == nil {
		t.Fatalf("初始页应为 SFTP，实际 page=%d sftp=%v", root.page, root.sftp)
	}
	if !root.sftp.fromSession {
		t.Fatal("会话唤起场景 fromSession 应为 true")
	}

	// q 返回 → 应请求重连会话
	root = upd(root, backToListMsg{})
	if root.Action != ActionResumeSSH {
		t.Fatalf("会话唤起下 q 应返回 ActionResumeSSH，实际 %d", root.Action)
	}
}

// TestNewSFTPRootQuitToList 从列表进入的 SFTP 页 q 返回正常列表（无 resume）
func TestNewSFTPRootQuitToList(t *testing.T) {
	s := testStore(t)
	h := &model.Host{Name: "测试机", Host: "10.0.0.1", User: "root", Auth: model.AuthPassword}
	_ = s.Add(h)

	root := NewRoot(s)
	root = upd(root, navSFTPMsg{host: h})
	if root.page != pageSFTP || root.sftp == nil {
		t.Fatalf("应进入 SFTP 页，实际 page=%d", root.page)
	}
	if root.sftp.fromSession {
		t.Fatal("列表进入的 SFTP 页 fromSession 应为 false")
	}
	root = upd(root, backToListMsg{})
	if root.Action != ActionNone {
		t.Fatalf("列表进入的 SFTP 页 q 应回列表（ActionNone），实际 %d", root.Action)
	}
	if root.page != pageList {
		t.Fatalf("应返回列表页，实际 %d", root.page)
	}
}

func TestListConnectAndQuit(t *testing.T) {
	s := testStore(t)
	h := &model.Host{Name: "测试机", Host: "10.0.0.1", User: "root", Auth: model.AuthPassword}
	_ = s.Add(h)

	root := NewRoot(s)
	// 密码认证主机：Enter 先进入免密提示待确认态
	rm, cmd := root.Update(pressKey(tea.KeyEnter))
	if cmd != nil {
		t.Fatalf("Enter 不应直接连接，应进入待确认态，实际返回命令")
	}
	root = rm.(*Root)
	if root.list.connectID != h.ID {
		t.Fatalf("应进入连接待确认态，connectID=%q", root.list.connectID)
	}
	view := root.View().Content
	if !strings.Contains(view, "免密提示") {
		t.Fatalf("界面应显示免密提示，实际: %q", view)
	}

	// 再次 Enter 确认连接
	rm, cmd = root.Update(pressKey(tea.KeyEnter))
	cm, ok := cmd().(tea.Msg)
	if !ok {
		t.Fatal("cmd 应返回 Msg")
	}
	if _, isConnect := cm.(connectMsg); !isConnect {
		t.Fatalf("确认后应产生 connectMsg，实际 %T", cm)
	}
	root = rm.(*Root)
	root.Action = ActionSSH
	root.HostID = h.ID

	// q 退出
	_, cmd2 := root.Update(press("q"))
	if _, isQuit := cmd2().(quitMsg); !isQuit {
		t.Fatalf("q 应产生 quitMsg，实际 %T", cmd2().(tea.Msg))
	}
}

func TestListConnectConfirmCancel(t *testing.T) {
	s := testStore(t)
	h := &model.Host{Name: "测试机", Host: "10.0.0.1", User: "root", Auth: model.AuthPassword}
	_ = s.Add(h)

	root := NewRoot(s)
	// Enter 进入待确认态
	root = upd(root, pressKey(tea.KeyEnter))
	if root.list.connectID != h.ID {
		t.Fatalf("应进入连接待确认态")
	}
	// Esc 取消
	root = upd(root, pressKey(tea.KeyEsc))
	if root.list.connectID != "" {
		t.Fatalf("Esc 应取消连接确认")
	}
	// 取消后不应产生 connectMsg（无命令）
	rm, cmd := root.Update(pressKey(tea.KeyEnter))
	_ = rm
	if cmd != nil {
		t.Fatal("取消后 Enter 应重新进入待确认态而不是直接连接")
	}
	root = rm.(*Root)
	if root.list.connectID != h.ID {
		t.Fatalf("Enter 应再次进入待确认态")
	}
}

func TestFormAddHost(t *testing.T) {
	s := testStore(t)
	root := NewRoot(s)

	// 进入新增表单
	root = upd(root, press("a"))
	if root.page != pageForm {
		t.Fatalf("应进入表单页，实际 %v", root.page)
	}

	// 依次填写字段：名称/主机/端口/用户名
	fill := func(text string) {
		for _, r := range text {
			root = upd(root, press(string(r)))
		}
	}
	fill("生产服务器")
	root = upd(root, pressKey(tea.KeyTab))
	fill("10.1.2.3")
	root = upd(root, pressKey(tea.KeyTab))
	fill("22")
	root = upd(root, pressKey(tea.KeyTab))
	fill("admin")

	// 认证方式为密码，直接保存：Tab 到最后一个字段，再 Enter
	root = upd(root, pressKey(tea.KeyTab)) // 认证方式
	root = upd(root, pressKey(tea.KeyTab)) // 密码
	root = upd(root, pressKey(tea.KeyTab)) // 私钥路径
	root = upd(root, pressKey(tea.KeyTab)) // 本地目录
	root = upd(root, pressKey(tea.KeyEnter))

	if root.page != pageList {
		t.Fatalf("保存后应回到列表页，实际 %v", root.page)
	}
	if len(root.Store.Hosts()) != 1 {
		t.Fatalf("应保存 1 个连接，实际 %d", len(root.Store.Hosts()))
	}
	h := root.Store.Hosts()[0]
	if h.Name != "生产服务器" || h.Host != "10.1.2.3" || h.User != "admin" {
		t.Fatalf("保存的连接数据异常: %+v", h)
	}
}

func TestListKeyAuthNoAgentHint(t *testing.T) {
	// 模拟无 agent 环境
	old, had := os.LookupEnv("SSH_AUTH_SOCK")
	_ = os.Setenv("SSH_AUTH_SOCK", filepath.Join(t.TempDir(), "no-agent.sock"))
	t.Cleanup(func() {
		if had {
			_ = os.Setenv("SSH_AUTH_SOCK", old)
		} else {
			_ = os.Unsetenv("SSH_AUTH_SOCK")
		}
	})

	s := testStore(t)
	h := &model.Host{Name: "密钥机", Host: "10.0.0.2", User: "root",
		Auth: model.AuthKey, KeyPath: "~/.ssh/id_ed25519"}
	_ = s.Add(h)

	root := NewRoot(s)
	root = upd(root, pressKey(tea.KeyEnter))
	if root.list.connectID != h.ID {
		t.Fatalf("密钥认证 + 无 agent 应进入待确认态")
	}
	if !strings.Contains(root.list.connectHint, "ssh-agent") {
		t.Fatalf("提示应提及 ssh-agent，实际: %q", root.list.connectHint)
	}
}

func TestListFilter(t *testing.T) {
	s := testStore(t)
	_ = s.Add(&model.Host{Name: "web-01", Host: "10.0.0.1", User: "root", Auth: model.AuthPassword})
	_ = s.Add(&model.Host{Name: "db-01", Host: "10.0.0.2", User: "root", Auth: model.AuthPassword})

	root := NewRoot(s)
	// 进入过滤模式
	root = upd(root, press("/"))
	if !root.list.filtering {
		t.Fatal("应进入过滤模式")
	}
	// 输入 db
	for _, r := range "db" {
		root = upd(root, press(string(r)))
	}
	if len(root.list.filtered) != 1 || root.list.filtered[0].Name != "db-01" {
		t.Fatalf("过滤结果异常: %d 条", len(root.list.filtered))
	}
	// 回车结束过滤
	root = upd(root, pressKey(tea.KeyEnter))
	if root.list.filtering {
		t.Fatal("回车应结束过滤模式")
	}
}
