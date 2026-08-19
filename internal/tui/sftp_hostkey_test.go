package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"simple-connect/internal/model"
	sshc "simple-connect/internal/ssh"
)

// TestSFTPFirstConnectFingerprintConfirm 验证首次连接指纹确认流程：
// 连接返回 UnknownHostKeyError → 页面进入确认态 → y 信任并重连，n 取消。
func TestSFTPFirstConnectFingerprintConfirm(t *testing.T) {
	host := &model.Host{Name: "测试机", Host: "example.com", Port: 22, User: "root", Auth: model.AuthPassword}
	ukErr := &sshc.UnknownHostKeyError{Hostname: "example.com:22", Fingerprint: "SHA256:abcd"}

	// 拒绝场景：n 取消且不重连
	m := newSFTPModel(testStore(t), host, "", nil)
	m.trustHostKey = func(uk *sshc.UnknownHostKeyError) error { return nil }
	m, _ = m.Update(sftpConnMsg{err: ukErr})
	if m.pendingKey == nil {
		t.Fatal("首次连接后应进入 pendingKey 确认态")
	}
	if m.status == "" {
		t.Fatal("应展示待确认提示")
	}
	m, cmd := m.handleKey(press("n").(tea.KeyPressMsg))
	if m.pendingKey != nil {
		t.Fatal("按 n 应清除 pendingKey")
	}
	if cmd != nil {
		t.Fatal("拒绝后不应返回重连命令")
	}

	// 信任场景：y 调用 trustHostKey 并触发重连
	m = newSFTPModel(testStore(t), host, "", nil)
	trusted := false
	m.trustHostKey = func(uk *sshc.UnknownHostKeyError) error { trusted = true; return nil }
	m, _ = m.Update(sftpConnMsg{err: ukErr})
	m, cmd = m.handleKey(press("y").(tea.KeyPressMsg))
	if !trusted {
		t.Fatal("按 y 应调用 trustHostKey")
	}
	if m.pendingKey != nil {
		t.Fatal("确认后应清除 pendingKey")
	}
	if cmd == nil {
		t.Fatal("确认后应返回重连命令")
	}

	// 信任失败：写入 known_hosts 失败时提示错误，不重连
	m = newSFTPModel(testStore(t), host, "", nil)
	m.trustHostKey = func(uk *sshc.UnknownHostKeyError) error {
		return &testErr{}
	}
	m, _ = m.Update(sftpConnMsg{err: ukErr})
	m, cmd = m.handleKey(press("y").(tea.KeyPressMsg))
	if cmd != nil {
		t.Fatal("信任失败后不应返回重连命令")
	}
	if m.err == "" {
		t.Fatal("信任失败应展示错误")
	}
}

type testErr struct{}

func (e *testErr) Error() string { return "写入失败" }
