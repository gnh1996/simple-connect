package session

import (
	"bytes"
	"errors"
	"io"
	"strings"
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

// waitContains 轮询等待输出包含子串
func waitContains(t *testing.T, buf *syncBuf, sub string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if buf.Contains(sub) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("等待 %q 超时，输出: %q", sub, buf.String())
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
		done <- func() error { _, err := runSession(cl, inR, out, out, 24, 80); return err }()
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
	go func() { _, err := runSession(cl, inR, out, out, 30, 100); done <- err }()

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

// TestSessionDetach 验证 Ctrl+X f 中断会话并返回 ErrDetach，且热键字节不转发远程
func TestSessionDetach(t *testing.T) {
	env := testutil.StartShell(t)
	cl := dialShell(t, env)
	defer cl.Close()

	inR, inW := io.Pipe()
	out := &syncBuf{}
	done := make(chan error, 1)
	go func() {
		done <- func() error { _, err := runSession(cl, inR, out, out, 24, 80); return err }()
	}()

	// 先写入普通内容，验证透传
	if _, err := inW.Write([]byte("abc\n")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if out.Contains("abc") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !out.Contains("abc") {
		t.Fatalf("普通输入未透传，输出: %q", out.String())
	}

	// Ctrl+X（跨 Read 边界）再按 f → 应 detach
	if _, err := inW.Write([]byte{0x18}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if _, err := inW.Write([]byte{'f'}); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-done:
		if !errors.Is(err, ErrDetach) {
			t.Fatalf("应返回 ErrDetach，实际 %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("detach 未在期限内返回")
	}
	// 热键字节不应透传
	if out.Contains(string([]byte{0x18, 'f'})) {
		t.Fatalf("热键序列不应转发远程，输出: %q", out.String())
	}
}

// TestDetachBoundary 验证 \x18x 不构成热键时原样转发
func TestDetachBoundary(t *testing.T) {
	env := testutil.StartShell(t)
	cl := dialShell(t, env)
	defer cl.Close()

	inR, inW := io.Pipe()
	out := &syncBuf{}
	done := make(chan error, 1)
	go func() {
		done <- func() error { _, err := runSession(cl, inR, out, out, 24, 80); return err }()
	}()

	// \x18 后跟 x（非 f）→ 应转发，不触发 detach
	if _, err := inW.Write([]byte{0x18, 'x'}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if out.Contains(string([]byte{0x18, 'x'})) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !out.Contains(string([]byte{0x18, 'x'})) {
		t.Fatalf("\\x18x 应被转发，输出: %q", out.String())
	}

	// 关闭输入 → 会话正常结束（非 detach）
	_ = inW.Close()
	select {
	case err := <-done:
		if errors.Is(err, ErrDetach) {
			t.Fatal("不应 detach")
		}
		if err != nil {
			t.Fatalf("会话应正常结束，实际 %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("会话未在期限内结束")
	}
}

// TestResolveTermSize 验证 term.GetSize 的 (width, height) 与 PTY 请求的 (rows, cols)
// 换算顺序。曾把 width/height 直接当 rows/cols 使用，导致远程 PTY 行列颠倒：
// 终端 30 行 x 120 列被请求为 120 行 x 30 列，远程按 30 列换行，输入/退格光标全乱。
func TestResolveTermSize(t *testing.T) {
	cases := []struct {
		name   string
		width  int
		height int
	}{
		{"常规宽屏", 120, 30},
		{"正方形", 80, 80},
		{"高窄屏", 60, 100},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rows, cols := resolveTermSize(c.width, c.height)
			if rows != c.height || cols != c.width {
				t.Fatalf("resolveTermSize(%d, %d) = (%d, %d)，应为 (行=%d, 列=%d)",
					c.width, c.height, rows, cols, c.height, c.width)
			}
		})
	}

	t.Run("非法尺寸回退 24x80", func(t *testing.T) {
		if rows, cols := resolveTermSize(0, 0); rows != 24 || cols != 80 {
			t.Fatalf("尺寸非法应回退 24x80，实际 %dx%d", rows, cols)
		}
		if rows, cols := resolveTermSize(-1, 50); rows != 24 || cols != 80 {
			t.Fatalf("宽度非法应回退 24x80，实际 %dx%d", rows, cols)
		}
		if rows, cols := resolveTermSize(120, 0); rows != 24 || cols != 80 {
			t.Fatalf("高度非法应回退 24x80，实际 %dx%d", rows, cols)
		}
	})
}

var _ = errors.New

// TestSessionEnvForward 验证 locale 环境变量被透传到远程（防止 C locale 导致
// readline 重绘光标错位/跳行首、CJK 对齐错乱）。
func TestSessionEnvForward(t *testing.T) {
	t.Setenv("LANG", "zh_CN.UTF-8")
	t.Setenv("LC_CTYPE", "zh_CN.UTF-8")
	t.Setenv("LC_ALL", "") // 空值不透传
	t.Setenv("TERM", "")   // TERM 不在透传列表，不应发送

	env := testutil.StartShell(t)
	cl := dialShell(t, env)
	defer cl.Close()

	inR, inW := io.Pipe()
	out := &syncBuf{}
	done := make(chan error, 1)
	go func() { done <- func() error { _, err := runSession(cl, inR, out, out, 24, 80); return err }() }()

	// 轮询等待服务器收到 env 请求（env 在 Shell() 前发送）
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if got := env.Env(); got["LANG"] == "zh_CN.UTF-8" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	got := env.Env()
	if got["LANG"] != "zh_CN.UTF-8" {
		t.Fatalf("LANG 未透传，收到: %v", got)
	}
	if got["LC_CTYPE"] != "zh_CN.UTF-8" {
		t.Fatalf("LC_CTYPE 未透传，收到: %v", got)
	}
	if _, ok := got["LC_ALL"]; ok {
		t.Fatalf("空值变量不应透传: %v", got)
	}
	if _, ok := got["TERM"]; ok {
		t.Fatalf("非透传列表变量不应发送: %v", got)
	}

	_ = inW.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("会话未结束")
	}
}

// TestSessionPtyIUTF8 验证 pty-req 发送 IUTF8 等关键 tty 模式
// （缺失时远程退格按字节删除、光标错位）。
func TestSessionPtyIUTF8(t *testing.T) {
	env := testutil.StartShell(t)
	cl := dialShell(t, env)
	defer cl.Close()

	inR, inW := io.Pipe()
	out := &syncBuf{}
	done := make(chan error, 1)
	go func() { done <- func() error { _, err := runSession(cl, inR, out, out, 24, 80); return err }() }()

	// 轮询等待服务器记录到 pty 模式
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if modes := env.PtyModes(); len(modes) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	modes := env.PtyModes()
	if modes[ssh.IUTF8] != 1 {
		t.Fatalf("IUTF8 模式应发送且为 1，实际: %v", modes)
	}
	if modes[ssh.IMAXBEL] != 1 {
		t.Fatalf("IMAXBEL 模式应发送且为 1，实际: %v", modes)
	}
	if modes[ssh.IXOFF] != 0 {
		t.Fatalf("IXOFF 应显式发送 0（防止服务器默认开启流控卡输入），实际: %v", modes)
	}
	// 关键基础模式仍在（ECHO=0 为 cwd 钩子注入专用，注入末尾 stty echo 恢复）
	if modes[ssh.ECHO] != 0 || modes[ssh.ICRNL] != 1 || modes[ssh.CS8] != 1 {
		t.Fatalf("基础模式缺失: %v", modes)
	}

	_ = inW.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("会话未结束")
	}
}

// TestSessionDetachKeepsConnection 验证 detach 只挂起透传、不关闭 SSH 连接：
// 挂起后同一连接仍可再建会话（不重连）。
func TestSessionDetachKeepsConnection(t *testing.T) {
	env := testutil.StartShell(t)
	cl := dialShell(t, env)
	defer cl.Close()

	inR, inW := io.Pipe()
	out := &syncBuf{}
	done := make(chan error, 1)
	go func() {
		done <- func() error { _, err := runSession(cl, inR, out, out, 24, 80); return err }()
	}()

	if _, err := inW.Write([]byte("before\n")); err != nil {
		t.Fatal(err)
	}
	waitContains(t, out, "before")

	// Ctrl+X f 挂起（不关闭连接）
	if _, err := inW.Write([]byte{0x18, 'f'}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, ErrDetach) {
			t.Fatalf("应返回 ErrDetach，实际 %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("detach 未在期限内返回")
	}

	// 连接应保持存活：同一连接再开 shell 会话并回显
	s2, err := cl.NewSession()
	if err != nil {
		t.Fatalf("detach 后连接应可再建会话: %v", err)
	}
	defer s2.Close()
	var out2 syncBuf
	s2.Stdout = &out2
	s2.Stdin = strings.NewReader("alive\n")
	if err := s2.Shell(); err != nil {
		t.Fatalf("detach 后同一连接启动 shell 失败: %v", err)
	}
	waitContains(t, &out2, "alive")
}

// TestSessionResume 验证 detach 挂起后可用同一会话恢复透传（不重连）：
// 挂起 → 恢复继续回显 → 输入 EOF 正常结束。
func TestSessionResume(t *testing.T) {
	env := testutil.StartShell(t)
	cl := dialShell(t, env)
	defer cl.Close()

	inR, inW := io.Pipe()
	out := &syncBuf{}
	h, err := newTestHandle(cl, inR, out, 24, 80)
	if err != nil {
		t.Fatalf("建立会话失败: %v", err)
	}

	// 首次透传：回显后挂起
	done := make(chan error, 1)
	go func() { done <- h.Resume() }()
	if _, err := inW.Write([]byte("hello\n")); err != nil {
		t.Fatal(err)
	}
	waitContains(t, out, "hello")
	if _, err := inW.Write([]byte{0x18, 'f'}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, ErrDetach) {
			t.Fatalf("首次应 detach，实际 %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("detach 未在期限内返回")
	}

	// 连接保持：恢复同一会话（目录/进程不变），继续回显
	done2 := make(chan error, 1)
	go func() { done2 <- h.Resume() }()
	if _, err := inW.Write([]byte("again\n")); err != nil {
		t.Fatal(err)
	}
	waitContains(t, out, "again")

	// 关闭输入 → 远程 EOF → 会话正常结束
	_ = inW.Close()
	select {
	case err := <-done2:
		if err != nil {
			t.Fatalf("恢复后会话应正常结束，实际 %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("恢复后会话未结束")
	}
}
