package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
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

// loadTempStore 在临时目录创建独立 Store 实例（模拟多实例并发编辑）。
// 每次调用都会 OpenFile 锁文件，fd 相互独立，可真实竞争 flock。
func loadTempStore(t *testing.T, dir string) *Store {
	t.Helper()
	os.Setenv("XDG_CONFIG_HOME", dir)
	t.Cleanup(func() { os.Unsetenv("XDG_CONFIG_HOME") })
	s, err := Load()
	if err != nil {
		t.Fatalf("加载失败: %v", err)
	}
	return s
}

func TestStoreConcurrentAdd(t *testing.T) {
	dir := t.TempDir()
	const n = 8

	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s := loadTempStore(t, dir)
			h := &model.Host{Name: fmt.Sprintf("主机%d", i), Host: "127.0.0.1", Port: 22, User: "root", Auth: model.AuthPassword}
			errs[i] = s.Add(h)
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("第 %d 个实例 Add 失败: %v", i, err)
		}
	}

	s := loadTempStore(t, dir)
	if len(s.Hosts()) != n {
		t.Fatalf("并发新增后应有 %d 个连接，实际 %d", n, len(s.Hosts()))
	}
	// 校验文件为合法 JSON 且无损坏
	b, err := os.ReadFile(filepath.Join(dir, "simple-connect", "hosts.json"))
	if err != nil {
		t.Fatalf("读取 hosts.json 失败: %v", err)
	}
	var hosts []*model.Host
	if err := json.Unmarshal(b, &hosts); err != nil {
		t.Fatalf("hosts.json 损坏: %v", err)
	}
	if len(hosts) != n {
		t.Fatalf("文件内应有 %d 个连接，实际 %d", n, len(hosts))
	}
}

func TestStoreConcurrentUpdateSameHost(t *testing.T) {
	dir := t.TempDir()
	s := loadTempStore(t, dir)
	h := &model.Host{Name: "原主机", Host: "127.0.0.1", User: "root", Auth: model.AuthPassword}
	if err := s.Add(h); err != nil {
		t.Fatalf("预置连接失败: %v", err)
	}
	id := h.ID

	const n = 6
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s2 := loadTempStore(t, dir)
			cur := s2.Find(id)
			if cur == nil {
				errs[i] = fmt.Errorf("连接不存在")
				return
			}
			cur.Name = fmt.Sprintf("并发改%d", i)
			errs[i] = s2.Update(cur)
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("第 %d 个实例 Update 失败: %v", i, err)
		}
	}

	// 并发更新同一主机：最终保留其中某一次完整写入（后写整体覆盖），文件不损坏不丢失
	s3 := loadTempStore(t, dir)
	if len(s3.Hosts()) != 1 {
		t.Fatalf("应有 1 个连接，实际 %d", len(s3.Hosts()))
	}
	got := s3.Find(id)
	if got == nil {
		t.Fatal("连接不应丢失")
	}
	want := false
	for i := 0; i < n; i++ {
		if got.Name == fmt.Sprintf("并发改%d", i) {
			want = true
			break
		}
	}
	if !want {
		t.Fatalf("连接名称应为某次写入值，实际 %q", got.Name)
	}
}

func TestStoreConcurrentAddDelete(t *testing.T) {
	dir := t.TempDir()
	s := loadTempStore(t, dir)
	// 预置 3 个待删除连接
	deleteIDs := make([]string, 3)
	for i := 0; i < 3; i++ {
		h := &model.Host{Name: fmt.Sprintf("待删%d", i), Host: "127.0.0.1", User: "root", Auth: model.AuthPassword}
		if err := s.Add(h); err != nil {
			t.Fatalf("预置连接失败: %v", err)
		}
		deleteIDs[i] = h.ID
	}

	var wg sync.WaitGroup
	errs := make([]error, 6)
	// 并发：3 个实例各新增一个，3 个实例各删除一个预置连接（互不干扰）
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s2 := loadTempStore(t, dir)
			if i < 3 {
				h := &model.Host{Name: fmt.Sprintf("新增%d", i), Host: "127.0.0.1", User: "root", Auth: model.AuthPassword}
				errs[i] = s2.Add(h)
			} else {
				errs[i] = s2.Delete(deleteIDs[i-3])
			}
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("第 %d 个实例操作失败: %v", i, err)
		}
	}

	s3 := loadTempStore(t, dir)
	if len(s3.Hosts()) != 3 {
		t.Fatalf("最终应有 3 个连接（3 新增，3 删除），实际 %d", len(s3.Hosts()))
	}
	for _, id := range deleteIDs {
		if s3.Find(id) != nil {
			t.Fatalf("连接 %s 应已删除", id)
		}
	}
	// 校验文件合法无重复
	b, err := os.ReadFile(filepath.Join(dir, "simple-connect", "hosts.json"))
	if err != nil {
		t.Fatalf("读取 hosts.json 失败: %v", err)
	}
	var hosts []*model.Host
	if err := json.Unmarshal(b, &hosts); err != nil {
		t.Fatalf("hosts.json 损坏: %v", err)
	}
	seen := map[string]bool{}
	for _, e := range hosts {
		if seen[e.ID] {
			t.Fatalf("存在重复 ID: %s", e.ID)
		}
		seen[e.ID] = true
	}
}

func TestStoreReloadSeesOtherWrites(t *testing.T) {
	dir := t.TempDir()
	s1 := loadTempStore(t, dir)
	s2 := loadTempStore(t, dir)

	if err := s1.Add(&model.Host{Name: "实例A新增", Host: "10.0.0.1", User: "root", Auth: model.AuthPassword}); err != nil {
		t.Fatalf("实例A新增失败: %v", err)
	}
	// 实例 B 未 Reload 前看不到
	if len(s2.Hosts()) != 0 {
		t.Fatalf("未 Reload 前实例 B 不应看到新连接")
	}
	if err := s2.Reload(); err != nil {
		t.Fatalf("Reload 失败: %v", err)
	}
	if len(s2.Hosts()) != 1 || s2.Hosts()[0].Name != "实例A新增" {
		t.Fatalf("Reload 后实例 B 应看到实例 A 的写入，实际 %+v", s2.Hosts())
	}
}
