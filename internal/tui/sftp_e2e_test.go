package tui

import (
	"os"
	"path"
	"path/filepath"
	"testing"

	tea "charm.land/bubbletea/v2"
	"golang.org/x/crypto/ssh"

	"simple-connect/internal/model"
	"simple-connect/internal/testutil"
)

// testHost 从测试环境构造主机
func testHost(env testutil.SFTPEnv) *model.Host {
	h, p := testutil.SplitHostPort(env.Addr)
	return &model.Host{
		Name: "测试机", Host: h, Port: p,
		User: "tester", Auth: model.AuthPassword,
	}
}

// newTestSFTPModel 创建注入忽略主机指纹的 SFTP 模型
func newTestSFTPModel(t *testing.T, env testutil.SFTPEnv) *sftpModel {
	t.Helper()
	s := testStore(t)
	h := testHost(env)
	_ = s.Add(h)
	_ = s.SetPassword(h, "secret")

	m := newSFTPModel(s, h)
	m.hostKeyCallback = ssh.InsecureIgnoreHostKey()
	m.localCwd = t.TempDir()
	return m
}

// connect 连接测试服务器
func connect(t *testing.T, m *sftpModel) {
	t.Helper()
	msg := m.Init()()
	if cm, ok := msg.(sftpConnMsg); ok {
		if cm.err != nil {
			t.Fatalf("连接失败: %v", cm.err)
		}
	} else {
		t.Fatalf("连接命令应返回 sftpConnMsg，实际 %T", msg)
	}
	m, _ = m.Update(msg)
}

// driveProgress 循环执行进度命令直到传输完成
func driveProgress(t *testing.T, m *sftpModel, cmd tea.Cmd) *sftpModel {
	t.Helper()
	for i := 0; i < 500 && m.transfer != nil; i++ {
		pm := cmd()
		if _, ok := pm.(sftpProgressMsg); !ok {
			t.Fatalf("进度命令应返回 sftpProgressMsg，实际 %T", pm)
		}
		next, c := m.handleProgress()
		m, cmd = next, c
	}
	if m.transfer != nil {
		t.Fatal("传输未在期限内完成")
	}
	return m
}

func TestSFTPConnectAndList(t *testing.T) {
	env := testutil.StartSFTP(t)
	m := newTestSFTPModel(t, env)
	connect(t, m)

	// 预置远程文件
	if err := os.WriteFile(filepath.Join(env.Root, "hello.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.MkdirAll(filepath.Join(env.Root, "sub"), 0o755)

	// 刷新目录
	m.cwd = env.Root
	m.entries = nil
	lm := m.loadList()
	m, _ = m.Update(lm())
	if m.err != "" {
		t.Fatalf("列目录失败: %s", m.err)
	}
	if len(m.entries) != 2 {
		t.Fatalf("应列出 2 项，实际 %d", len(m.entries))
	}
	if !m.entries[0].IsDir() || m.entries[0].Name() != "sub" {
		t.Fatalf("目录应排在最前: %v", m.entries[0].Name())
	}
	m.close()
}

func TestSFTPUploadDownloadDelete(t *testing.T) {
	env := testutil.StartSFTP(t)
	m := newTestSFTPModel(t, env)
	connect(t, m)
	m.cwd = env.Root

	// 本地源文件
	src := filepath.Join(m.localCwd, "src.bin")
	content := make([]byte, 8192)
	for i := range content {
		content[i] = byte(i % 251)
	}
	if err := os.WriteFile(src, content, 0o644); err != nil {
		t.Fatal(err)
	}

	// 上传
	up := m.startUpload(src)
	m = driveProgress(t, m, up)
	if m.err != "" {
		t.Fatalf("上传失败: %s", m.err)
	}
	// 校验远程文件
	if b, err := os.ReadFile(filepath.Join(env.Root, "src.bin")); err != nil || string(b) != string(content) {
		t.Fatalf("远程文件校验失败: %v", err)
	}

	// 下载
	dst := filepath.Join(m.localCwd, "dst.bin")
	dl := m.startTransfer(path.Join(env.Root, "src.bin"), dst, false)
	m = driveProgress(t, m, dl)
	if m.err != "" {
		t.Fatalf("下载失败: %s", m.err)
	}
	if b, err := os.ReadFile(dst); err != nil || string(b) != string(content) {
		t.Fatalf("下载内容校验失败: %v", err)
	}

	// 删除远程文件
	m.entries = nil
	lm := m.loadList()
	m, _ = m.Update(lm())
	m.confirmID = 0
	m, dc := m.doDelete()
	if dc != nil {
		_ = dc()
	}
	if _, err := os.Stat(filepath.Join(env.Root, "src.bin")); !os.IsNotExist(err) {
		t.Fatal("远程文件应已删除")
	}
	m.close()
}

func TestSFTPMkdirAndNavigate(t *testing.T) {
	env := testutil.StartSFTP(t)
	m := newTestSFTPModel(t, env)
	connect(t, m)
	m.cwd = env.Root

	// 新建目录
	mk := m.mkdir("data")
	m, _ = m.Update(mk())
	if m.err != "" {
		t.Fatalf("新建目录失败: %s", m.err)
	}
	// 进入新目录
	m.entries = nil
	lm := m.loadList()
	m, _ = m.Update(lm())
	found := false
	for _, e := range m.entries {
		if e.Name() == "data" && e.IsDir() {
			found = true
		}
	}
	if !found {
		t.Fatal("未找到新建目录 data")
	}
	m.cursor = indexOfDir(m.entries, "data")
	m, _ = m.enterCurrent()
	if m.cwd != path.Join(env.Root, "data") {
		t.Fatalf("应进入 %s，实际 %s", path.Join(env.Root, "data"), m.cwd)
	}
	// 返回上级
	m, _ = m.goUp()
	if m.cwd != env.Root {
		t.Fatalf("应返回 %s，实际 %s", env.Root, m.cwd)
	}
	m.close()
}

func indexOfDir(entries []os.FileInfo, name string) int {
	for i, e := range entries {
		if e.Name() == name {
			return i
		}
	}
	return 0
}
