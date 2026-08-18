package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"

	"simple-connect/internal/model"
)

// Store 负责连接配置与密钥的持久化
type Store struct {
	path    string
	hosts   []*model.Host
	secrets Secrets
	keyring bool
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
		path:    path,
		secrets: newSecrets(dir),
		keyring: true,
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
	b, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			s.hosts = []*model.Host{}
			return nil
		}
		return err
	}
	return json.Unmarshal(b, &s.hosts)
}

func (s *Store) save() error {
	b, err := json.MarshalIndent(s.hosts, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, b, 0o600)
}

// Hosts 返回按名称排序的连接列表
func (s *Store) Hosts() []*model.Host {
	out := make([]*model.Host, len(s.hosts))
	copy(out, s.hosts)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Add 新增连接
func (s *Store) Add(h *model.Host) error {
	if h.ID == "" {
		h.ID = model.NewID()
	}
	s.hosts = append(s.hosts, h)
	return s.save()
}

// Update 更新连接
func (s *Store) Update(h *model.Host) error {
	for i, e := range s.hosts {
		if e.ID == h.ID {
			s.hosts[i] = h
			return s.save()
		}
	}
	return errors.New("连接不存在")
}

// Delete 删除连接
func (s *Store) Delete(id string) error {
	for i, e := range s.hosts {
		if e.ID == id {
			s.hosts = append(s.hosts[:i], s.hosts[i+1:]...)
			_ = s.secrets.Delete(id)
			return s.save()
		}
	}
	return errors.New("连接不存在")
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

// SetPassword 保存连接密码
func (s *Store) SetPassword(h *model.Host, pass string) error {
	if pass == "" {
		_ = s.secrets.Delete(h.ID)
		h.HasPassword = false
		return s.save()
	}
	if err := s.secrets.Set(h.ID, pass); err != nil {
		return err
	}
	h.HasPassword = true
	return s.save()
}

// UsingKeyring 是否使用系统 keyring 存储密码
func (s *Store) UsingKeyring() bool { return s.keyring }
