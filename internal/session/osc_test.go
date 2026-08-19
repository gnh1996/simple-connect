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

// TestOSCTracker 验证 OSC 133;cwd 序列解析 + 从输出中剔除（对齐 Tabby）：
// 跨 Write 边界、多路径覆盖、无序列安全；非目标序列原样透传。
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

	// OSC 133;cwd 序列应从输出中剔除，不污染终端
	if buf.String() != "prefixmid" {
		t.Fatalf("OSC 序列应从输出中剔除，实际: %q", buf.String())
	}

	// 非目标序列（CSI 颜色等）原样透传，且不误跟踪
	var buf2 bytes.Buffer
	tr2 := newOSCTracker(&buf2)
	_, _ = tr2.Write([]byte("a\x1b[31mred\x1b[0m\x1b]0;title\x07tail"))
	if buf2.String() != "a\x1b[31mred\x1b[0m\x1b]0;title\x07tail" {
		t.Fatalf("非目标序列应原样透传: %q", buf2.String())
	}
	if tr2.Cwd() != "" {
		t.Fatalf("非 133;cwd 序列不应跟踪: %q", tr2.Cwd())
	}

	// 无序列输出不崩溃、不误报
	tr3 := newOSCTracker(&bytes.Buffer{})
	_, _ = tr3.Write([]byte(strings.Repeat("hello ", 2000))) // 超过保留窗口
	if tr3.Cwd() != "" {
		t.Fatalf("无序列时不应跟踪到路径: %q", tr3.Cwd())
	}

	// 超长输出后仍能识别后续序列（窗口滚动）
	_, _ = tr3.Write([]byte(strings.Repeat("x", 5000)))
	_, _ = tr3.Write([]byte("\x1b]133;cwd=/tmp\x07"))
	if got := tr3.Cwd(); got != "/tmp" {
		t.Fatalf("窗口滚动后应跟踪到 /tmp，实际 %q", got)
	}
}

// TestOSCTrackerCwdAfterCSI 回归：cwd OSC 之前已有其他 ESC 序列（真实 bash
// 括号粘贴模式 \x1b[?2004h/\x1b[?2004l 等），首个 ESC 并非 OSC 起点。
// 必须精确定位 `\x1b]133;cwd=`，而非「首个 ESC 到首个 BEL」——否则整个区间被
// 当作一个 OSC 透传、cwd 未被剔除也未跟踪（历史 bug：detach 时 Cwd() 为空）。
func TestOSCTrackerCwdAfterCSI(t *testing.T) {
	var buf bytes.Buffer
	tr := newOSCTracker(&buf)

	// 模拟真实交互流：提示符重绘（CSI）后紧跟 cwd OSC
	_, _ = tr.Write([]byte("\x1b[?2004l\r\x1b[?2004hroot@h:~# \x1b]133;cwd=/root\x07\x1b[?2004h"))
	if got := tr.Cwd(); got != "/root" {
		t.Fatalf("CSI 后的 cwd OSC 应被跟踪到 /root，实际 %q", got)
	}
	// cwd 序列被剔除，但 CSI 序列保留透传
	if !strings.Contains(buf.String(), "\x1b[?2004l") {
		t.Fatalf("非 cwd 的 CSI 序列应保留透传: %q", buf.String())
	}
	if strings.Contains(buf.String(), "]133;cwd=/root") {
		t.Fatalf("cwd OSC 应从输出剔除: %q", buf.String())
	}

	// cd 到新目录后的下一提示符
	_, _ = tr.Write([]byte("cd /opt\r\n\x1b[?2004l\r\x1b]133;cwd=/opt\x07\x1b[?2004hroot@h:/opt# "))
	if got := tr.Cwd(); got != "/opt" {
		t.Fatalf("应跟踪到最新 /opt，实际 %q", got)
	}
}

// TestOSCTrackerNoDuplicateOnBoundary 回归：跨 Write 边界时，无 ESC 的纯文本
// （如登录横幅）不得在后续 Write 中重复输出。历史 bug：scan 在「无 ESC 序列」
// 分支把全部已透传数据又写回 t.buf，导致下一 Write 重复输出该内容（登录横幅
// 被重复多份）。
func TestOSCTrackerNoDuplicateOnBoundary(t *testing.T) {
	var buf bytes.Buffer
	tr := newOSCTracker(&buf)

	banner := []byte("Linux host 6.1.0-32-amd64\n\nWelcome !\n\nLast login: 2026-08-19 00:00:00 from x.x.x.x\r\n")
	// 横幅分多段到达（真实网络分片）
	for i := 0; i < len(banner); i += 40 {
		end := i + 40
		if end > len(banner) {
			end = len(banner)
		}
		if _, err := tr.Write(banner[i:end]); err != nil {
			t.Fatal(err)
		}
	}
	// 横幅后继续正常输出
	if _, err := tr.Write([]byte("root@h:~# echo HI\r\nHI\r\n")); err != nil {
		t.Fatal(err)
	}

	// 横幅必须恰好输出一次，不因跨 Write 边界而重复
	if got := bytes.Count(buf.Bytes(), []byte("Linux host")); got != 1 {
		t.Fatalf("纯文本不应跨 Write 重复输出，出现 %d 次（应 1）：%q", got, buf.String())
	}
	// 横幅字节顺序与完整性
	if !bytes.HasPrefix(buf.Bytes(), banner) {
		t.Fatalf("横幅应原样透传且开头出现：%q", buf.String())
	}
}

