package sshc

import (
	"testing"

	"simple-connect/internal/model"
)

// TestPasswordAuthMethodsOnlyForPasswordAuth 回归：私钥认证主机即使残留了
// 历史密码，也不能回落到密码/keyboard-interactive 认证。
func TestPasswordAuthMethodsOnlyForPasswordAuth(t *testing.T) {
	pw := &model.Host{Auth: model.AuthPassword}
	if got := len(passwordAuthMethods(pw, "secret")); got != 2 {
		t.Fatalf("密码认证应生成 password + keyboard-interactive，实际 %d", got)
	}
	if got := len(passwordAuthMethods(pw, "")); got != 0 {
		t.Fatalf("空密码不应生成认证方法，实际 %d", got)
	}

	key := &model.Host{Auth: model.AuthKey, KeyPath: "~/.ssh/id_ed25519"}
	if got := len(passwordAuthMethods(key, "legacy-secret")); got != 0 {
		t.Fatalf("私钥认证不应使用历史密码，实际生成 %d 个方法", got)
	}
}
