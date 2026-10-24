package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"simple-connect/internal/model"
)

func seedPasswordHost(t *testing.T, password string) (*formModel, *model.Host) {
	t.Helper()
	s := testStore(t)
	h := &model.Host{Name: "测试机", Host: "127.0.0.1", Port: 22, User: "root", Auth: model.AuthPassword}
	if err := s.Add(h); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPassword(h, password); err != nil {
		t.Fatal(err)
	}
	return newFormModel(s, s.Find(h.ID)), h
}

// TestFormCtrlDClearsSavedPassword 编辑密码主机时 Ctrl+D 应清除旧密码，
// 且不影响其它字段的更新。
func TestFormCtrlDClearsSavedPassword(t *testing.T) {
	m, h := seedPasswordHost(t, "old-pass")
	m.cursor = fieldPasswordIdx
	m.focus(m.cursor)

	nm, cmd := m.Update(tea.KeyPressMsg(tea.Key{Code: 'd', Mod: tea.ModCtrl}))
	m = nm
	if cmd != nil {
		t.Fatalf("Ctrl+D 不应产生命令")
	}
	if !m.clearPassword {
		t.Fatal("Ctrl+D 应设置清除标记")
	}
	m.fields[0].input.SetValue("改名后的主机")
	m, cmd = m.save()
	if m.err != "" {
		t.Fatalf("保存失败: %s", m.err)
	}
	if cmd == nil {
		t.Fatal("保存成功应返回导航命令")
	}
	if _, ok, err := m.store.Password(h); err != nil || ok {
		t.Fatalf("旧密码应已清除: err=%v ok=%v", err, ok)
	}
	got := m.store.Find(h.ID)
	if got == nil || got.Name != "改名后的主机" || got.HasPassword {
		t.Fatalf("主机字段/HasPassword 异常: %+v", got)
	}
}

// TestFormSwitchToKeyClearsPassword 切到私钥认证保存后，旧密码不应继续留在密码本中。
func TestFormSwitchToKeyClearsPassword(t *testing.T) {
	m, h := seedPasswordHost(t, "old-pass")
	m.fields[fieldAuthIdx].choice.index = 1
	m.fields[fieldKeyIdx].input.SetValue("~/.ssh/id_ed25519")

	m, cmd := m.save()
	if m.err != "" {
		t.Fatalf("保存失败: %s", m.err)
	}
	if cmd == nil {
		t.Fatal("保存成功应返回导航命令")
	}
	if _, ok, err := m.store.Password(h); err != nil || ok {
		t.Fatalf("切换私钥后旧密码应清除: err=%v ok=%v", err, ok)
	}
	got := m.store.Find(h.ID)
	if got == nil || got.Auth != model.AuthKey || got.HasPassword {
		t.Fatalf("主机状态异常: %+v", got)
	}
}

// TestFormTypingCancelsClearMarker Ctrl+D 后继续输入新密码，应保存新值而不是清除。
func TestFormTypingCancelsClearMarker(t *testing.T) {
	m, h := seedPasswordHost(t, "old-pass")
	m.cursor = fieldPasswordIdx
	m.focus(m.cursor)
	nm, _ := m.Update(tea.KeyPressMsg(tea.Key{Code: 'd', Mod: tea.ModCtrl}))
	m = nm
	if !m.clearPassword {
		t.Fatal("Ctrl+D 应设置清除标记")
	}
	for _, r := range "new-pass" {
		nm, _ = m.Update(press(string(r)))
		m = nm
	}
	if m.clearPassword {
		t.Fatal("输入新密码应取消清除标记")
	}
	m, cmd := m.save()
	if m.err != "" || cmd == nil {
		t.Fatalf("保存失败: err=%q cmd=%v", m.err, cmd)
	}
	if p, ok, err := m.store.Password(h); err != nil || !ok || p != "new-pass" {
		t.Fatalf("应保存新密码: err=%v ok=%v p=%q", err, ok, p)
	}
}

// TestFormPasteCancelsClearMarker 回归：Ctrl+D 后粘贴新密码应保存粘贴值。
// 粘贴经 tea.PasteMsg（bracketed paste）送达，不能只靠 KeyPressMsg 取消标记，
// 否则保存时会误走“清除密码”分支，把粘贴的新密码丢掉。
func TestFormPasteCancelsClearMarker(t *testing.T) {
	m, h := seedPasswordHost(t, "old-pass")
	m.cursor = fieldPasswordIdx
	m.focus(m.cursor)
	nm, _ := m.Update(tea.KeyPressMsg(tea.Key{Code: 'd', Mod: tea.ModCtrl}))
	m = nm
	if !m.clearPassword {
		t.Fatal("Ctrl+D 应设置清除标记")
	}

	nm, _ = m.Update(tea.PasteMsg{Content: "pasted-new"})
	m = nm
	if m.clearPassword {
		t.Fatal("粘贴新密码应取消清除标记")
	}

	m, cmd := m.save()
	if m.err != "" || cmd == nil {
		t.Fatalf("保存失败: err=%q cmd=%v", m.err, cmd)
	}
	if p, ok, err := m.store.Password(h); err != nil || !ok || p != "pasted-new" {
		t.Fatalf("应保存粘贴的新密码: err=%v ok=%v p=%q", err, ok, p)
	}
}
