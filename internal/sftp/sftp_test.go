package sftp

import (
	"os"
	"path/filepath"
	"testing"

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
