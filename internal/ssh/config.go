package sshc

import (
	"strconv"
	"strings"

	"github.com/kevinburke/ssh_config"

	"simple-connect/internal/model"
)

// configGetter 抽象 ssh 配置查询，便于测试注入
type configGetter interface {
	Get(alias, key string) string
}

type getterFunc func(alias, key string) string

func (f getterFunc) Get(alias, key string) string { return f(alias, key) }

// ResolveSSHConfig 合并 ~/.ssh/config 配置到主机（别名/User/Port/IdentityFile/ProxyJump）。
// 返回合并后的主机（新副本，不修改原主机）与跳板机链。
func ResolveSSHConfig(h *model.Host) (*model.Host, []*model.Host) {
	return resolveSSHConfig(h, getterFunc(ssh_config.Get))
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

// parseProxyJump 解析 ProxyJump 语法 user@host:port[,user2@host2:port2...]
func parseProxyJump(spec string) []*model.Host {
	var out []*model.Host
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		j := &model.Host{Auth: model.AuthPassword}
		if idx := strings.LastIndex(part, "@"); idx >= 0 {
			j.User = part[:idx]
			part = part[idx+1:]
		}
		if idx := strings.LastIndex(part, ":"); idx >= 0 {
			if p, err := strconv.Atoi(part[idx+1:]); err == nil && p > 0 {
				j.Port = p
			}
			part = part[:idx]
		}
		j.Host = part
		out = append(out, j)
	}
	return out
}
