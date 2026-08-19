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

// Connect 建立 SSH 连接（自动合并 ~/.ssh/config，支持 ProxyJump 跳板）
func Connect(h *model.Host, password string, opts ...Option) (*Client, error) {
	target, jumps := ResolveSSHConfig(h)
	cfg, err := newConfig(target, password)
	if err != nil {
		return nil, err
	}
	for _, o := range opts {
		o(cfg)
	}
	conn, err := dialWithJumps(jumps, cfg, opts, password, target)
	if err != nil {
		return nil, err
	}
	return &Client{Client: conn, Host: target}, nil
}

// ConnectRaw 建立 SSH 连接（不合并 ~/.ssh/config，测试与内嵌 SFTP 使用）
func ConnectRaw(h *model.Host, password string, opts ...Option) (*Client, error) {
	cfg, err := newConfig(h, password)
	if err != nil {
		return nil, err
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

// newConfig 构造 SSH 客户端配置
func newConfig(h *model.Host, password string) (*ssh.ClientConfig, error) {
	hk, err := hostKeyCallback(h.Addr())
	if err != nil {
		return nil, err
	}
	return &ssh.ClientConfig{
		User:            h.User,
		Auth:            authMethods(h, password),
		HostKeyCallback: hk,
		Timeout:         10 * time.Second,
		ClientVersion:   "SSH-2.0-simple-connect",
	}, nil
}

// dialWithJumps 依次经跳板机隧道连接目标；无跳板时直连
func dialWithJumps(jumps []*model.Host, targetCfg *ssh.ClientConfig, opts []Option, password string, target *model.Host) (*ssh.Client, error) {
	var current *ssh.Client
	var err error
	for _, j := range jumps {
		jcfg, err := newConfig(j, password) // 跳板复用目标保存的密码与密钥
		if err != nil {
			return nil, err
		}
		for _, o := range opts {
			o(jcfg)
		}
		if current == nil {
			current, err = ssh.Dial("tcp", j.Addr(), jcfg)
		} else {
			var c net.Conn
			c, err = current.Dial("tcp", j.Addr())
			if err == nil {
				cc, chans, reqs, e := ssh.NewClientConn(c, j.Addr(), jcfg)
				if e != nil {
					err = e
				} else {
					current = ssh.NewClient(cc, chans, reqs)
				}
			}
		}
		if err != nil {
			return nil, fmt.Errorf("连接跳板 %s 失败: %w", j.Addr(), err)
		}
	}

	if current == nil {
		return ssh.Dial("tcp", target.Addr(), targetCfg)
	}
	c, err := current.Dial("tcp", target.Addr())
	if err != nil {
		return nil, fmt.Errorf("经跳板连接 %s 失败: %w", target.Addr(), err)
	}
	cc, chans, reqs, err := ssh.NewClientConn(c, target.Addr(), targetCfg)
	if err != nil {
		return nil, err
	}
	return ssh.NewClient(cc, chans, reqs), nil
}

// NewSession 创建会话（带 PTY 的交互终端）
func (c *Client) NewTerminalSession(term string, rows, cols int) (*ssh.Session, error) {
	s, err := c.NewSession()
	if err != nil {
		return nil, err
	}
	// 与 OpenSSH 对齐的 tty 模式。
	// 开关类模式显式发送（未发送的项保留服务器 tty 默认值，老系统/网络设备上
	// 可能关闭 IUTF8/IMAXBEL 等关键项，导致退格吃半个 UTF-8 字符、光标错位）；
	// 控制字符（VINTR/VERASE/VSUSP 等）不发送，保留服务器配置。
	// ECHO=0：会话建立即关闭回显——首条 cwd 钩子注入命令从第一条起就不回显
	//（无 stty -echo 引导行残留，见 internal/session）；注入末尾 `stty echo`
	// 恢复交互回显，之后与 OpenSSH（ECHO=1）表现一致。
	modes := ssh.TerminalModes{
		ssh.ECHO:          0,
		ssh.ECHOE:         1,
		ssh.ECHOK:         1,
		ssh.ECHOKE:        1,
		ssh.ECHOCTL:       1,
		ssh.ECHONL:        0,
		ssh.ICANON:        1,
		ssh.IEXTEN:        1,
		ssh.ISIG:          1,
		ssh.IXON:          1,
		ssh.IXANY:         0,
		ssh.IXOFF:         0,
		ssh.IMAXBEL:       1,
		ssh.IUTF8:         1, // 关键：line discipline 按 UTF-8 处理，退格/光标按字符而非字节
		ssh.ICRNL:         1,
		ssh.INLCR:         0,
		ssh.IGNCR:         0,
		ssh.IGNPAR:        0,
		ssh.PARMRK:        0,
		ssh.INPCK:         0,
		ssh.ISTRIP:        0,
		ssh.IUCLC:         0,
		ssh.OPOST:         1,
		ssh.ONLCR:         1,
		ssh.OCRNL:         0,
		ssh.ONOCR:         0,
		ssh.ONLRET:        0,
		ssh.OLCUC:         0,
		ssh.CS8:           1,
		ssh.CS7:           0,
		ssh.PARENB:        0,
		ssh.PARODD:        0,
		ssh.NOFLSH:        0,
		ssh.TOSTOP:        0,
		ssh.XCASE:         0,
		ssh.PENDIN:        0,
		ssh.TTY_OP_ISPEED: 38400,
		ssh.TTY_OP_OSPEED: 38400,
	}
	if err := s.RequestPty(term, rows, cols, modes); err != nil {
		_ = s.Close()
		return nil, err
	}
	return s, nil
}

// authMethods 根据认证方式生成认证方法（私钥 + 密码 + keyboard-interactive + agent）
func authMethods(h *model.Host, password string) []ssh.AuthMethod {
	var methods []ssh.AuthMethod
	if h.Auth == model.AuthKey && h.KeyPath != "" {
		if signer, err := loadSigner(h.KeyPath, ""); err == nil {
			methods = append(methods, ssh.PublicKeys(signer))
		}
	}
	if password != "" {
		methods = append(methods, ssh.Password(password))
		// 兼容部分服务器使用 keyboard-interactive 认证
		methods = append(methods, ssh.KeyboardInteractive(passwordAnswer(password)))
	}
	// 代理认证兜底
	if agent, err := agentSigner(); err == nil {
		methods = append(methods, ssh.PublicKeysCallback(agent))
	}
	return methods
}

// passwordAnswer 用保存的密码应答 keyboard-interactive 提示。
// 安全限制（对齐 OpenSSH 默认行为）：仅当「单个提示且不回显输入」时才回填密码；
// 多提示（OTP/堡垒机二次验证）或提示回显输入时一律中止，避免用密码应答验证码
// 提示导致多次失败锁账号。
func passwordAnswer(password string) func(string, string, []string, []bool) ([]string, error) {
	return func(name, instruction string, questions []string, echos []bool) ([]string, error) {
		if len(questions) != 1 {
			return nil, errors.New("keyboard-interactive 多提示，中止盲答")
		}
		if len(echos) > 0 && echos[0] {
			return nil, errors.New("keyboard-interactive 提示回显输入，中止盲答")
		}
		return []string{password}, nil
	}
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

// agentSigner 返回 ssh-agent 中的全部签名器
func agentSigner() (func() ([]ssh.Signer, error), error) {
	conn, err := agentConn()
	if err != nil {
		return nil, err
	}
	ag := agent.NewClient(conn)
	return ag.Signers, nil
}

// UnknownHostKeyError 首次连接错误：主机指纹不在 known_hosts 中。
// 由调用方展示 Fingerprint 征求用户确认；确认后调 TrustHostKey 追加 known_hosts
// 并重新连接（对齐 OpenSSH ask 模式，不再静默信任）。
type UnknownHostKeyError struct {
	Hostname    string
	Fingerprint string
	key         ssh.PublicKey
}

func (e *UnknownHostKeyError) Error() string {
	return fmt.Sprintf("主机 %s 首次连接，指纹 SHA256:%s 未确认", e.Hostname, e.Fingerprint)
}

// TrustHostKey 将 UnknownHostKeyError 携带的主机指纹追加到 known_hosts
//（用户确认信任后调用）。返回错误表示追加失败。
func TrustHostKey(e *UnknownHostKeyError) error {
	return trustHostKeyPath(e, defaultKnownHostsPath())
}

func trustHostKeyPath(e *UnknownHostKeyError, path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("创建 %s 失败: %w", filepath.Dir(path), err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("打开 known_hosts 失败: %w", err)
	}
	defer f.Close()
	line := knownhosts.Line([]string{e.Hostname}, e.key)
	if _, err := f.WriteString(line + "\n"); err != nil {
		return fmt.Errorf("写入 known_hosts 失败: %w", err)
	}
	return nil
}

// hostKeyCallback 校验主机指纹。
//   - known_hosts 解析失败（如文件损坏）直接报错，禁止静默降级 InsecureIgnoreHostKey；
//   - 首次连接（主机不在 known_hosts）返回 UnknownHostKeyError，由调用方展示指纹并
//     征得用户确认后调 TrustHostKey 追加（对齐 OpenSSH ask 模式）；
//   - 指纹不匹配（Want 非空）拒绝连接。
func hostKeyCallback(addr string) (ssh.HostKeyCallback, error) {
	return hostKeyCallbackPath(addr, defaultKnownHostsPath())
}

func hostKeyCallbackPath(addr, path string) (ssh.HostKeyCallback, error) {
	// 首次使用（如全新安装）：known_hosts 文件不存在时先创建空文件，
	// 否则 knownhosts.New 会报错（不存在的文件 ≠ 空列表）。
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			if mkErr := os.MkdirAll(filepath.Dir(path), 0o700); mkErr != nil {
				return nil, fmt.Errorf("创建 %s 失败: %w", filepath.Dir(path), mkErr)
			}
			f, oErr := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o600)
			if oErr != nil {
				return nil, fmt.Errorf("创建 known_hosts 失败: %w", oErr)
			}
			_ = f.Close()
		} else {
			return nil, fmt.Errorf("读取 known_hosts（%s）失败: %w", path, err)
		}
	}
	kh, err := knownhosts.New(path)
	if err != nil {
		return nil, fmt.Errorf("解析 known_hosts（%s）失败: %w", path, err)
	}
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		var keyErr *knownhosts.KeyError
		if err := kh(hostname, remote, key); err == nil {
			return nil
		} else if !errors.As(err, &keyErr) {
			return err
		} else if len(keyErr.Want) == 0 {
			return &UnknownHostKeyError{Hostname: hostname, Fingerprint: ssh.FingerprintSHA256(key), key: key}
		}
		return fmt.Errorf("主机指纹不匹配，请检查 known_hosts: %w", err)
	}, nil
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
