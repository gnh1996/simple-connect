//go:build windows

package sshc

import (
	"fmt"
	"net"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

// agentConn Windows 下通过 OpenSSH Authentication Agent 命名管道连接
func agentConn() (net.Conn, error) {
	const pipe = `\\.\pipe\openssh-ssh-agent`
	h, err := windows.CreateFile(
		windows.StringToUTF16Ptr(pipe),
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		0, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		return nil, fmt.Errorf("无法连接 OpenSSH agent: %w", err)
	}
	return &pipeConn{f: os.NewFile(uintptr(h), "openssh-ssh-agent")}, nil
}

// agentHintText 返回 agent 不可用时的引导文案
func agentHintText() string {
	return "未检测到 ssh-agent，请在 Windows 服务中启动 OpenSSH Authentication Agent，然后运行: ssh-add ~/.ssh/id_ed25519"
}

// pipeConn 将 *os.File（命名管道句柄）适配为 net.Conn
type pipeConn struct{ f *os.File }

func (c *pipeConn) Read(b []byte) (int, error)       { return c.f.Read(b) }
func (c *pipeConn) Write(b []byte) (int, error)      { return c.f.Write(b) }
func (c *pipeConn) Close() error                     { return c.f.Close() }
func (c *pipeConn) LocalAddr() net.Addr              { return nil }
func (c *pipeConn) RemoteAddr() net.Addr             { return nil }
func (c *pipeConn) SetDeadline(time.Time) error      { return nil }
func (c *pipeConn) SetReadDeadline(time.Time) error  { return nil }
func (c *pipeConn) SetWriteDeadline(time.Time) error { return nil }
