package sshc

import (
	"bufio"
	"net"
	"strings"
	"time"

	"simple-connect/internal/model"
)

// 连接状态
type Status int

const (
	StatusUnknown Status = iota
	StatusOnline
	StatusOffline
)

func (s Status) String() string {
	switch s {
	case StatusOnline:
		return "在线"
	case StatusOffline:
		return "离线"
	default:
		return "未知"
	}
}

// CheckStatus 检测主机连接状态：TCP 拨号并校验 SSH 协议头
func CheckStatus(h *model.Host, timeout time.Duration) Status {
	addr := h.Addr()
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return StatusOffline
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil || !strings.HasPrefix(line, "SSH-") {
		return StatusOffline
	}
	return StatusOnline
}
