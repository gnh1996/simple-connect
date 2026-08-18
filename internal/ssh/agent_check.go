package sshc

import (
	"os"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// withAgent 建立到 ssh-agent 的连接并执行回调
func withAgent(fn func(a agent.Agent) error) error {
	conn, err := agentConn()
	if err != nil {
		return err
	}
	defer conn.Close()
	return fn(agent.NewClient(conn))
}

// AgentAvailable 检测 ssh-agent 是否可用
func AgentAvailable() bool {
	return withAgent(func(a agent.Agent) error { return nil }) == nil
}

// AgentHint 返回 ssh-agent 不可用时的引导文案；可用时返回空串
func AgentHint() string {
	if AgentAvailable() {
		return ""
	}
	return agentHintText()
}

// AgentFingerprints 返回 agent 中全部密钥的 SHA256 指纹（不可用或为空时返回 nil）
func AgentFingerprints() []string {
	var out []string
	_ = withAgent(func(a agent.Agent) error {
		keys, err := a.List()
		if err != nil {
			return err
		}
		for _, k := range keys {
			if pk, err := ssh.ParsePublicKey(k.Blob); err == nil {
				out = append(out, ssh.FingerprintSHA256(pk))
			}
		}
		return nil
	})
	return out
}

// KeyInAgent 判断私钥是否已加入 ssh-agent。
// 返回 (是否已加入, 是否可确认)；agent 不可用或私钥无法读取时 known=false
func KeyInAgent(keyPath string) (in, known bool) {
	if !AgentAvailable() {
		return false, false
	}
	pub, err := loadPublicKey(keyPath)
	if err != nil {
		return false, false
	}
	fp := ssh.FingerprintSHA256(pub)
	for _, f := range AgentFingerprints() {
		if f == fp {
			return true, true
		}
	}
	return false, true
}

// loadPublicKey 读取私钥对应的公钥（优先 .pub 文件，其次从私钥推导）
func loadPublicKey(keyPath string) (ssh.PublicKey, error) {
	if p := keyPath + ".pub"; fileExists(p) {
		if b, err := os.ReadFile(p); err == nil {
			if pk, err := ssh.ParsePublicKey(b); err == nil {
				return pk, nil
			}
		}
	}
	signer, err := loadSigner(keyPath, "")
	if err != nil {
		return nil, err
	}
	return signer.PublicKey(), nil
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
