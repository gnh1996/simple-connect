package testutil

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"
)

// ShellEnv 交互 shell 测试服务器环境
type ShellEnv struct {
	Addr string
	User string
	Pass string

	mu   sync.Mutex
	rows int
	cols int
}

// WindowSize 读取测试服务器记录的最新 PTY 窗口尺寸
func (e *ShellEnv) WindowSize() (rows, cols int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.rows, e.cols
}

// StartShell 启动支持交互 shell 的测试 SSH 服务器。
// 认证仅走 keyboard-interactive（验证 User/Pass）；shell 请求后回显输入行，
// 并记录最新 PTY 窗口尺寸（WindowSize 读取）。
func StartShell(t *testing.T) *ShellEnv {
	t.Helper()
	env := &ShellEnv{User: "tester", Pass: "secret"}

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{
		// 拒绝 password 认证，仅接受 keyboard-interactive
		PasswordCallback: func(c ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
			return nil, errors.New("password 认证禁用")
		},
		KeyboardInteractiveCallback: func(c ssh.ConnMetadata, challenge ssh.KeyboardInteractiveChallenge) (*ssh.Permissions, error) {
			answers, err := challenge("", "", []string{"Password: "}, []bool{true})
			if err != nil {
				return nil, err
			}
			if len(answers) > 0 && answers[0] == env.Pass {
				return nil, nil
			}
			return nil, errors.New("认证失败")
		},
	}
	cfg.AddHostKey(signer)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go serveShell(conn, cfg, env)
		}
	}()

	env.Addr = ln.Addr().String()
	return env
}

func serveShell(conn net.Conn, cfg *ssh.ServerConfig, env *ShellEnv) {
	sconn, chans, reqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		_ = conn.Close()
		return
	}
	defer sconn.Close()
	go ssh.DiscardRequests(reqs)

	for newChan := range chans {
		if newChan.ChannelType() != "session" {
			_ = newChan.Reject(ssh.UnknownChannelType, "不支持的通道")
			continue
		}
		ch, requests, err := newChan.Accept()
		if err != nil {
			continue
		}
		go func(ch ssh.Channel, requests <-chan *ssh.Request) {
			defer ch.Close()
			for req := range requests {
				switch req.Type {
				case "pty-req":
					if len(req.Payload) >= 12 {
						l := int(binary.BigEndian.Uint32(req.Payload[:4]))
						if len(req.Payload) >= 4+l+8 {
							cols := binary.BigEndian.Uint32(req.Payload[4+l : 4+l+4])
							rows := binary.BigEndian.Uint32(req.Payload[4+l+4 : 4+l+8])
							env.mu.Lock()
							env.rows, env.cols = int(rows), int(cols)
							env.mu.Unlock()
						}
					}
					if req.WantReply {
						_ = req.Reply(true, nil)
					}
				case "shell":
					if req.WantReply {
						_ = req.Reply(true, nil)
					}
					// 回显 shell：读到数据原样回写
					_, _ = io.Copy(ch, ch)
					return
				default:
					if req.WantReply {
						_ = req.Reply(false, nil)
					}
				}
			}
		}(ch, requests)
	}
}
