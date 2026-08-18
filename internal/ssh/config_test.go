package sshc

import (
	"testing"

	"github.com/kevinburke/ssh_config"

	"simple-connect/internal/model"
)

func TestParseProxyJump(t *testing.T) {
	jumps := parseProxyJump("u1@h1:2222, u2@h2")
	if len(jumps) != 2 {
		t.Fatalf("应解析 2 个跳板，实际 %d", len(jumps))
	}
	j0 := jumps[0]
	if j0.User != "u1" || j0.Host != "h1" || j0.Port != 2222 {
		t.Fatalf("跳板1 解析异常: %+v", j0)
	}
	j1 := jumps[1]
	if j1.User != "u2" || j1.Host != "h2" || j1.Port != 0 {
		t.Fatalf("跳板2 解析异常: %+v", j1)
	}
}

func TestParseProxyJumpNoUser(t *testing.T) {
	jumps := parseProxyJump("bastion")
	if len(jumps) != 1 {
		t.Fatalf("应解析 1 个跳板，实际 %d", len(jumps))
	}
	if jumps[0].User != "" || jumps[0].Host != "bastion" {
		t.Fatalf("无用户名跳板解析异常: %+v", jumps[0])
	}
}

func TestResolveSSHConfig(t *testing.T) {
	getter := getterFunc(func(alias, key string) string {
		if alias != "web" {
			return ""
		}
		switch key {
		case "HostName":
			return "10.0.0.9"
		case "User":
			return "deploy"
		case "Port":
			return "2200"
		case "IdentityFile":
			return "~/.ssh/web_id"
		case "ProxyJump":
			return "jump@bastion:2222"
		}
		return ""
	})

	h := &model.Host{Name: "web", Host: "web", User: "root", Port: 22, Auth: model.AuthPassword}
	resolved, jumps := resolveSSHConfig(h, getter)

	if resolved.Host != "10.0.0.9" {
		t.Fatalf("HostName 应解析为 10.0.0.9，实际 %s", resolved.Host)
	}
	if resolved.User != "deploy" {
		t.Fatalf("User 应解析为 deploy，实际 %s", resolved.User)
	}
	if resolved.Port != 2200 {
		t.Fatalf("Port 应解析为 2200，实际 %d", resolved.Port)
	}
	if resolved.KeyPath != ExpandPath("~/.ssh/web_id") || resolved.Auth != model.AuthKey {
		t.Fatalf("IdentityFile 应被应用: %+v", resolved)
	}
	if len(jumps) != 1 || jumps[0].Host != "bastion" || jumps[0].User != "jump" {
		t.Fatalf("ProxyJump 应解析为 1 个跳板: %+v", jumps)
	}
	// 原主机不应被修改
	if h.Host != "web" || h.User != "root" || h.Port != 22 {
		t.Fatalf("原主机被修改: %+v", h)
	}
}

func TestResolveSSHConfigNoMatch(t *testing.T) {
	getter := getterFunc(func(alias, key string) string { return "" })
	h := &model.Host{Name: "x", Host: "10.0.0.1", User: "root", Port: 22, Auth: model.AuthPassword}
	resolved, jumps := resolveSSHConfig(h, getter)
	if resolved.Host != "10.0.0.1" || resolved.User != "root" || resolved.Port != 22 {
		t.Fatalf("无匹配时不应改动: %+v", resolved)
	}
	if len(jumps) != 0 {
		t.Fatalf("无匹配时不应有跳板: %+v", jumps)
	}
}

// TestResolveSSHConfigNoConfigMatch 回归：主机不在 config 中时，
// 不得被默认值污染（Port 保持自定义端口、认证方式不被改成 key）
func TestResolveSSHConfigNoConfigMatch(t *testing.T) {
	cfg, err := ssh_config.DecodeBytes([]byte("Host aliyun\n  User root\n  Port 22\n  IdentityFile ~/.ssh/id_rsa\n"))
	if err != nil {
		t.Fatal(err)
	}
	h := &model.Host{Name: "t", Host: "127.0.0.1", Port: 46551, User: "tester", Auth: model.AuthPassword}
	resolved, jumps := resolveSSHConfig(h, &sshConfigGetter{cfg: cfg})
	if resolved.Port != 46551 {
		t.Fatalf("无匹配主机的端口不应被默认值覆盖: %d", resolved.Port)
	}
	if resolved.Auth != model.AuthPassword || resolved.KeyPath != "" {
		t.Fatalf("无匹配主机的认证方式不应被改为 key: %+v", resolved)
	}
	if len(jumps) != 0 {
		t.Fatalf("不应有跳板: %+v", jumps)
	}
}

// TestSSHConfigGetterMatch 匹配到 config 块时正确返回明确设置的值
func TestSSHConfigGetterMatch(t *testing.T) {
	cfg, err := ssh_config.DecodeBytes([]byte("Host myserver\n  HostName 10.0.0.9\n  User deploy\n  Port 2200\n  IdentityFile ~/.ssh/my_id\n  ProxyJump jump@bastion\n"))
	if err != nil {
		t.Fatal(err)
	}
	g := &sshConfigGetter{cfg: cfg}
	h := &model.Host{Name: "myserver", Host: "myserver", User: "root", Port: 22, Auth: model.AuthPassword}
	resolved, jumps := resolveSSHConfig(h, g)
	if resolved.Host != "10.0.0.9" || resolved.User != "deploy" || resolved.Port != 2200 {
		t.Fatalf("匹配时应用 config 失败: %+v", resolved)
	}
	if resolved.Auth != model.AuthKey || resolved.KeyPath != ExpandPath("~/.ssh/my_id") {
		t.Fatalf("IdentityFile 未应用: %+v", resolved)
	}
	if len(jumps) != 1 || jumps[0].User != "jump" || jumps[0].Host != "bastion" {
		t.Fatalf("ProxyJump 未解析: %+v", jumps)
	}
}
