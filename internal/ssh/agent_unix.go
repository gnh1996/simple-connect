//go:build !windows

package sshc

import (
	"fmt"
	"net"
	"os"
)

// agentConn 通过 SSH_AUTH_SOCK 连接 ssh-agent
func agentConn() (net.Conn, error) {
	addr := os.Getenv("SSH_AUTH_SOCK")
	if addr == "" {
		return nil, fmt.Errorf("SSH_AUTH_SOCK 未设置，ssh-agent 未运行")
	}
	conn, err := net.Dial("unix", addr)
	if err != nil {
		return nil, fmt.Errorf("连接 ssh-agent 失败: %w", err)
	}
	return conn, nil
}

// agentHintText 返回 agent 不可用时的引导文案
func agentHintText() string {
	return "未检测到 ssh-agent，建议: eval $(ssh-agent) && ssh-add ~/.ssh/id_ed25519"
}
