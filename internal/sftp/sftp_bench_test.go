package sftp

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/ssh"

	"simple-connect/internal/model"
	sshc "simple-connect/internal/ssh"
	"simple-connect/internal/testutil"
)

// benchDial 基准专用连接（等价 sftp_test.go 的 dialTest，接受 *testing.B）
func benchDial(b *testing.B, env testutil.SFTPEnv) *Conn {
	b.Helper()
	h, p := testutil.SplitHostPort(env.Addr)
	host := &model.Host{Name: "bench", Host: h, Port: p, User: "tester", Auth: model.AuthPassword}
	conn, err := Dial(host, "secret", sshc.WithHostKeyCallback(ssh.InsecureIgnoreHostKey()))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { conn.Close() })
	return conn
}

// BenchmarkList 大目录列表：SFTP ReadDir + SortEntries。
// 1000 个条目一次往返，反映目录浏览的核心路径。
func BenchmarkList(b *testing.B) {
	env := testutil.StartSFTP(b)
	conn := benchDial(b, env)

	const n = 1000
	dir := filepath.Join(env.Root, "big")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		b.Fatal(err)
	}
	for i := 0; i < n; i++ {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("file-%04d.txt", i)), []byte("x"), 0o644); err != nil {
			b.Fatal(err)
		}
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		entries, err := List(conn.Client, dir)
		if err != nil {
			b.Fatal(err)
		}
		if len(entries) != n {
			b.Fatalf("条目数 %d，期望 %d", len(entries), n)
		}
	}
}

// BenchmarkSortEntries 排序比较器开销（目录优先 + 忽略大小写）。
// 每次迭代拷贝一份乱序切片（对齐 List 每次拿到服务端无序返回的真实场景）。
func BenchmarkSortEntries(b *testing.B) {
	dir := b.TempDir()
	const n = 5000
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("file-%04d.txt", i)
		if i%3 == 0 {
			if err := os.MkdirAll(filepath.Join(dir, "dir-"+name), 0o755); err != nil {
				b.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			b.Fatal(err)
		}
	}
	des, err := os.ReadDir(dir)
	if err != nil {
		b.Fatal(err)
	}
	entries := make([]os.FileInfo, 0, len(des))
	for _, de := range des {
		info, err := de.Info()
		if err != nil {
			b.Fatal(err)
		}
		entries = append(entries, info)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		work := make([]os.FileInfo, len(entries))
		copy(work, entries)
		SortEntries(work)
	}
}

// BenchmarkUpload 单文件上传吞吐（8MB 触发 1MB buffer 的并发分片写路径）。
// 本地回环内存服务器，反映高延迟链路下单次大 buffer 写 vs 分片数量关系。
func BenchmarkUpload(b *testing.B) {
	env := testutil.StartSFTP(b)
	conn := benchDial(b, env)

	content := make([]byte, 8<<20)
	for i := range content {
		content[i] = byte(i * 31)
	}
	local := filepath.Join(b.TempDir(), "up.bin")
	if err := os.WriteFile(local, content, 0o644); err != nil {
		b.Fatal(err)
	}
	remote := filepath.Join(env.Root, "up.bin")

	b.SetBytes(int64(len(content)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		t := NewTransfer("up.bin", true)
		Upload(conn.Client, t, local, remote)
		if _, _, finished, err := t.Snapshot(); !finished || err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkDownload 单文件下载吞吐（与上传对称，走 pkg/sftp 默认 read 路径）。
func BenchmarkDownload(b *testing.B) {
	env := testutil.StartSFTP(b)
	conn := benchDial(b, env)

	content := make([]byte, 8<<20)
	for i := range content {
		content[i] = byte(i * 31)
	}
	remote := filepath.Join(env.Root, "dl.bin")
	if err := os.WriteFile(remote, content, 0o644); err != nil {
		b.Fatal(err)
	}
	local := filepath.Join(b.TempDir(), "dl.bin")

	b.SetBytes(int64(len(content)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		t := NewTransfer("dl.bin", false)
		Download(conn.Client, t, remote, local)
		if _, _, finished, err := t.Snapshot(); !finished || err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkUploadPath 目录递归上传：先 dirSizeLocal 统计再逐文件传输，
// 覆盖 filepath.WalkDir + MkdirAll 路径。
func BenchmarkUploadPath(b *testing.B) {
	env := testutil.StartSFTP(b)
	conn := benchDial(b, env)

	local := b.TempDir()
	// 嵌套目录：3 层 × 每层 10 个小文件
	for l := 0; l < 3; l++ {
		sub := filepath.Join(append([]string{local}, repeat("sub", l+1)...)...)
		if err := os.MkdirAll(sub, 0o755); err != nil {
			b.Fatal(err)
		}
		for f := 0; f < 10; f++ {
			if err := os.WriteFile(filepath.Join(sub, fmt.Sprintf("f%02d.dat", f)), make([]byte, 4096), 0o644); err != nil {
				b.Fatal(err)
			}
		}
	}
	remote := filepath.Join(env.Root, "proj")

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		t := NewTransfer("proj", true)
		UploadPath(conn.Client, t, local, remote)
		if _, _, finished, err := t.Snapshot(); !finished || err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkFormatSize 人类可读大小格式化（UI 渲染热路径，每行条目都会调用）
func BenchmarkFormatSize(b *testing.B) {
	sizes := []int64{0, 1023, 1024, 1536, 5 << 20, 3 << 30, 1 << 40, 1 << 50}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = FormatSize(sizes[i%len(sizes)])
	}
}

func repeat(s string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = s
	}
	return out
}