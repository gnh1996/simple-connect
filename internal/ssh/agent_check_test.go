package sshc

import (
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// 模拟无 agent 环境：将 SSH_AUTH_SOCK 指向无效路径
func noAgent(t *testing.T) {
	t.Helper()
	old, had := os.LookupEnv("SSH_AUTH_SOCK")
	_ = os.Setenv("SSH_AUTH_SOCK", filepath.Join(t.TempDir(), "no-agent.sock"))
	t.Cleanup(func() {
		if had {
			_ = os.Setenv("SSH_AUTH_SOCK", old)
		} else {
			_ = os.Unsetenv("SSH_AUTH_SOCK")
		}
	})
}

func TestAgentUnavailable(t *testing.T) {
	noAgent(t)
	if AgentAvailable() {
		t.Fatal("无效 socket 下应判定 agent 不可用")
	}
	if hint := AgentHint(); hint == "" {
		t.Fatal("agent 不可用时应返回引导文案")
	}
	if fps := AgentFingerprints(); fps != nil {
		t.Fatalf("agent 不可用时指纹应为空，实际 %v", fps)
	}
	if in, known := KeyInAgent("/tmp/nonexistent-key"); in || known {
		t.Fatalf("agent 不可用时 KeyInAgent 应为 (false,false)，实际 (%v,%v)", in, known)
	}
}

func TestKeyInAgentWithoutAgent(t *testing.T) {
	noAgent(t)
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "id_ed25519")
	if err := os.WriteFile(keyPath, []byte("not a key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if in, known := KeyInAgent(keyPath); in || known {
		t.Fatalf("agent 不可用时 KeyInAgent 应为 (false,false)，实际 (%v,%v)", in, known)
	}
}

// TestAgentSignerCleanupClosesConn 回归：agentSigner 必须返回可关闭连接的 cleanup，
// 且调用后连接真正关闭（旧实现从不关闭，每次连接泄漏一个 agent socket）。
func TestAgentSignerCleanupClosesConn(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix socket 仅在类 Unix 平台可用")
	}
	sock := filepath.Join(t.TempDir(), "agent.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("监听测试 agent socket 失败: %v", err)
	}
	defer ln.Close()

	accepted := make(chan struct{})
	eof := make(chan struct{})
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		close(accepted)
		// agent 协议由客户端按需发起；此处仅等待对端关闭（Read 返回 EOF）。
		buf := make([]byte, 1)
		_, _ = c.Read(buf)
		_ = c.Close()
		close(eof)
	}()
	t.Setenv("SSH_AUTH_SOCK", sock)

	_, cleanup, err := agentSigner()
	if err != nil {
		t.Fatalf("agentSigner 失败: %v", err)
	}
	if cleanup == nil {
		t.Fatal("agentSigner 应返回非 nil cleanup")
	}
	select {
	case <-accepted:
	case <-time.After(2 * time.Second):
		t.Fatal("agentSigner 未连接到测试 agent")
	}
	cleanup()
	select {
	case <-eof:
	case <-time.After(2 * time.Second):
		t.Fatal("cleanup 未关闭 agent 连接（fd 泄漏）")
	}
}
