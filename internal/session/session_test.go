package session

import (
	"bytes"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"simple-connect/internal/model"
	sshc "simple-connect/internal/ssh"
	"simple-connect/internal/testutil"
)

// syncBuf 并发安全的字节缓冲（Session 后台写 + 测试轮询读）
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func (s *syncBuf) Contains(sub string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return bytes.Contains(s.b.Bytes(), []byte(sub))
}

func dialShell(t *testing.T, env *testutil.ShellEnv) *sshc.Client {
	t.Helper()
	h, p := testutil.SplitHostPort(env.Addr)
	host := &model.Host{Name: "t", Host: h, Port: p, User: env.User, Auth: model.AuthPassword}
	cl, err := sshc.ConnectRaw(host, env.Pass,
		sshc.WithHostKeyCallback(ssh.InsecureIgnoreHostKey()))
	if err != nil {
		t.Fatalf("连接失败（含 keyboard-interactive 认证）: %v", err)
	}
	return cl
}

func TestSessionEcho(t *testing.T) {
	env := testutil.StartShell(t)
	cl := dialShell(t, env)
	defer cl.Close()

	inR, inW := io.Pipe()
	out := &syncBuf{}

	done := make(chan error, 1)
	go func() {
		done <- runSession(cl, inR, out, out, 24, 80)
	}()

	// 写入并等待回显
	if _, err := inW.Write([]byte("hello\n")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if out.Contains("hello") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !out.Contains("hello") {
		t.Fatalf("未收到回显，输出: %q", out.String())
	}

	// 关闭输入 → EOF → 会话正常结束
	_ = inW.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("会话应正常结束，实际错误: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("会话未在期限内结束")
	}
}

func TestSessionWindowSize(t *testing.T) {
	env := testutil.StartShell(t)
	cl := dialShell(t, env)
	defer cl.Close()

	inR, inW := io.Pipe()
	out := &syncBuf{}
	done := make(chan error, 1)
	go func() { done <- runSession(cl, inR, out, out, 30, 100) }()

	// 等待服务器记录到窗口尺寸
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		r, c := env.WindowSize()
		if r == 30 && c == 100 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	r, c := env.WindowSize()
	if r != 30 || c != 100 {
		t.Fatalf("服务器应记录 30x100，实际 %dx%d", r, c)
	}

	_ = inW.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("会话未结束")
	}
}

func TestSessionAuthFailure(t *testing.T) {
	env := testutil.StartShell(t)
	h, p := testutil.SplitHostPort(env.Addr)
	host := &model.Host{Name: "t", Host: h, Port: p, User: env.User, Auth: model.AuthPassword}
	if _, err := sshc.ConnectRaw(host, "wrong-password",
		sshc.WithHostKeyCallback(ssh.InsecureIgnoreHostKey())); err == nil {
		t.Fatal("错误密码应认证失败")
	}
}

var _ = errors.New
