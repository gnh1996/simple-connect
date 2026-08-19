package testutil

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync"

	"golang.org/x/crypto/ssh"
)

// ShellEnv 交互 shell 测试服务器环境
type ShellEnv struct {
	Addr string
	User string
	Pass string

	mu    sync.Mutex
	rows  int
	cols  int
	envs  map[string]string // 收到的 env 请求（name → value）
	modes map[uint32]uint32 // 收到的 pty-req 终端模式（opcode → value）
}

// WindowSize 读取测试服务器记录的最新 PTY 窗口尺寸
func (e *ShellEnv) WindowSize() (rows, cols int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.rows, e.cols
}

// Env 读取测试服务器收到的 env 请求（name → value）
func (e *ShellEnv) Env() map[string]string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make(map[string]string, len(e.envs))
	for k, v := range e.envs {
		out[k] = v
	}
	return out
}

// PtyModes 读取测试服务器收到的 pty-req 终端模式（opcode → value）
func (e *ShellEnv) PtyModes() map[uint32]uint32 {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make(map[uint32]uint32, len(e.modes))
	for k, v := range e.modes {
		out[k] = v
	}
	return out
}

// StartShell 启动支持交互 shell 的测试 SSH 服务器。
// 认证仅走 keyboard-interactive（验证 User/Pass）；shell 请求后回显输入行，
// 并记录最新 PTY 窗口尺寸（WindowSize 读取）。
func StartShell(tb TB) *ShellEnv {
	tb.Helper()
	env := &ShellEnv{User: "tester", Pass: "secret"}

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		tb.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		tb.Fatal(err)
	}
	cfg := &ssh.ServerConfig{
		// 拒绝 password 认证，仅接受 keyboard-interactive
		PasswordCallback: func(c ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
			return nil, errors.New("password 认证禁用")
		},
		KeyboardInteractiveCallback: func(c ssh.ConnMetadata, challenge ssh.KeyboardInteractiveChallenge) (*ssh.Permissions, error) {
			answers, err := challenge("", "", []string{"Password: "}, []bool{false})
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
		tb.Fatal(err)
	}
	tb.Cleanup(func() { _ = ln.Close() })

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

// parsePtyModes 解析 pty-req 的 tty 模式段：
// 重复的 (opcode byte, value uint32 BE) 对，opcode 0 表示结束。
func parsePtyModes(b []byte) map[uint32]uint32 {
	modes := map[uint32]uint32{}
	for len(b) >= 5 {
		op := b[0]
		if op == 0 {
			break
		}
		modes[uint32(op)] = binary.BigEndian.Uint32(b[1:5])
		b = b[5:]
	}
	return modes
}

// serveShell 处理单个 SSH 连接，回显 shell 数据并记录 PTY 尺寸/env/modes。
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
				case "env":
					// payload = string(name) + string(value)
					if len(req.Payload) >= 8 {
						nl := int(binary.BigEndian.Uint32(req.Payload[:4]))
						if len(req.Payload) >= 4+nl+4 {
							name := string(req.Payload[4 : 4+nl])
							// 对齐真实 OpenSSH：默认 AcceptEnv 仅接受 LANG/LC_*，
							// PROMPT_COMMAND 不在白名单 → 拒绝该 env 请求且不记录
							// （cwd 钩子改为 shell 内注入，见 internal/session）。
							if name == "PROMPT_COMMAND" {
								if req.WantReply {
									_ = req.Reply(false, nil)
								}
								continue
							}
							vl := int(binary.BigEndian.Uint32(req.Payload[4+nl : 4+nl+4]))
							if len(req.Payload) >= 4+nl+4+vl {
								value := string(req.Payload[4+nl+4 : 4+nl+4+vl])
								env.mu.Lock()
								if env.envs == nil {
									env.envs = map[string]string{}
								}
								env.envs[name] = value
								env.mu.Unlock()
							}
						}
					}
					if req.WantReply {
						_ = req.Reply(true, nil)
					}
				case "pty-req":
					if len(req.Payload) >= 12 {
						l := int(binary.BigEndian.Uint32(req.Payload[:4]))
						if len(req.Payload) >= 4+l+8 {
							cols := binary.BigEndian.Uint32(req.Payload[4+l : 4+l+4])
							rows := binary.BigEndian.Uint32(req.Payload[4+l+4 : 4+l+8])
							// modes: 跳过像素宽高(8B)，4 字节长度 + 数据
							off := 4 + l + 8 + 8
							if len(req.Payload) >= off+4 {
								ml := int(binary.BigEndian.Uint32(req.Payload[off : off+4]))
								if len(req.Payload) >= off+4+ml {
									modes := parsePtyModes(req.Payload[off+4 : off+4+ml])
									env.mu.Lock()
									env.rows, env.cols = int(rows), int(cols)
									env.modes = modes
									env.mu.Unlock()
								}
							}
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
