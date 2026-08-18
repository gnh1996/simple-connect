package session

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	sshc "simple-connect/internal/ssh"
	"simple-connect/internal/testutil"
)

// TestOSCTracker 验证 OSC 133;cwd 序列解析：跨 Write 边界、多路径覆盖、无序列安全
func TestOSCTracker(t *testing.T) {
	var buf bytes.Buffer
	tr := newOSCTracker(&buf)

	// 多路径覆盖 + 跨边界拆分
	seq1 := "\x1b]133;cwd=/var/log\x07"
	seq2 := "\x1b]133;cwd=/opt/app/config\x07"
	_, _ = tr.Write([]byte("prefix" + seq1[:5])) // 序列被拆开
	_, _ = tr.Write([]byte(seq1[5:] + "mid" + seq2[:3]))
	_, _ = tr.Write([]byte(seq2[3:]))

	if got := tr.Cwd(); got != "/opt/app/config" {
		t.Fatalf("应跟踪到最新路径 /opt/app/config，实际 %q", got)
	}

	// 数据原样透传
	if buf.String() != "prefix"+seq1+"mid"+seq2 {
		t.Fatalf("输出未原样透传: %q", buf.String())
	}

	// 无序列输出不崩溃、不误报
	tr2 := newOSCTracker(&bytes.Buffer{})
	_, _ = tr2.Write([]byte(strings.Repeat("hello ", 2000))) // 超过保留窗口
	if tr2.Cwd() != "" {
		t.Fatalf("无序列时不应跟踪到路径: %q", tr2.Cwd())
	}

	// 超长输出后仍能识别后续序列（窗口滚动）
	_, _ = tr2.Write([]byte(strings.Repeat("x", 5000)))
	_, _ = tr2.Write([]byte("\x1b]133;cwd=/tmp\x07"))
	if got := tr2.Cwd(); got != "/tmp" {
		t.Fatalf("窗口滚动后应跟踪到 /tmp，实际 %q", got)
	}
}

// TestSessionDetachReturnsCwd 验证 detach 时返回会话内跟踪到的远程目录
func TestSessionDetachReturnsCwd(t *testing.T) {
	env := testutil.StartShell(t)
	cl := dialShell(t, env)
	defer cl.Close()

	inR, inW := io.Pipe()
	out := &syncBuf{}
	done := make(chan cwdErr, 1)
	go func() { done <- runSessionCwd(cl, inR, out, out, 24, 80) }()

	// 远程回显 OSC 标记（模拟 PROMPT_COMMAND 输出）
	osc := "\x1b]133;cwd=/var/log\x07"
	if _, err := inW.Write([]byte(osc)); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if out.Contains("/var/log") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !out.Contains("/var/log") {
		t.Fatalf("OSC 标记未回显，输出: %q", out.String())
	}

	// Ctrl+X f detach
	if _, err := inW.Write([]byte{0x18, 'f'}); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-done:
		if !errors.Is(r.err, ErrDetach) {
			t.Fatalf("应返回 ErrDetach，实际 %v", r.err)
		}
		if r.cwd != "/var/log" {
			t.Fatalf("detach 应携带跟踪到的 cwd /var/log，实际 %q", r.cwd)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("detach 未在期限内返回")
	}
}

// TestSessionEnvPromptCommand 验证 PROMPT_COMMAND 钩子被注入（bash cwd 追踪）
func TestSessionEnvPromptCommand(t *testing.T) {
	t.Setenv("LANG", "en_US.UTF-8")
	env := testutil.StartShell(t)
	cl := dialShell(t, env)
	defer cl.Close()

	inR, inW := io.Pipe()
	out := &syncBuf{}
	done := make(chan cwdErr, 1)
	go func() { done <- runSessionCwd(cl, inR, out, out, 24, 80) }()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		got := env.Env()
		if _, ok := got["PROMPT_COMMAND"]; ok {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	got := env.Env()
	if got["PROMPT_COMMAND"] != oscPromptCommand {
		t.Fatalf("PROMPT_COMMAND 注入值不符: %q", got["PROMPT_COMMAND"])
	}

	_ = inW.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("会话未结束")
	}
}

// cwdErr runSession 双返回值（cwd, err）
type cwdErr struct {
	cwd string
	err error
}

func runSessionCwd(cl *sshc.Client, in io.Reader, out, errOut io.Writer, rows, cols int) cwdErr {
	cwd, err := runSession(cl, in, out, errOut, rows, cols)
	return cwdErr{cwd: cwd, err: err}
}

var _ = ssh.InsecureIgnoreHostKey