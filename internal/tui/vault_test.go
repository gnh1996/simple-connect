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

func testStoreAt(t *testing.T, dir string) *store.Store {
	t.Helper()
	s, err := store.LoadWithOptions(store.Options{Dir: dir})
	if err != nil {
		t.Fatalf("store 加载失败: %v", err)
	}
	return s
}

func TestListShowsVaultPathsAndWarning(t *testing.T) {
	s := testStore(t)
	root := NewRoot(s)
	view := root.View().Content
	for _, want := range []string{"密码库", "密钥", "vault.age", "vault.key", "建议独立保管"} {
		if !strings.Contains(view, want) {
			t.Fatalf("列表页应显示 %q:\n%s", want, view)
		}
	}
}

func TestListOpenVaultPage(t *testing.T) {
	s := testStore(t)
	root := NewRoot(s)
	root = upd(root, press("p"))
	if root.page != pageVault {
		t.Fatalf("按 p 应进入密码库页，实际 page=%d", root.page)
	}
	if !strings.Contains(root.View().Content, "密码库") {
		t.Fatal("密码库页应渲染标题")
	}
	root = upd(root, pressKey(tea.KeyEsc))
	if root.page != pageList {
		t.Fatalf("Esc 应返回列表，实际 page=%d", root.page)
	}
}

func TestVaultPageWhenKeyMissing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "simple-connect")
	s := testStoreAt(t, dir)
	h := &model.Host{Name: "测试机", Host: "127.0.0.1", User: "root", Auth: model.AuthPassword}
	if err := s.Add(h); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPassword(h, "secret"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(s.VaultInfo().KeyPath); err != nil {
		t.Fatal(err)
	}

	s2 := testStoreAt(t, dir)
	root := NewRoot(s2)
	if root.page != pageVault {
		t.Fatalf("密钥缺失时应停在密码库页，实际 page=%d", root.page)
	}
	view := root.View().Content
	if !strings.Contains(view, "未解锁") || !strings.Contains(view, "密钥文件不存在") {
		t.Fatalf("应显示密钥缺失原因:\n%s", view)
	}
	if !strings.Contains(view, "重置") {
		t.Fatalf("应提供重置入口:\n%s", view)
	}
}

func TestVaultPageSwitchCustomKeyPath(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "simple-connect")
	s := testStoreAt(t, dir)
	h := &model.Host{Name: "测试机", Host: "127.0.0.1", User: "root", Auth: model.AuthPassword}
	_ = s.Add(h)
	if err := s.SetPassword(h, "secret"); err != nil {
		t.Fatal(err)
	}
	keyBytes, err := os.ReadFile(s.VaultInfo().KeyPath)
	if err != nil {
		t.Fatal(err)
	}
	custom := filepath.Join(t.TempDir(), "usb.key")
	if err := os.WriteFile(custom, keyBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(s.VaultInfo().KeyPath); err != nil {
		t.Fatal(err)
	}

	s2 := testStoreAt(t, dir)
	root := NewRoot(s2)
	if root.page != pageVault {
		t.Fatalf("应停在密码库页，实际 %d", root.page)
	}
	root.vault.input.SetValue(custom)
	root = upd(root, pressKey(tea.KeyEnter))
	if root.page != pageList {
		t.Fatalf("保存有效密钥后应进入列表，实际 %d（err=%q）", root.page, root.vault.err)
	}
	if !root.Store.VaultReady() || root.Store.VaultInfo().KeyPath != custom {
		t.Fatalf("密钥路径未生效: %+v", root.Store.VaultInfo())
	}
	got := root.Store.Find(h.ID)
	if _, ok, err := root.Store.Password(got); err != nil || !ok {
		t.Fatalf("切换路径后应能读取密码: err=%v ok=%v", err, ok)
	}
	if strings.Contains(root.View().Content, "建议独立保管") {
		t.Fatal("外部密钥不应再显示同目录警告")
	}
}

func TestVaultPageReset(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "simple-connect")
	s := testStoreAt(t, dir)
	h := &model.Host{Name: "测试机", Host: "127.0.0.1", User: "root", Auth: model.AuthPassword}
	_ = s.Add(h)
	if err := s.SetPassword(h, "secret"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(s.VaultInfo().KeyPath); err != nil {
		t.Fatal(err)
	}

	s2 := testStoreAt(t, dir)
	root := NewRoot(s2)
	if root.page != pageVault {
		t.Fatalf("应停在密码库页，实际 %d", root.page)
	}
	// Ctrl+R → 输入 RESET → Enter
	root = upd(root, tea.KeyPressMsg(tea.Key{Code: 'r', Mod: tea.ModCtrl}))
	if !root.vault.confirmReset {
		t.Fatal("Ctrl+R 应进入重置确认")
	}
	for _, r := range "RESET" {
		root = upd(root, press(string(r)))
	}
	root = upd(root, pressKey(tea.KeyEnter))
	if root.vault.err != "" {
		t.Fatalf("重置失败: %s", root.vault.err)
	}
	if !root.Store.VaultReady() {
		t.Fatalf("重置后密码库应可用: %+v", root.Store.VaultInfo())
	}
	if _, ok, _ := root.Store.Password(h); ok {
		t.Fatal("重置后旧密码应不可读")
	}
	for _, got := range root.Store.Hosts() {
		if got.HasPassword {
			t.Fatalf("重置后 HasPassword 应为 false: %+v", got)
		}
	}
	// 按 Enter 进入列表
	root = upd(root, pressKey(tea.KeyEnter))
	if root.page != pageList {
		t.Fatalf("重置后 Enter 应进入列表，实际 %d", root.page)
	}
	view := root.View().Content
	if !strings.Contains(view, "密码库") || !strings.Contains(view, "建议独立保管") {
		t.Fatalf("重置后应显示新路径与同目录提示:\n%s", view)
	}
}

// TestVaultStatusLinesKeepsHintOnNarrowWidth 回归：窄终端下「建议独立保管」
// 不能被路径截断吃掉（提示比完整路径更重要）。
func TestVaultStatusLinesKeepsHintOnNarrowWidth(t *testing.T) {
	info := store.VaultInfo{
		VaultPath:      "/home/user/.config/simple-connect/vault.age",
		KeyPath:        "/home/user/.config/simple-connect/vault.key",
		DefaultKeyPath: "/home/user/.config/simple-connect/vault.key",
		KeyInConfigDir: true,
		Ready:          true,
	}
	for _, w := range []int{20, 30, 34, 40, 80} {
		lines := vaultStatusLines(info, w)
		if !strings.Contains(strings.Join(lines, "\n"), "建议独立保管") {
			t.Errorf("宽度 %d 丢失独立保管提示: %q", w, lines)
		}
	}
	if got := len(vaultStatusLines(info, 80)); got != 2 {
		t.Fatalf("宽终端应保持两行，实际 %d 行", got)
	}
	if got := len(vaultStatusLines(info, 30)); got != 3 {
		t.Fatalf("窄终端应把提示分行显示，实际 %d 行", got)
	}
}
