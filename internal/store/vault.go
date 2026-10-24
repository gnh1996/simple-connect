package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"filippo.io/age"

	"simple-connect/internal/applog"
)

const (
	vaultVersion = 1
	vaultFile    = "vault.age"
	keyFile      = "vault.key"
	vaultLock    = "vault.lock"
	configFile   = "config.json"
)

var (
	// ErrVaultNotFound 密码本文件不存在。
	ErrVaultNotFound = errors.New("密码本不存在")
	// ErrVaultKeyMissing 密码本存在但密钥文件不存在。
	ErrVaultKeyMissing = errors.New("密码本存在，但密钥文件不存在")
	// ErrVaultKeyInvalid 密钥文件内容不是有效的 age X25519 identity。
	ErrVaultKeyInvalid = errors.New("密钥文件无效")
	// ErrVaultKeyMismatch 密钥无法解开密码本（密钥不匹配或文件损坏）。
	ErrVaultKeyMismatch = errors.New("密钥与密码本不匹配或密码本已损坏")
	// ErrVaultNotReady 密码本尚未成功加载。
	ErrVaultNotReady = errors.New("密码库未解锁")
)

// vaultData 是加密写入密码本的内层结构，带版本号便于以后升级格式。
type vaultData struct {
	Version int               `json:"version"`
	Secrets map[string]string `json:"secrets"`
}

// Vault 是单一加密密码本。数据始终写在 vault.age 中，用 age X25519 加密；
// 密钥文件只保存 identity，与数据文件解耦，可移动到其他路径。
type Vault struct {
	path     string
	lockPath string
	identity *age.X25519Identity
}

func newVault(path, lockPath string) *Vault {
	return &Vault{path: path, lockPath: lockPath}
}

func (v *Vault) loadLocked() (map[string]string, error) {
	b, err := os.ReadFile(v.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrVaultNotFound
		}
		return nil, err
	}
	if v.identity == nil {
		return nil, ErrVaultKeyMissing
	}
	r, err := age.Decrypt(bytes.NewReader(b), v.identity)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrVaultKeyMismatch, err)
	}
	plain, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrVaultKeyMismatch, err)
	}
	var d vaultData
	if err := json.Unmarshal(plain, &d); err != nil {
		return nil, fmt.Errorf("密码本内容损坏: %w", err)
	}
	if d.Version != vaultVersion {
		return nil, fmt.Errorf("不支持的密码本版本: %d", d.Version)
	}
	if d.Secrets == nil {
		d.Secrets = map[string]string{}
	}
	return d.Secrets, nil
}

