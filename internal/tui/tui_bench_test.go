package tui

import (
	"fmt"
	"io/fs"
	"os"
	"time"
	"testing"

	"simple-connect/internal/model"
	sshc "simple-connect/internal/ssh"
	"simple-connect/internal/store"
)

// benchStore 基准专用 store（临时配置目录，避免污染真实配置）
func benchStore(b *testing.B) *store.Store {
	b.Helper()
	dir := b.TempDir()
	old, had := os.LookupEnv("XDG_CONFIG_HOME")
	if err := os.Setenv("XDG_CONFIG_HOME", dir); err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() {
		if had {
			_ = os.Setenv("XDG_CONFIG_HOME", old)
		} else {
			_ = os.Unsetenv("XDG_CONFIG_HOME")
		}
	})
	s, err := store.Load()
	if err != nil {
		b.Fatal(err)
	}
	return s
}

// BenchmarkListView 连接列表整帧渲染：1000 个连接全部绘制（含状态图标、
// lipgloss 样式、padRight CJK 对齐）。每按键一次触发，是交互流畅性的关键路径。
func BenchmarkListView(b *testing.B) {
	s := benchStore(b)
	const n = 1000
	hosts := make([]*model.Host, n)
	for i := range hosts {
		hosts[i] = &model.Host{
			ID:         fmt.Sprintf("id-%04d", i),
			Name:       fmt.Sprintf("生产服务器-%04d", i),
			Host:       fmt.Sprintf("10.0.%d.%d", i/250, i%250+1),
			Port:       22,
			User:       "root",
			Auth:       model.AuthPassword,
			HasPassword: true,
		}
	}
	m := &listModel{store: s, hosts: hosts, status: map[string]sshc.Status{}}
	m.applyFilter()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = m.View()
	}
}

// BenchmarkListFilter 过滤 10000 个连接（逐条 strings.Contains 扫描）。
// 每次输入字符触发，是过滤输入时的关键路径。
func BenchmarkListFilter(b *testing.B) {
	s := benchStore(b)
	const n = 10000
	hosts := make([]*model.Host, n)
	for i := range hosts {
		hosts[i] = &model.Host{
			ID:   fmt.Sprintf("id-%04d", i),
			Name: fmt.Sprintf("web-%04d", i),
			Host: fmt.Sprintf("10.0.%d.%d", i/250, i%250+1),
			User: "root", Auth: model.AuthPassword,
		}
	}
	m := &listModel{store: s, hosts: hosts, filter: "web-0"}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.applyFilter()
	}
}

// benchFileInfo 合成的目录条目（benchFileInfo 实现 fs.FileInfo）
type benchFileInfo struct {
	name string
	size int64
	dir  bool
	mod  time.Time
}

func (f benchFileInfo) Name() string       { return f.name }
func (f benchFileInfo) Size() int64        { return f.size }
func (f benchFileInfo) Mode() fs.FileMode  { return 0o644 }
func (f benchFileInfo) ModTime() time.Time { return f.mod }
func (f benchFileInfo) IsDir() bool        { return f.dir }
func (f benchFileInfo) Sys() any           { return nil }

// BenchmarkSFTPView 双栏 SFTP 整帧渲染：10000 条目的大目录滚动到中部，
// 仅绘制可视行（24 行/栏）。衡量 padRight + runewidth.Truncate + lipgloss 的帧开销。
func BenchmarkSFTPView(b *testing.B) {
	s := benchStore(b)
	h := &model.Host{Name: "bench", Host: "10.0.0.1", User: "root", Auth: model.AuthPassword}
	m := newSFTPModel(s, h, "", nil)

	const n = 10000
	entries := make([]fs.FileInfo, n)
	base := time.Now()
	for i := range entries {
		entries[i] = benchFileInfo{
			name: fmt.Sprintf("file-%05d.txt", i),
			size: int64(i) * 1000,
			mod:  base,
		}
	}
	m.entries = entries
	m.localEntries = entries
	m.cwd = "/srv/app"
	m.localCwd = "/home/user"
	m.width, m.height = 100, 30
	m.remoteTop = 5000 // 滚动到中部，顶部显示 ▴
	m.localTop = 5000

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = m.View()
	}
}