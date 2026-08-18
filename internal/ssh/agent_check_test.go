package sshc

import (
	"os"
	"path/filepath"
	"testing"
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