func (v *Vault) saveLocked(data map[string]string) error {
	if v.identity == nil {
		return ErrVaultKeyMissing
	}
	if data == nil {
		data = map[string]string{}
	}
	plain, err := json.Marshal(vaultData{Version: vaultVersion, Secrets: data})
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	w, err := age.Encrypt(&buf, v.identity.Recipient())
	if err != nil {
		return err
	}
	if _, err := w.Write(plain); err != nil {
		_ = w.Close()
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	backupFile(v.path)
	return writeFileAtomic(v.path, buf.Bytes(), 0o600)
}

// Snapshot 返回密码本当前内容（共享锁，读取最新磁盘状态）。
func (v *Vault) Snapshot() (map[string]string, error) {
	l, err := acquireLock(v.lockPath, true)
	if err != nil {
		return nil, err
	}
	defer l.release()
	return v.loadLocked()
}

// Get 读取单个密码。
func (v *Vault) Get(key string) (string, bool, error) {
	data, err := v.Snapshot()
	if err != nil {
		return "", false, err
	}
	val, ok := data[key]
	return val, ok, nil
}

// Set 保存/覆盖单个密码。
func (v *Vault) Set(key, value string) error {
	l, err := acquireLock(v.lockPath, false)
	if err != nil {
		return err
	}
	defer l.release()
	data, err := v.loadLocked()
	if err != nil {
		return err
	}
	data[key] = value
	return v.saveLocked(data)
}

// Delete 删除单个密码（不存在视为成功）。
// 删除后同时移除 vault.age.bak，避免已删除的密码残留在上一份备份中。
func (v *Vault) Delete(key string) error {
	l, err := acquireLock(v.lockPath, false)
	if err != nil {
		return err
	}
	defer l.release()
	data, err := v.loadLocked()
	if err != nil {
		return err
	}
	if _, ok := data[key]; !ok {
		return nil
	}
	delete(data, key)
	if err := v.saveLocked(data); err != nil {
		return err
	}
	if err := os.Remove(v.path + ".bak"); err != nil && !errors.Is(err, os.ErrNotExist) {
		applog.Errorf("清理密码库备份失败: %v", err)
	}
	return nil
}

func newIdentity() (*age.X25519Identity, error) {
	return age.GenerateX25519Identity()
}

// identityDecryptsVault 供重置时确认旧密钥确实属于当前密码本。
// 调用方需持有 vault.lock（ResetVault 已持有）。
func identityDecryptsVault(path string, id *age.X25519Identity) bool {
	if id == nil {
		return false
	}
	v := &Vault{path: path, identity: id}
	_, err := v.loadLocked()
	return err == nil
}

// readIdentity 读取 age X25519 identity 文件。
func readIdentity(path string) (*age.X25519Identity, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrVaultKeyMissing
		}
		return nil, err
	}
	id, err := age.ParseX25519Identity(strings.TrimSpace(string(b)))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrVaultKeyInvalid, err)
	}
	return id, nil
}

func writeIdentity(path string, id *age.X25519Identity) error {
	if id == nil {
		return ErrVaultKeyMissing
	}
	return writeFileAtomic(path, []byte(id.String()+"\n"), 0o600)
}

func keyPermissionWarning(path string) string {
	if runtime.GOOS == "windows" || path == "" {
		return ""
	}
	st, err := os.Stat(path)
	if err != nil {
		return ""
	}
	if st.Mode().Perm()&0o077 != 0 {
		return fmt.Sprintf("密钥文件权限为 %04o，建议 chmod 600: %s", st.Mode().Perm(), path)
	}
	return ""
}

// backupFile 尽力保留上一份密文；失败不阻塞主写入（日志留痕）。
func backupFile(path string) {
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	if err := writeFileAtomic(path+".bak", b, 0o600); err != nil {
		applog.Errorf("写入密码库备份失败: %v", err)
	}
}

// writeFileAtomic 同目录临时文件 + fsync + rename，避免半截文件。
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }()
	if err := f.Chmod(perm); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	// rename 后补一次 chmod，确保已存在的旧文件权限也被收紧（Unix 生效）。
	_ = os.Chmod(path, perm)
	return nil
}

// expandHome 支持 ~ / ~/ 路径（Windows 同样按用户主目录展开）。
func expandHome(p string) string {
	p = strings.TrimSpace(p)
	if p == "" || p[0] != '~' {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		return filepath.Join(home, p[2:])
	}
	return p
}

// resolveStorePath 将用户输入解析为绝对路径；相对路径按配置目录解析。
func resolveStorePath(dir, p string) string {
	p = expandHome(p)
	if p == "" {
		return ""
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(dir, p)
	}
	return filepath.Clean(p)
}

// pathWithin 判断 path 是否位于 dir 内（含 dir 本身）。
func pathWithin(dir, path string) bool {
	if dir == "" || path == "" {
		return false
	}
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func fileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

// samePath 跨平台比较同一路径（Windows 盘符/大小写不敏感）。
func samePath(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// configData 是 config.json 的内容，目前只保存密钥路径（非秘密）。
type configData struct {
	KeyPath string `json:"key_path,omitempty"`
}

func readConfig(path string) (configData, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return configData{}, nil
		}
		return configData{}, err
	}
	var c configData
	if err := json.Unmarshal(b, &c); err != nil {
		return configData{}, err
	}
	return c, nil
}

func writeConfig(path string, c configData) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, b, 0o600)
}
