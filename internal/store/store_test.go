package store

import (
	"os"
	"path/filepath"
	"testing"

	"simple-connect/internal/model"
)

func TestStoreCRUD(t *testing.T) {
	dir := t.TempDir()
	os.Setenv("XDG_CONFIG_HOME", dir)
	t.Cleanup(func() { os.Unsetenv("XDG_CONFIG_HOME") })

	s, err := Load()
	if err != nil {
		t.Fatalf("加载失败: %v", err)
	}
	if len(s.Hosts()) != 0 {
		t.Fatalf("初始连接数应为 0，实际 %d", len(s.Hosts()))
	}

	h := &model.Host{Name: "测试机", Host: "127.0.0.1", Port: 22, User: "root", Auth: model.AuthPassword}
	if err := s.Add(h); err != nil {
		t.Fatalf("新增失败: %v", err)
	}
	if h.ID == "" {
		t.Fatal("新增后应生成 ID")
	}
	if err := s.SetPassword(h, "secret123"); err != nil {
		t.Fatalf("保存密码失败: %v", err)
	}
	if pass, ok := s.Password(h); !ok || pass != "secret123" {
		t.Fatalf("密码读取异常: ok=%v pass=%q", ok, pass)
	}

	// 重新加载验证持久化
	s2, err := Load()
	if err != nil {
		t.Fatalf("重新加载失败: %v", err)
	}
	if len(s2.Hosts()) != 1 {
		t.Fatalf("重载后连接数应为 1，实际 %d", len(s2.Hosts()))
	}
	h2 := s2.Find(h.ID)
	if h2 == nil || h2.Name != "测试机" || !h2.HasPassword {
		t.Fatalf("重载后数据异常: %+v", h2)
	}
	if pass, ok := s2.Password(h2); !ok || pass != "secret123" {
		t.Fatalf("重载后密码异常: ok=%v", ok)
	}

	if err := s2.Delete(h.ID); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	if len(s2.Hosts()) != 0 {
		t.Fatal("删除后连接数应为 0")
	}
	if pass, ok := s2.Password(h2); ok && pass == "secret123" {
		t.Fatal("删除后密码应被清除")
	}
}

func TestFileSecrets(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secrets.json")
	fs := &fileSecrets{path: path}

	if v, ok, _ := fs.Get("k"); ok || v != "" {
		t.Fatal("空存储不应有值")
	}
	if err := fs.Set("k", "v1"); err != nil {
		t.Fatalf("Set 失败: %v", err)
	}
	st, _ := os.Stat(path)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("文件权限应为 0600，实际 %v", st.Mode().Perm())
	}
	if v, ok, _ := fs.Get("k"); !ok || v != "v1" {
		t.Fatalf("Get 失败: %v %v", ok, v)
	}
	if err := fs.Delete("k"); err != nil {
		t.Fatalf("Delete 失败: %v", err)
	}
	if v, ok, _ := fs.Get("k"); ok || v != "" {
		t.Fatal("删除后不应有值")
	}
}
