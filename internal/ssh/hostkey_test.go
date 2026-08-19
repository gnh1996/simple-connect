package sshc

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/ssh"
)

// testPublicKey 生成测试主机公钥
func testPublicKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return signer.PublicKey()
}

func fakeTCPAddr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 22}
}

// TestHostKeyCallbackFirstConnect 回归：首次连接不得静默信任，必须返回
// UnknownHostKeyError 交由调用方征得用户确认（对齐 OpenSSH ask 模式）。
func TestHostKeyCallbackFirstConnect(t *testing.T) {
	kh := filepath.Join(t.TempDir(), "known_hosts")
	cb, err := hostKeyCallbackPath("", kh)
	if err != nil {
		t.Fatalf("构造 callback 失败: %v", err)
	}
	key := testPublicKey(t)
	err = cb("example.com:22", fakeTCPAddr(), key)
	var uk *UnknownHostKeyError
	if !errors.As(err, &uk) {
		t.Fatalf("首次连接应返回 UnknownHostKeyError，实际: %v", err)
	}
	if uk.Fingerprint != ssh.FingerprintSHA256(key) {
		t.Fatalf("指纹不符: %s != %s", uk.Fingerprint, ssh.FingerprintSHA256(key))
	}
	if uk.Hostname != "example.com:22" {
		t.Fatalf("主机名不符: %s", uk.Hostname)
	}
}

// TestHostKeyTrustFlow 回归：确认信任（TrustHostKey）后同指纹放行，不同指纹拒绝。
func TestHostKeyTrustFlow(t *testing.T) {
	kh := filepath.Join(t.TempDir(), "known_hosts")
	cb, err := hostKeyCallbackPath("", kh)
	if err != nil {
		t.Fatalf("构造 callback 失败: %v", err)
	}
	key := testPublicKey(t)

	// 首次连接 → 待确认错误
	err = cb("example.com:22", fakeTCPAddr(), key)
	var uk *UnknownHostKeyError
	if !errors.As(err, &uk) {
		t.Fatalf("首次连接应返回 UnknownHostKeyError，实际: %v", err)
	}

	// 信任后写入 known_hosts
	if err := trustHostKeyPath(uk, kh); err != nil {
		t.Fatalf("TrustHostKey 失败: %v", err)
	}
	b, err := os.ReadFile(kh)
	if err != nil {
		t.Fatalf("读取 known_hosts 失败: %v", err)
	}
	if len(b) == 0 {
		t.Fatal("known_hosts 未被写入")
	}

	// 重新连接会新建 callback（knownhosts 数据库在创建时读入文件），验证已放行
	cb, err = hostKeyCallbackPath("", kh)
	if err != nil {
		t.Fatalf("重建 callback 失败: %v", err)
	}

	// 同指纹 → 放行
	if err := cb("example.com:22", fakeTCPAddr(), key); err != nil {
		t.Fatalf("信任后同指纹应放行: %v", err)
	}

	// 不同指纹 → 拒绝（指纹被替换检测）
	other := testPublicKey(t)
	if err := cb("example.com:22", fakeTCPAddr(), other); err == nil {
		t.Fatal("不同指纹应拒绝")
	}
}

// TestHostKeyCallbackCorruptKnownHosts 回归：known_hosts 解析失败必须显式报错，
// 禁止静默降级 InsecureIgnoreHostKey。
func TestHostKeyCallbackCorruptKnownHosts(t *testing.T) {
	kh := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(kh, []byte("this is not a valid known_hosts line\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cb, err := hostKeyCallbackPath("", kh)
	if err == nil {
		t.Fatal("损坏的 known_hosts 应显式报错，不得静默降级")
	}
	if cb != nil {
		t.Fatal("出错时不应返回 callback")
	}
}

// TestPasswordAnswer 回归：k-i 盲答限制——仅单个提示且不回显输入时回填密码，
// 多提示/回显输入一律中止（避免用密码应答验证码导致多次失败锁账号）。
func TestPasswordAnswer(t *testing.T) {
	cb := passwordAnswer("secret")
	name, instr := "", ""

	// 单个提示且不回显 → 应答密码
	answers, err := cb(name, instr, []string{"Password: "}, []bool{false})
	if err != nil || len(answers) != 1 || answers[0] != "secret" {
		t.Fatalf("单提示不回显应应答密码: answers=%v err=%v", answers, err)
	}

	// 未提供回显信息（部分服务器省略）→ 默认视为不回显，应答密码
	answers, err = cb(name, instr, []string{"Password: "}, nil)
	if err != nil || len(answers) != 1 || answers[0] != "secret" {
		t.Fatalf("无回显信息应默认应答: answers=%v err=%v", answers, err)
	}

	// 单提示但回显输入（如用户名/验证码）→ 中止
	if _, err := cb(name, instr, []string{"Verification code: "}, []bool{true}); err == nil {
		t.Fatal("回显输入的提示应中止盲答")
	}

	// 多提示（OTP/堡垒机二次验证）→ 中止
	if _, err := cb(name, instr, []string{"Password: ", "OTP: "}, []bool{false, false}); err == nil {
		t.Fatal("多提示应中止盲答")
	}
}
