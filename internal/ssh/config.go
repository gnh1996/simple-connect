package sshc

import (
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/kevinburke/ssh_config"

	"simple-connect/internal/model"
)

// configGetter 抽象 ssh 配置查询，便于测试注入
type configGetter interface {
	Get(alias, key string) string
}

type getterFunc func(alias, key string) string

func (f getterFunc) Get(alias, key string) string { return f(alias, key) }

// sshConfigGetter 基于解析后的 ssh config 查询。
// 与 ssh_config.Get 不同：无匹配条目时返回空（不使用默认值），
// 避免"不在 config 中的主机"被默认值污染（Port 默认 22、IdentityFile 默认 ~/.ssh/identity）。
type sshConfigGetter struct {
	cfg *ssh_config.Config
}

func (g *sshConfigGetter) Get(alias, key string) string {
	if g.cfg == nil {
		return ""
	}
	v, err := g.cfg.Get(alias, key)
	if err != nil {
		return ""
	}
	return v
}

var (
	sshConfigOnce sync.Once
	sshConfigVal  *ssh_config.Config
)

// loadSSHConfig 解析用户与系统 ssh config（用户优先），懒加载并缓存
func loadSSHConfig() *ssh_config.Config {
	sshConfigOnce.Do(func() {
		sshConfigVal = parseSSHConfigFile(userSSHConfigPath())
		if sys := parseSSHConfigFile(systemSSHConfigPath()); sys != nil {
			if sshConfigVal == nil {
				sshConfigVal = sys
			} else {
				sshConfigVal = &ssh_config.Config{Hosts: append(sshConfigVal.Hosts, sys.Hosts...)}
			}
		}
	})
	return sshConfigVal
}

func parseSSHConfigFile(path string) *ssh_config.Config {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	cfg, err := ssh_config.Decode(f)
	if err != nil {
		return nil
	}
	return cfg
}

func userSSHConfigPath() string {
	if u, err := user.Current(); err == nil {
		return filepath.Join(u.HomeDir, ".ssh", "config")
	}
	return filepath.Join(os.Getenv("HOME"), ".ssh", "config")
}

func systemSSHConfigPath() string {
	return "/etc/ssh/ssh_config"
}

// ResolveSSHConfig 合并 ~/.ssh/config 配置到主机（别名/User/Port/IdentityFile/ProxyJump）。
// 仅当主机别名匹配 config 中的 Host 块时才合并；无匹配时返回原样副本。
// 返回合并后的主机（新副本，不修改原主机）与跳板机链。
func ResolveSSHConfig(h *model.Host) (*model.Host, []*model.Host) {
	return resolveSSHConfig(h, &sshConfigGetter{cfg: loadSSHConfig()})
}

func resolveSSHConfig(h *model.Host, get configGetter) (*model.Host, []*model.Host) {
	resolved := *h
	alias := h.Host

	if hostName := get.Get(alias, "HostName"); hostName != "" {
		resolved.Host = hostName
	}
	if user := get.Get(alias, "User"); user != "" {
		resolved.User = user
	}
	if portStr := get.Get(alias, "Port"); portStr != "" {
		if p, err := strconv.Atoi(portStr); err == nil && p > 0 {
			resolved.Port = p
		}
	}
	if idFile := get.Get(alias, "IdentityFile"); idFile != "" {
		resolved.KeyPath = ExpandPath(strings.Trim(idFile, `"`))
		resolved.Auth = model.AuthKey
	}

	var jumps []*model.Host
	if pj := get.Get(alias, "ProxyJump"); pj != "" {
		jumps = parseProxyJump(pj)
	}
	return &resolved, jumps
}

// parseProxyJump 解析 ProxyJump 链（user@host:port，逗号分隔）
func parseProxyJump(s string) []*model.Host {
	var jumps []*model.Host
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		j := &model.Host{}
		if at := strings.LastIndex(part, "@"); at >= 0 {
			j.User = part[:at]
			part = part[at+1:]
		}
		hostPort := strings.Split(part, ":")
		j.Host = hostPort[0]
		if len(hostPort) > 1 {
			if p, err := strconv.Atoi(hostPort[1]); err == nil {
				j.Port = p
			}
		}
		jumps = append(jumps, j)
	}
	return jumps
}
