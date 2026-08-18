package model

import (
	"crypto/rand"
	"encoding/hex"
)

// 认证方式
type AuthType string

const (
	AuthPassword AuthType = "password" // 密码认证
	AuthKey      AuthType = "key"      // 私钥认证
)

// Host 表示一条 SSH 连接配置
type Host struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Host        string   `json:"host"`
	Port        int      `json:"port"`
	User        string   `json:"user"`
	Auth        AuthType `json:"auth"`
	KeyPath     string   `json:"key_path,omitempty"`
	HasPassword bool     `json:"has_password"`
	LocalDir    string   `json:"local_dir,omitempty"` // SFTP 默认本地目录
}

// Addr 返回 host:port
func (h *Host) Addr() string {
	port := h.Port
	if port <= 0 {
		port = 22
	}
	return joinHostPort(h.Host, port)
}

// NewID 生成随机连接 ID
func NewID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
