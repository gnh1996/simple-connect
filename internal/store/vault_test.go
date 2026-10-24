package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"filippo.io/age"

	"simple-connect/internal/model"
)

func newVaultTestStore(t *testing.T) (string, *Store) {
	t.Helper()
	dir := t.TempDir()
	s, err := LoadWithOptions(Options{Dir: dir})
	if err != nil {
		t.Fatalf("加载失败: %v", err)
	}
	if !s.VaultReady() {
		t.Fatalf("密码库应可用: %+v", s.VaultInfo())
	}
	return dir, s
}

func mustPassword(t *testing.T, s *Store, h *model.Host, want string) {
	t.Helper()
	got, ok, err := s.Password(h)
	if err != nil || !ok || got != want {
		t.Fatalf("密码异常: err=%v ok=%v got=%q want=%q", err, ok, got, want)
	}
}

func TestVaultEncryptedAtRest(t *testing.T) {
	dir, s := newVaultTestStore(t)
	info := s.VaultInfo()
	if info.VaultPath != filepath.Join(dir, vaultFile) {
		t.Fatalf("密码本路径异常: %q", info.VaultPath)
	}
	if info.KeyPath != filepath.Join(dir, keyFile) {
		t.Fatalf("默认密钥路径异常: %q", info.KeyPath)
	}
	if !info.KeyInConfigDir {
		t.Fatal("默认密钥应被识别为与密码本同目录")
	}

	h := &model.Host{Name: "加密机", Host: "127.0.0.1", User: "root", Auth: model.AuthPassword}
	if err := s.Add(h); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPassword(h, "s3cret-value"); err != nil {
		t.Fatal(err)
	}
	mustPassword(t, s, h, "s3cret-value")

	vaultBytes, err := os.ReadFile(info.VaultPath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(vaultBytes, []byte("s3cret-value")) {
		t.Fatal("密码本中不应出现明文密码")
	}
	if !bytes.Contains(vaultBytes, []byte("age-encryption.org")) {
		t.Fatalf("密码本应为 age 格式，实际开头: %q", vaultBytes[:min(32, len(vaultBytes))])
	}
	keyBytes, err := os.ReadFile(info.KeyPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(keyBytes, []byte("AGE-SECRET-KEY-1")) {
		t.Fatal("密钥文件应为 age X25519 identity")
	}
	if runtime.GOOS != "windows" {
		for _, p := range []string{info.VaultPath, info.KeyPath} {
			st, err := os.Stat(p)
			if err != nil {
				t.Fatal(err)
			}
			if st.Mode().Perm() != 0o600 {
				t.Fatalf("%s 权限应为 0600，实际 %04o", p, st.Mode().Perm())
			}
		}
	}
	if info.KeyPermWarning != "" {
		t.Fatalf("0600 密钥不应有权限警告: %q", info.KeyPermWarning)
	}
}

func TestVaultKeyMissingDoesNotRegenerate(t *testing.T) {
	dir, s := newVaultTestStore(t)
	h := &model.Host{Name: "主机", Host: "127.0.0.1", User: "root", Auth: model.AuthPassword}
	_ = s.Add(h)
	if err := s.SetPassword(h, "pass"); err != nil {
		t.Fatal(err)
	}
	keyPath := s.VaultInfo().KeyPath
	if err := os.Remove(keyPath); err != nil {
		t.Fatal(err)
	}
	vaultBefore, err := os.ReadFile(s.VaultInfo().VaultPath)
	if err != nil {
		t.Fatal(err)
	}

	s2, err := LoadWithOptions(Options{Dir: dir})
	if err != nil {
		t.Fatalf("缺少密钥时 Load 不应返回致命错误: %v", err)
	}
	if s2.VaultReady() {
		t.Fatal("缺少密钥时密码库不应可用")
	}
	if !strings.Contains(s2.VaultInfo().Problem, "密钥文件不存在") {
		t.Fatalf("应给出密钥缺失原因，实际 %q", s2.VaultInfo().Problem)
	}
	if _, err := os.Stat(keyPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("不得自动生成新密钥")
	}
	vaultAfter, err := os.ReadFile(s2.VaultInfo().VaultPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(vaultBefore, vaultAfter) {
		t.Fatal("缺少密钥时不得改写密码本")
	}
}

func TestVaultKeyMismatchDoesNotOverwrite(t *testing.T) {
	dir, s := newVaultTestStore(t)
	h := &model.Host{Name: "主机", Host: "127.0.0.1", User: "root", Auth: model.AuthPassword}
	_ = s.Add(h)
	if err := s.SetPassword(h, "pass"); err != nil {
		t.Fatal(err)
	}
	info := s.VaultInfo()
	before, _ := os.ReadFile(info.VaultPath)

	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	if err := writeIdentity(info.KeyPath, id); err != nil {
		t.Fatal(err)
	}

	s2, err := LoadWithOptions(Options{Dir: dir})
	if err != nil {
		t.Fatalf("Load 不应返回致命错误: %v", err)
	}
	if s2.VaultReady() {
		t.Fatal("错误密钥时密码库不应可用")
	}
	if s2.VaultInfo().Problem == "" {
		t.Fatal("应给出密钥不匹配原因")
	}
	after, _ := os.ReadFile(info.VaultPath)
	if !bytes.Equal(before, after) {
		t.Fatal("密钥不匹配时不得改写密码本")
	}
}

func TestVaultCustomKeyPath(t *testing.T) {
	dir, s := newVaultTestStore(t)
	h := &model.Host{Name: "主机", Host: "127.0.0.1", User: "root", Auth: model.AuthPassword}
	_ = s.Add(h)
	if err := s.SetPassword(h, "pass"); err != nil {
		t.Fatal(err)
	}
	info := s.VaultInfo()

	keyBytes, err := os.ReadFile(info.KeyPath)
	if err != nil {
		t.Fatal(err)
	}
	customDir := t.TempDir()
	customKey := filepath.Join(customDir, "usb.key")
	if err := os.WriteFile(customKey, keyBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.SetKeyPath(customKey); err != nil {
		t.Fatalf("切换密钥路径失败: %v", err)
	}
	if got := s.VaultInfo().KeyPath; got != customKey {
		t.Fatalf("keyPath=%q want %q", got, customKey)
	}
	if s.VaultInfo().KeyInConfigDir {
		t.Fatal("外部密钥不应被识别为配置目录内")
	}
	mustPassword(t, s, h, "pass")

	// config.json 应持久化自定义路径；重新加载不再依赖默认密钥。
	s2, err := LoadWithOptions(Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if !s2.VaultReady() || s2.VaultInfo().KeyPath != customKey {
		t.Fatalf("重载未使用自定义密钥: %+v", s2.VaultInfo())
	}
	mustPassword(t, s2, h, "pass")

	// 显式 --key-file 覆盖 config。
	other := filepath.Join(t.TempDir(), "other.key")
	if err := os.WriteFile(other, keyBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	s3, err := LoadWithOptions(Options{Dir: dir, KeyFile: other})
	if err != nil {
		t.Fatal(err)
	}
	if !s3.VaultReady() || s3.VaultInfo().KeyPath != other {
		t.Fatalf("--key-file 未覆盖配置: %+v", s3.VaultInfo())
	}
}

func TestVaultSetKeyPathRejectsMismatch(t *testing.T) {
	_, s := newVaultTestStore(t)
	h := &model.Host{Name: "主机", Host: "127.0.0.1", User: "root", Auth: model.AuthPassword}
	_ = s.Add(h)
	if err := s.SetPassword(h, "pass"); err != nil {
		t.Fatal(err)
	}
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(t.TempDir(), "other.key")
	if err := writeIdentity(other, id); err != nil {
		t.Fatal(err)
	}
	if err := s.SetKeyPath(other); err == nil {
		t.Fatal("不匹配的密钥应被拒绝")
	}
	if s.VaultInfo().KeyPath == other {
		t.Fatal("验证失败时不得持久化错误路径")
	}
	mustPassword(t, s, h, "pass")
}

func TestVaultCustomKeyMissingDoesNotGenerate(t *testing.T) {
	dir := t.TempDir()
	custom := filepath.Join(t.TempDir(), "missing", "vault.key")
	cfg, _ := json.Marshal(configData{KeyPath: custom})
	if err := os.WriteFile(filepath.Join(dir, configFile), cfg, 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := LoadWithOptions(Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if s.VaultReady() {
		t.Fatal("自定义密钥缺失时不应自动生成/解锁")
	}
	if !strings.Contains(s.VaultInfo().Problem, "密钥文件不存在") {
		t.Fatalf("应提示密钥缺失，实际 %q", s.VaultInfo().Problem)
	}
	if _, err := os.Stat(filepath.Join(dir, keyFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("不得在默认路径偷偷生成新密钥")
	}
	if _, err := os.Stat(filepath.Join(dir, vaultFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("不得在密钥缺失时创建空密码本")
	}
}

func TestVaultResetKeepsUnrelatedCustomKey(t *testing.T) {
	dir, s := newVaultTestStore(t)
	h := &model.Host{Name: "主机", Host: "127.0.0.1", User: "root", Auth: model.AuthPassword}
	_ = s.Add(h)
	if err := s.SetPassword(h, "pass"); err != nil {
		t.Fatal(err)
	}
	// 把正确密钥复制到自定义路径并写入 config，然后把自定义路径换成另一把密钥。
	keyBytes, _ := os.ReadFile(s.VaultInfo().KeyPath)
	custom := filepath.Join(t.TempDir(), "other.key")
	if err := os.WriteFile(custom, keyBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.SetKeyPath(custom); err != nil {
		t.Fatal(err)
	}
	otherID, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	if err := writeIdentity(custom, otherID); err != nil {
		t.Fatal(err)
	}
	s2, err := LoadWithOptions(Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if s2.VaultReady() {
		t.Fatal("错误自定义密钥时不应解锁")
	}
	warning, err := s2.ResetVault()
	if err != nil {
		t.Fatalf("重置失败: %v", err)
	}
	if !strings.Contains(warning, "不匹配") {
		t.Fatalf("应提示旧密钥未删除，实际 %q", warning)
	}
	if _, err := os.Stat(custom); err != nil {
		t.Fatal("不匹配的自定义密钥不应被删除")
	}
	if !s2.VaultReady() {
		t.Fatalf("重置后应可用: %+v", s2.VaultInfo())
	}
}

func TestVaultReset(t *testing.T) {
	dir, s := newVaultTestStore(t)
	h1 := &model.Host{Name: "A", Host: "127.0.0.1", User: "root", Auth: model.AuthPassword}
	h2 := &model.Host{Name: "B", Host: "127.0.0.1", User: "root", Auth: model.AuthPassword}
	_ = s.Add(h1)
	_ = s.Add(h2)
	_ = s.SetPassword(h1, "p1")
	_ = s.SetPassword(h2, "p2")

	oldInfo := s.VaultInfo()
	oldKey, _ := os.ReadFile(oldInfo.KeyPath)
	oldVault, _ := os.ReadFile(oldInfo.VaultPath)

	if _, err := s.ResetVault(); err != nil {
		t.Fatalf("重置失败: %v", err)
	}
	info := s.VaultInfo()
	if !info.Ready || info.KeyPath != filepath.Join(dir, keyFile) {
		t.Fatalf("重置后状态异常: %+v", info)
	}
	newKey, err := os.ReadFile(info.KeyPath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(oldKey, newKey) {
		t.Fatal("重置后应生成新密钥")
	}
	newVault, err := os.ReadFile(info.VaultPath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(oldVault, newVault) {
		t.Fatal("重置后应生成新密码本")
	}
	if _, ok, _ := s.Password(h1); ok {
		t.Fatal("重置后旧密码应不可读")
	}
	if _, err := os.Stat(oldInfo.VaultPath + ".bak"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("重置应删除密码本备份")
	}
	for _, h := range s.Hosts() {
		if h.HasPassword {
			t.Fatalf("重置后 HasPassword 应为 false: %+v", h)
		}
	}

	// 重新加载：新密钥可用、旧密码标记已清。
	s2, err := LoadWithOptions(Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if !s2.VaultReady() {
		t.Fatalf("重置后重载应可用: %+v", s2.VaultInfo())
	}
	if _, ok, _ := s2.Password(h1); ok {
		t.Fatal("重置后重载仍读到旧密码")
	}
	for _, h := range s2.Hosts() {
		if h.HasPassword {
			t.Fatalf("重载后 HasPassword 应为 false: %+v", h)
		}
	}
}

func TestVaultDeleteRemovesBackup(t *testing.T) {
	_, s := newVaultTestStore(t)
	h := &model.Host{Name: "主机", Host: "127.0.0.1", User: "root", Auth: model.AuthPassword}
	if err := s.Add(h); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPassword(h, "pass"); err != nil {
		t.Fatal(err)
	}
	vaultPath := s.VaultInfo().VaultPath
	if _, err := os.Stat(vaultPath + ".bak"); err != nil {
		t.Fatalf("保存密码后应存在备份: %v", err)
	}
	if err := s.Delete(h.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(vaultPath + ".bak"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("删除密码后不应保留含旧密码的备份")
	}
}

func TestVaultKeyPermissionWarning(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 无 Unix 权限位")
	}
	dir, s := newVaultTestStore(t)
	keyPath := s.VaultInfo().KeyPath
	if err := os.Chmod(keyPath, 0o644); err != nil {
		t.Fatal(err)
	}
	s2, err := LoadWithOptions(Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if !s2.VaultReady() {
		t.Fatalf("密码库应可用: %+v", s2.VaultInfo())
	}
	if s2.VaultInfo().KeyPermWarning == "" {
		t.Fatal("密钥权限过宽时应给出警告")
	}
}

func TestVaultConcurrentSetPassword(t *testing.T) {
	dir, s := newVaultTestStore(t)
	const n = 6
	hosts := make([]*model.Host, n)
	for i := 0; i < n; i++ {
		hosts[i] = &model.Host{Name: "h", Host: "127.0.0.1", User: "root", Auth: model.AuthPassword}
		if err := s.Add(hosts[i]); err != nil {
			t.Fatal(err)
		}
	}

	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s2, err := LoadWithOptions(Options{Dir: dir})
			if err != nil {
				errs[i] = err
				return
			}
			h := s2.Find(hosts[i].ID)
			if h == nil {
				errs[i] = errors.New("host missing")
				return
			}
			errs[i] = s2.SetPassword(h, "pass-"+h.ID)
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("第 %d 个实例保存密码失败: %v", i, err)
		}
	}
	s3, err := LoadWithOptions(Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range hosts {
		mustPassword(t, s3, h, "pass-"+h.ID)
	}
}

func TestVaultConfigCorrupt(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, configFile), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := LoadWithOptions(Options{Dir: dir})
	if err != nil {
		t.Fatalf("配置损坏不应阻塞 hosts 加载: %v", err)
	}
	if s.VaultReady() || s.VaultInfo().Problem == "" {
		t.Fatalf("配置损坏时应停在密码库页: %+v", s.VaultInfo())
	}
}

// TestVaultDeletePasswordlessHostKeepsBackup 回归：删除没有密码的主机时
// 密码本里并没有对应条目，不应顺带删掉 vault.age.bak（备份里可能还有
// 其它主机的密码）。只有真正删除密码时才清理备份。
func TestVaultDeletePasswordlessHostKeepsBackup(t *testing.T) {
	_, s := newVaultTestStore(t)
	withPass := &model.Host{Name: "有密码", Host: "127.0.0.1", User: "root", Auth: model.AuthPassword}
	noPass := &model.Host{Name: "无密码", Host: "127.0.0.1", User: "root", Auth: model.AuthPassword}
	if err := s.Add(withPass); err != nil {
		t.Fatal(err)
	}
	if err := s.Add(noPass); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPassword(withPass, "pass"); err != nil {
		t.Fatal(err)
	}
	bak := s.VaultInfo().VaultPath + ".bak"
	if _, err := os.Stat(bak); err != nil {
		t.Fatalf("保存密码后应存在备份: %v", err)
	}

	if err := s.Delete(noPass.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(bak); err != nil {
		t.Fatalf("删除无密码主机不应删除密码本备份: %v", err)
	}

	if err := s.Delete(withPass.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(bak); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("删除有密码主机后应清理备份: %v", err)
	}
}
