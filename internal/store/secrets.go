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

// fileSecrets 本地文件兜底后端（权限 0600），在无系统 keyring 时使用
type fileSecrets struct {
	path string
	data map[string]string
}

func (s *fileSecrets) Get(key string) (string, bool, error) {
	if s.data == nil {
		if err := s.load(); err != nil {
			return "", false, err
		}
	}
	v, ok := s.data[key]
	return v, ok, nil
}

func (s *fileSecrets) Set(key, value string) error {
	if s.data == nil {
		if err := s.load(); err != nil {
			s.data = map[string]string{}
		}
	}
	s.data[key] = value
	return s.save()
}

func (s *fileSecrets) Delete(key string) error {
	if s.data == nil {
		if err := s.load(); err != nil {
			return nil
		}
	}
	delete(s.data, key)
	return s.save()
}

func (s *fileSecrets) load() error {
	b, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			s.data = map[string]string{}
			return nil
		}
		return err
	}
	s.data = map[string]string{}
	_ = json.Unmarshal(b, &s.data)
	return nil
}

func (s *fileSecrets) save() error {
	b, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(s.path, b, 0o600)
}

// newSecrets 探测系统 keyring 是否可用，不可用则降级为本地文件
func newSecrets(dir string) Secrets {
	if _, err := keyring.Get(keyringService, "__probe__"); err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return &fileSecrets{path: filepath.Join(dir, "secrets.json")}
	}
	return keyringSecrets{}
}
