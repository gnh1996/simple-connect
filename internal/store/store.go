package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"

	"simple-connect/internal/model"
)

// Store 负责连接配置与密钥的持久化。
// 多实例并发安全：所有变更操作先获取文件锁，再重读磁盘最新数据按 ID 应用变更，
// 原子写回；读取方通过共享锁 / 原子写保证看不到半截文件。
type Store struct {
	path     string
	lockPath string
	hosts    []*model.Host
	secrets  Secrets
	keyring  bool
}

// Load 从用户配置目录加载存储
func Load() (*Store, error) {
	dir, err := configDir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "hosts.json")
	s := &Store{
		path:     path,
		lockPath: filepath.Join(dir, "hosts.lock"),
		secrets:  newSecrets(dir),
		keyring:  true,
	}
	if _, ok := s.secrets.(*fileSecrets); ok {
		s.keyring = false
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

// configDir 返回应用配置目录
func configDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "simple-connect"), nil
}

func (s *Store) load() error {
	hosts, err := s.readHosts()
	if err != nil {
		return err
	}
	s.hosts = hosts
	return nil
}

// withExclusiveLock 持有排他锁执行 fn（读-改-写操作互斥）
func (s *Store) withExclusiveLock(fn func() error) error {
	l, err := acquireLock(s.lockPath, false)
	if err != nil {
		return err
	}
	defer l.release()
	return fn()
}

// withSharedLock 持有共享锁执行 fn（只读重载，可并发）
func (s *Store) withSharedLock(fn func() error) error {
	l, err := acquireLock(s.lockPath, true)
	if err != nil {
		return err
	}
	defer l.release()
	return fn()
}

// readHosts 从磁盘读取最新连接列表（文件不存在返回空切片）
func (s *Store) readHosts() ([]*model.Host, error) {
	b, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []*model.Host{}, nil
		}
		return nil, err
	}
	var hosts []*model.Host
	if err := json.Unmarshal(b, &hosts); err != nil {
		return nil, err
	}
	return hosts, nil
}

// writeHosts 原子写回连接列表（临时文件 + rename，避免并发读看到半截文件）
func (s *Store) writeHosts(hosts []*model.Host) error {
	b, err := json.MarshalIndent(hosts, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// Hosts 返回按名称排序的连接列表
func (s *Store) Hosts() []*model.Host {
	out := make([]*model.Host, len(s.hosts))
	copy(out, s.hosts)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Add 新增连接。写前重读磁盘最新列表合并追加（多实例并发安全）。
func (s *Store) Add(h *model.Host) error {
	if h.ID == "" {
		h.ID = model.NewID()
	}
	return s.withExclusiveLock(func() error {
		hosts, err := s.readHosts()
		if err != nil {
			return err
		}
		for _, e := range hosts {
			if e.ID == h.ID {
				return errors.New("连接已存在")
			}
		}
		hosts = append(hosts, h)
		if err := s.writeHosts(hosts); err != nil {
			return err
		}
		s.hosts = hosts
		return nil
	})
}

// Update 更新连接。基于最新快照按 ID 替换（目标已被删除则报错，不静默复活）。
func (s *Store) Update(h *model.Host) error {
	return s.withExclusiveLock(func() error {
		hosts, err := s.readHosts()
		if err != nil {
			return err
		}
		idx := -1
		for i, e := range hosts {
			if e.ID == h.ID {
				idx = i
				break
			}
		}
		if idx < 0 {
			return errors.New("连接不存在")
		}
		hosts[idx] = h
		if err := s.writeHosts(hosts); err != nil {
			return err
		}
		s.hosts = hosts
		return nil
	})
}

// Delete 删除连接（含对应密钥）
func (s *Store) Delete(id string) error {
	return s.withExclusiveLock(func() error {
		hosts, err := s.readHosts()
		if err != nil {
			return err
		}
		var out []*model.Host
		found := false
		for _, e := range hosts {
			if e.ID == id {
				found = true
				continue
			}
			out = append(out, e)
		}
		if !found {
			return errors.New("连接不存在")
		}
		if err := s.writeHosts(out); err != nil {
			return err
		}
		_ = s.secrets.Delete(id)
		s.hosts = out
		return nil
	})
}

// Find 按 ID 查找连接
func (s *Store) Find(id string) *model.Host {
	for _, e := range s.hosts {
		if e.ID == id {
			return e
		}
	}
	return nil
}

// Password 读取连接密码
func (s *Store) Password(h *model.Host) (string, bool) {
	v, ok, err := s.secrets.Get(h.ID)
	if err != nil || !ok {
		return "", false
	}
	return v, true
}

// SetPassword 保存/清除连接密码。锁内同步更新 hosts 与 secrets，保持一致性。
func (s *Store) SetPassword(h *model.Host, pass string) error {
	return s.withExclusiveLock(func() error {
		hosts, err := s.readHosts()
		if err != nil {
			return err
		}
		idx := -1
		for i, e := range hosts {
			if e.ID == h.ID {
				idx = i
				break
			}
		}
		if idx < 0 {
			return errors.New("连接不存在")
		}
		if pass == "" {
			if err := s.secrets.Delete(h.ID); err != nil {
				return err
			}
			hosts[idx].HasPassword = false
			h.HasPassword = false
		} else {
			if err := s.secrets.Set(h.ID, pass); err != nil {
				return err
			}
			hosts[idx].HasPassword = true
			h.HasPassword = true
		}
		if err := s.writeHosts(hosts); err != nil {
			return err
		}
		s.hosts = hosts
		return nil
	})
}

// Reload 从磁盘重新加载连接列表（共享锁，多实例间看到彼此的修改）
func (s *Store) Reload() error {
	return s.withSharedLock(func() error {
		hosts, err := s.readHosts()
		if err != nil {
			return err
		}
		s.hosts = hosts
		return nil
	})
}

// UsingKeyring 是否使用系统 keyring 存储密码
func (s *Store) UsingKeyring() bool { return s.keyring }
