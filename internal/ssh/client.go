package sshc

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"

	"simple-connect/internal/model"
)

// Client 封装 SSH 客户端
type Client struct {
	*ssh.Client
	Host *model.Host
}

// Option 连接选项
type Option func(*ssh.ClientConfig)

// WithHostKeyCallback 覆盖主机指纹校验策略（测试使用）
func WithHostKeyCallback(cb ssh.HostKeyCallback) Option {
	return func(cfg *ssh.ClientConfig) {
		if cb != nil {
			cfg.HostKeyCallback = cb
		}
	}
}

// Connect 建立 SSH 连接
func Connect(h *model.Host, password string, opts ...Option) (*Client, error) {
	cfg := &ssh.ClientConfig{
		User:            h.User,
		Auth:            authMethods(h, password),
		HostKeyCallback: hostKeyCallback(h.Addr()),
		Timeout:         10 * time.Second,
		ClientVersion:   "SSH-2.0-simple-connect",
	}
	for _, o := range opts {
		o(cfg)
	}
	conn, err := ssh.Dial("tcp", h.Addr(), cfg)
	if err != nil {
		return nil, err
	}
	return &Client{Client: conn, Host: h}, nil
}

// NewSession 创建会话（带 PTY 的交互终端）
func (c *Client) NewTerminalSession(term string, rows, cols int) (*ssh.Session, error) {
	s, err := c.NewSession()
	if err != nil {
		return nil, err
	}
	modes := ssh.TerminalModes{
		ssh.ECHO:          1,
		ssh.TTY_OP_ISPEED: 14400,
		ssh.TTY_OP_OSPEED: 14400,
	}
	if err := s.RequestPty(term, rows, cols, modes); err != nil {
		_ = s.Close()
		return nil, err
	}
	return s, nil
}

// authMethods 根据认证方式生成认证方法
func authMethods(h *model.Host, password string) []ssh.AuthMethod {
	var methods []ssh.AuthMethod
	if h.Auth == model.AuthKey && h.KeyPath != "" {
		if signer, err := loadSigner(h.KeyPath, ""); err == nil {
			methods = append(methods, ssh.PublicKeys(signer))
		}
	}
	if password != "" {
		methods = append(methods, ssh.Password(password))
	}
	// 代理认证兜底
	if agent, err := agentSigner(); err == nil {
		methods = append(methods, ssh.PublicKeysCallback(agent))
	}
	return methods
}

// loadSigner 加载私钥签名器（支持加密私钥）
func loadSigner(keyPath, passphrase string) (ssh.Signer, error) {
	pem, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, err
	}
	if passphrase != "" {
		return ssh.ParsePrivateKeyWithPassphrase(pem, []byte(passphrase))
	}
	return ssh.ParsePrivateKey(pem)
}

// agentSigner 返回 ssh-agent 中的全部签名器（支持 SSH_AUTH_SOCK）
func agentSigner() (func() ([]ssh.Signer, error), error) {
	addr := os.Getenv("SSH_AUTH_SOCK")
	if addr == "" {
		return nil, os.ErrNotExist
	}
	conn, err := net.Dial("unix", addr)
	if err != nil {
		return nil, err
	}
	ag := agent.NewClient(conn)
	return ag.Signers, nil
}

// hostKeyCallback 校验主机指纹；首次连接自动信任并写入 known_hosts
func hostKeyCallback(addr string) ssh.HostKeyCallback {
	kh, err := knownhosts.New(defaultKnownHostsPath())
	if err != nil {
		return ssh.InsecureIgnoreHostKey()
	}
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		var keyErr *knownhosts.KeyError
		if err := kh(hostname, remote, key); err == nil {
			return nil
		} else if !errors.As(err, &keyErr) {
			return err
		} else if len(keyErr.Want) == 0 {
			// 首次连接：信任并追加
			appendKnownHosts(hostname, key)
			return nil
		}
		return fmt.Errorf("主机指纹不匹配，请检查 known_hosts: %w", err)
	}
}

func appendKnownHosts(hostname string, key ssh.PublicKey) {
	path := defaultKnownHostsPath()
	line := knownhosts.Line([]string{hostname}, key)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(line + "\n")
}

func defaultKnownHostsPath() string {
	if u, err := user.Current(); err == nil {
		return filepath.Join(u.HomeDir, ".ssh", "known_hosts")
	}
	return filepath.Join(os.Getenv("HOME"), ".ssh", "known_hosts")
}

// ExpandPath 展开 ~ 前缀
func ExpandPath(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}
