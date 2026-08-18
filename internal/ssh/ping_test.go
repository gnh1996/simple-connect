package sshc

import (
	"testing"

	"simple-connect/internal/model"
)

func TestExpandPath(t *testing.T) {
	if ExpandPath("plain") != "plain" {
		t.Fatal("普通路径不应展开")
	}
	got := ExpandPath("~/.ssh/id_ed25519")
	if got == "~/.ssh/id_ed25519" {
		t.Fatal("~ 应被展开")
	}
	if len(got) < 5 {
		t.Fatalf("展开结果异常: %q", got)
	}
}

func TestCheckStatus(t *testing.T) {
	// 不可达地址应返回离线
	h := &model.Host{Host: "127.0.0.1", Port: 9, User: "x"}
	if s := CheckStatus(h, 2); s != StatusOffline {
		t.Fatalf("不可达主机应为离线，实际 %v", s)
	}
}
