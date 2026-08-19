package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/zalando/go-keyring"
)

const keyringService = "simple-connect"

// Secrets 抽象密钥存取，支持系统 keyring 与本地加密文件两种后端
type Secrets interface {
	Get(key string) (string, bool, error)
	Set(key, value string) error
	Delete(key string) error
}

// keyringSecrets 使用系统 keyring（macOS Keychain / Linux Secret Service / Windows 凭据管理器）
type keyringSecrets struct{}

func (s keyringSecrets) Get(key string) (string, bool, error) {
	v, err := keyring.Get(keyringService, key)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}

func (s keyringSecrets) Set(key, value string) error {
	return keyring.Set(keyringService, key, value)
}

func (s keyringSecrets) Delete(key string) error {
	return keyring.Delete(keyringService, key)
}

// fileSecrets 本地文件兜底后端（权限 0600），在无系统 keyring 时使用。
// 每次 Get/Set/Delete 均从磁盘重读最新内容（不做内存缓存），
// 配合 store 层文件锁与原子写，保证多实例并发安全。
type fileSecrets struct {
	path string
}

func (s *fileSecrets) Get(key string) (string, bool, error) {
	data, err := s.load()
	if err != nil {
		return "", false, err
	}
	v, ok := data[key]
	return v, ok, nil
}

func (s *fileSecrets) Set(key, value string) error {
	data, err := s.load()
	if err != nil {
		return err
	}
	data[key] = value
	return s.save(data)
}

func (s *fileSecrets) Delete(key string) error {
	data, err := s.load()
	if err != nil {
		return err
	}
	delete(data, key)
	return s.save(data)
}

// load 从磁盘读取全部密钥（文件不存在返回空 map）
func (s *fileSecrets) load() (map[string]string, error) {
	b, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return map[string]string{}, nil
		}
		return nil, err
	}
	data := map[string]string{}
	if err := json.Unmarshal(b, &data); err != nil {
		return nil, err
	}
	return data, nil
}

// save 原子写回（临时文件 + rename），避免并发读看到半截文件
func (s *fileSecrets) save(data map[string]string) error {
	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// newSecrets 探测系统 keyring 是否可用，不可用则降级为本地文件
func newSecrets(dir string) Secrets {
	if _, err := keyring.Get(keyringService, "__probe__"); err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return &fileSecrets{path: filepath.Join(dir, "secrets.json")}
	}
	return keyringSecrets{}
}