// TestOSCTrackerQuiet 验证静音模式：丢弃输出但继续消费（不阻塞远端）、
// 仍扫描 OSC 序列跟踪 cwd；恢复后输出恢复。
func TestOSCTrackerQuiet(t *testing.T) {
	var buf bytes.Buffer
	tr := newOSCTracker(&buf)
	if _, err := tr.Write([]byte("before")); err != nil {
		t.Fatal(err)
	}
	tr.setQuiet(true)
	seq := []byte("during\x1b]133;cwd=/tmp\x07")
	if n, err := tr.Write(seq); n != len(seq) || err != nil {
		t.Fatalf("静音期间应消费全部数据: n=%d err=%v", n, err)
	}
	tr.setQuiet(false)
	if _, err := tr.Write([]byte("after")); err != nil {
		t.Fatal(err)
	}
	if buf.String() != "beforeafter" {
		t.Fatalf("静音期间输出应被丢弃: %q", buf.String())
	}
	if got := tr.Cwd(); got != "/tmp" {
		t.Fatalf("静音期间仍应跟踪 cwd: %q", got)
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

	// 远程回显 OSC 标记（模拟 PROMPT_COMMAND 输出）；OSC 序列已被 tracker 过滤，
	// 用同步标记确认数据已到达 tracker（此时 Cwd 已更新）。
	osc := "\x1b]133;cwd=/var/log\x07"
	if _, err := inW.Write([]byte("syncmark" + osc)); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if out.Contains("syncmark") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !out.Contains("syncmark") {
		t.Fatalf("同步标记未回显，输出: %q", out.String())
	}
	if out.Contains("/var/log") {
		t.Fatalf("OSC 133;cwd 序列应从输出中剔除，实际: %q", out.String())
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

// TestSessionEnvPromptCommandRejected 验证 PROMPT_COMMAND env 请求被服务器拒绝
// （对齐真实 OpenSSH 默认 AcceptEnv 仅接受 LANG/LC_*，cwd 钩子改为 shell 内注入）。
func TestSessionEnvPromptCommandRejected(t *testing.T) {
	t.Setenv("LANG", "en_US.UTF-8")
	env := testutil.StartShell(t)
	cl := dialShell(t, env)
	defer cl.Close()

	inR, inW := io.Pipe()
	out := &syncBuf{}
	done := make(chan cwdErr, 1)
	go func() { done <- runSessionCwd(cl, inR, out, out, 24, 80) }()

	// 轮询等待服务器收到 LANG（env 处理正常）
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if got := env.Env(); got["LANG"] == "en_US.UTF-8" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	got := env.Env()
	if _, ok := got["PROMPT_COMMAND"]; ok {
		t.Fatalf("真实 OpenSSH 应拒绝 PROMPT_COMMAND env 请求，实际收到: %v", got)
	}

	_ = inW.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("会话未结束")
	}
}

// TestSessionCwdHookInjected 验证首次进入会话时向远程 shell 注入 cwd 钩子命令
// （绕开 PROMPT_COMMAND env 被拒绝的限制），且同时包含 bash / zsh / tmux passthrough
// / 清除序列各分支。
func TestSessionCwdHookInjected(t *testing.T) {
	env := testutil.StartShell(t)
	cl := dialShell(t, env)
	defer cl.Close()

	inR, inW := io.Pipe()
	out := &syncBuf{}
	h, err := newTestHandle(cl, inR, out, 24, 80)
	if err != nil {
		t.Fatalf("建立会话失败: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- h.Resume() }()

	// 注入命令经 stdin 发送（pty ECHO=0 保证不回显；测试服务器 shell 原样回显），
	// 应包含 bash/zsh/tmux passthrough/恢复 echo 各分支，且无清屏/清行控制序列。
	waitContains(t, out, "_sc_cwd(){")
	for _, frag := range []string{
		"_sc_cwd(){",                      // bash 函数定义
		"precmd_functions+=(_sc_cwd)",     // zsh precmd 追加
		`\ePtmux;\e\e]133;cwd=%s\007\e\\`, // tmux passthrough
		"; stty echo",                     // 恢复 ECHO
	} {
		if !out.Contains(frag) {
			t.Fatalf("注入命令应包含 %q，实际: %q", frag, out.String())
		}
	}
	if out.Contains("2J") || out.Contains("1A") {
		t.Fatalf("注入命令不应包含清屏/清行控制序列（干扰 readline），实际: %q", out.String())
	}

	// detach 挂起（连接保持）
	if _, err := inW.Write([]byte{0x18, 'f'}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, ErrDetach) {
			t.Fatalf("应 detach，实际 %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("detach 未在期限内返回")
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
