package sftp

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"simple-connect/internal/model"
	sshc "simple-connect/internal/ssh"
	"simple-connect/internal/testutil"
)

func TestFormatSize(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0 B"},
		{1023, "1023 B"},
		{1024, "1.0 KiB"},
		{1536, "1.5 KiB"},
		{5 * 1024 * 1024, "5.0 MiB"},
		{3 * 1024 * 1024 * 1024, "3.0 GiB"},
	}
	for _, c := range cases {
		if got := FormatSize(c.in); got != c.want {
			t.Errorf("FormatSize(%d) = %q，期望 %q", c.in, got, c.want)
		}
	}
}

func TestListSortOrder(t *testing.T) {
	env := testutil.StartSFTP(t)
	// 预置文件与目录
	_ = os.MkdirAll(filepath.Join(env.Root, "bdir"), 0o755)
	_ = os.MkdirAll(filepath.Join(env.Root, "adir"), 0o755)
	_ = os.WriteFile(filepath.Join(env.Root, "zfile.txt"), []byte("x"), 0o644)
	_ = os.WriteFile(filepath.Join(env.Root, "afile.txt"), []byte("x"), 0o644)

	conn := dialTest(t, env)
	defer conn.Close()

	entries, err := List(conn.Client, env.Root)
	if err != nil {
		t.Fatalf("列目录失败: %v", err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	want := []string{"adir", "bdir", "afile.txt", "zfile.txt"}
	if len(names) != len(want) {
		t.Fatalf("条目数量不符: %v", names)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("排序错误: 期望 %v，实际 %v", want, names)
		}
	}
}

func TestTransferUploadDownload(t *testing.T) {
	env := testutil.StartSFTP(t)
	conn := dialTest(t, env)
	defer conn.Close()

	remoteFile := filepath.Join(env.Root, "src.dat")

	// 本地源文件
	local := filepath.Join(t.TempDir(), "src.dat")
	content := make([]byte, 4096)
	for i := range content {
		content[i] = byte(i % 251)
	}
	if err := os.WriteFile(local, content, 0o644); err != nil {
		t.Fatal(err)
	}

	// 上传
	tup := NewTransfer("src.dat", true)
	Upload(conn.Client, tup, local, remoteFile)
	_, _, finished, err := tup.Snapshot()
	if !finished || err != nil {
		t.Fatalf("上传异常: finished=%v err=%v", finished, err)
	}
	if b, e := os.ReadFile(remoteFile); e != nil || string(b) != string(content) {
		t.Fatalf("远程文件校验失败: %v", e)
	}

	// 下载
	dst := filepath.Join(t.TempDir(), "dst.dat")
	td := NewTransfer("dst.dat", false)
	Download(conn.Client, td, remoteFile, dst)
	_, _, finished, err = td.Snapshot()
	if !finished || err != nil {
		t.Fatalf("下载异常: finished=%v err=%v", finished, err)
	}
	if b, e := os.ReadFile(dst); e != nil || string(b) != string(content) {
		t.Fatalf("下载内容校验失败: %v", e)
	}

	// 删除
	if err := Remove(conn.Client, remoteFile, false); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	if _, e := os.Stat(remoteFile); !os.IsNotExist(e) {
		t.Fatal("远程文件应已删除")
	}
}

func TestTransferFailure(t *testing.T) {
	env := testutil.StartSFTP(t)
	conn := dialTest(t, env)
	defer conn.Close()

	// 上传不存在的本地文件应报错
	tup := NewTransfer("nope", true)
	Upload(conn.Client, tup, filepath.Join(t.TempDir(), "不存在"), filepath.Join(env.Root, "nope"))
	_, _, finished, err := tup.Snapshot()
	if !finished || err == nil {
		t.Fatalf("上传应失败: finished=%v err=%v", finished, err)
	}
}

func TestRecursiveUploadDownload(t *testing.T) {
	env := testutil.StartSFTP(t)
	conn := dialTest(t, env)
	defer conn.Close()

	local := t.TempDir()
	// 嵌套目录：sub/ 下有文件与空目录 empty/
	_ = os.MkdirAll(filepath.Join(local, "sub", "empty"), 0o755)
	_ = os.WriteFile(filepath.Join(local, "sub", "a.txt"), []byte("aaa"), 0o644)
	_ = os.WriteFile(filepath.Join(local, "sub", "b.bin"), bytes.Repeat([]byte{0x1}, 3000), 0o644)
	_ = os.WriteFile(filepath.Join(local, "top.txt"), []byte("top"), 0o644)
	// 统计本地总字节
	wantTotal := int64(3 + 3000 + 3)

	// 递归上传目录
	remoteDir := filepath.Join(env.Root, "proj")
	tup := NewTransfer("proj", true)
	UploadPath(conn.Client, tup, local, remoteDir)
	_, _, finished, err := tup.Snapshot()
	if !finished || err != nil {
		t.Fatalf("递归上传异常: finished=%v err=%v", finished, err)
	}
	done, total, _, _ := tup.Snapshot()
	if total != wantTotal || done != wantTotal {
		t.Fatalf("上传进度 total=%d done=%d，期望 %d", total, done, wantTotal)
	}

	// 校验远程结构
	checkRemoteFile(t, conn.Client, filepath.Join(remoteDir, "top.txt"), "top")
	checkRemoteFile(t, conn.Client, filepath.Join(remoteDir, "sub", "a.txt"), "aaa")
	checkRemoteFile(t, conn.Client, filepath.Join(remoteDir, "sub", "b.bin"), string(bytes.Repeat([]byte{0x1}, 3000)))
	// 空目录保留
	if st, err := conn.Client.Stat(filepath.Join(remoteDir, "sub", "empty")); err != nil || !st.IsDir() {
		t.Fatalf("远程空目录应保留: %v", err)
	}

	// 递归下载到新目录
	dst := t.TempDir()
	td := NewTransfer("proj", false)
	DownloadPath(conn.Client, td, remoteDir, filepath.Join(dst, "proj"))
	_, _, finished, err = td.Snapshot()
	if !finished || err != nil {
		t.Fatalf("递归下载异常: finished=%v err=%v", finished, err)
	}
	checkLocalFile(t, filepath.Join(dst, "proj", "top.txt"), "top")
	checkLocalFile(t, filepath.Join(dst, "proj", "sub", "a.txt"), "aaa")
	if st, err := os.Stat(filepath.Join(dst, "proj", "sub", "empty")); err != nil || !st.IsDir() {
		t.Fatalf("本地空目录应保留: %v", err)
	}
}

func TestBatchTransfer(t *testing.T) {
	env := testutil.StartSFTP(t)
	conn := dialTest(t, env)
	defer conn.Close()

	local := t.TempDir()
	_ = os.WriteFile(filepath.Join(local, "f1.txt"), []byte("111"), 0o644)
	_ = os.MkdirAll(filepath.Join(local, "dir2"), 0o755)
	_ = os.WriteFile(filepath.Join(local, "dir2", "nested.txt"), bytes.Repeat([]byte{0x2}, 1000), 0o644)

	remoteBase := filepath.Join(env.Root, "batch")
	items := []BatchItem{
		{Src: filepath.Join(local, "f1.txt"), Dst: filepath.Join(remoteBase, "f1.txt")},
		{Src: filepath.Join(local, "dir2"), Dst: filepath.Join(remoteBase, "dir2")},
	}
	tb := NewTransfer("2 项", true)
	BatchTransfer(conn.Client, tb, true, items)
	_, _, finished, err := tb.Snapshot()
	if !finished || err != nil {
		t.Fatalf("批量上传异常: finished=%v err=%v", finished, err)
	}
	checkRemoteFile(t, conn.Client, filepath.Join(remoteBase, "f1.txt"), "111")
	checkRemoteFile(t, conn.Client, filepath.Join(remoteBase, "dir2", "nested.txt"), string(bytes.Repeat([]byte{0x2}, 1000)))

	// 批量下载
	dst := t.TempDir()
	items = []BatchItem{
		{Src: filepath.Join(remoteBase, "f1.txt"), Dst: filepath.Join(dst, "f1.txt")},
		{Src: filepath.Join(remoteBase, "dir2"), Dst: filepath.Join(dst, "dir2")},
	}
	td := NewTransfer("2 项", false)
	BatchTransfer(conn.Client, td, false, items)
	_, _, finished, err = td.Snapshot()
	if !finished || err != nil {
		t.Fatalf("批量下载异常: finished=%v err=%v", finished, err)
	}
	checkLocalFile(t, filepath.Join(dst, "f1.txt"), "111")
	checkLocalFile(t, filepath.Join(dst, "dir2", "nested.txt"), string(bytes.Repeat([]byte{0x2}, 1000)))
}

func TestBatchTransferFailureAborts(t *testing.T) {
	env := testutil.StartSFTP(t)
	conn := dialTest(t, env)
	defer conn.Close()

	items := []BatchItem{
		{Src: filepath.Join(t.TempDir(), "missing"), Dst: filepath.Join(env.Root, "m1")},
		{Src: filepath.Join(t.TempDir(), "also-missing"), Dst: filepath.Join(env.Root, "m2")},
	}
	tb := NewTransfer("2 项", true)
	BatchTransfer(conn.Client, tb, true, items)
	_, _, finished, err := tb.Snapshot()
	if !finished || err == nil {
		t.Fatalf("批量传输应失败: finished=%v err=%v", finished, err)
	}
}

func checkRemoteFile(t *testing.T, cl *sftp.Client, p, want string) {
	t.Helper()
	f, err := cl.Open(p)
	if err != nil {
		t.Fatalf("远程文件 %s 打开失败: %v", p, err)
	}
	defer f.Close()
	b, err := io.ReadAll(f)
	if err != nil || string(b) != want {
		t.Fatalf("远程文件 %s 校验失败: err=%v", p, err)
	}
}

func checkLocalFile(t *testing.T, p, want string) {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil || string(b) != want {
		t.Fatalf("本地文件 %s 校验失败: err=%v", p, err)
	}
}

func dialTest(t *testing.T, env testutil.SFTPEnv) *Conn {
	t.Helper()
	h, p := testutil.SplitHostPort(env.Addr)
	host := &model.Host{Name: "t", Host: h, Port: p, User: "tester", Auth: model.AuthPassword}
	conn, err := Dial(host, "secret", sshc.WithHostKeyCallback(ssh.InsecureIgnoreHostKey()))
	if err != nil {
		t.Fatalf("连接失败: %v", err)
	}
	return conn
}
