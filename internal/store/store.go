package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"simple-connect/internal/applog"
	"simple-connect/internal/model"
)

// Options 允许测试/主程序覆盖默认目录与密钥路径。
type Options struct {
	// Dir 覆盖配置目录（测试用；空表示 os.UserConfigDir()/simple-connect）。
	Dir string
	// KeyFile 覆盖本次运行的密钥路径（命令行 --key-file；不写回 config.json）。
	KeyFile string
}

// VaultInfo 供 TUI 展示密码库状态与路径。
type VaultInfo struct {
	VaultPath      string
	KeyPath        string
	DefaultKeyPath string
	KeyInConfigDir bool
	Ready          bool
	Problem        string
	KeyPermWarning string
}

// Store 负责连接配置与加密密码本的持久化。
// 多实例并发安全：hosts.json 与 vault.age 分别用文件锁串行化读-改-写，
// 原子写回；密码本只有一个数据文件，密钥文件只保存 age identity。
// 进程内并发安全：内存中的 hosts 快照用 RWMutex 保护。
type Store struct {
	dir            string
	path           string
	lockPath       string
	vaultPath      string
	configPath     string
	defaultKeyPath string

	mu    sync.RWMutex
	hosts []*model.Host

	infoMu         sync.RWMutex
	keyPath        string
	keyInConfigDir bool
	vault          *Vault
	vaultReady     bool
	vaultProblem   string
	keyPermWarning string
}

// Load 从用户配置目录加载存储。
func Load() (*Store, error) {
	return LoadWithOptions(Options{})
}

// LoadWithOptions 加载存储并初始化/解密密码本。
// 密码本问题不会阻塞 hosts 配置加载：Store 会以 !VaultReady 状态返回，
// 由 TUI 的“密码库”页让用户修改密钥路径或重置。只有配置目录/文件读取
// 等无法继续的错误才直接返回 error。
func LoadWithOptions(opts Options) (*Store, error) {
	dir := opts.Dir
	if dir == "" {
		var err error
		dir, err = configDir()
		if err != nil {
			return nil, err
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	s := &Store{
		dir:            dir,
		path:           filepath.Join(dir, "hosts.json"),
		lockPath:       filepath.Join(dir, "hosts.lock"),
		vaultPath:      filepath.Join(dir, vaultFile),
		configPath:     filepath.Join(dir, configFile),
		defaultKeyPath: filepath.Join(dir, keyFile),
	}
	hosts, err := s.readHosts()
	if err != nil {
		return nil, err
	}
	s.setHosts(hosts)

	s.initVault(opts)
	if s.VaultReady() {
		if err := s.reconcilePasswordFlags(); err != nil {
			applog.Errorf("同步已保存密码标记失败: %v", err)
		}
	}
	return s, nil
}

// initVault 在 vault.lock 内完成密钥加载/生成、密码本解密/创建。
func (s *Store) initVault(opts Options) {
	s.setVaultInfoPaths(s.defaultKeyPath)
	cfg, cfgErr := readConfig(s.configPath)
	if cfgErr != nil && opts.KeyFile == "" {
		s.setVaultProblem(fmt.Errorf("读取 %s 失败: %w", s.configPath, cfgErr))
		return
	}
	if cfgErr != nil {
		applog.Errorf("配置文件损坏，忽略并继续使用显式 --key-file: %v", cfgErr)
		cfg = configData{}
	}

	keyPath := s.defaultKeyPath
	if p := resolveStorePath(s.dir, cfg.KeyPath); p != "" {
		keyPath = p
	}
	if p := resolveStorePath(s.dir, opts.KeyFile); p != "" {
		keyPath = p
	}
	s.setVaultInfoPaths(keyPath)

	l, err := acquireLock(filepath.Join(s.dir, vaultLock), false)
	if err != nil {
		s.setVaultProblem(err)
		return
	}
	defer l.release()

	vaultExists := fileExists(s.vaultPath)
	keyExists := fileExists(keyPath)

	v := newVault(s.vaultPath, filepath.Join(s.dir, vaultLock))
	s.setVaultObject(v)

	if vaultExists {
		if !keyExists {
			s.setVaultProblem(fmt.Errorf("%w: %s", ErrVaultKeyMissing, keyPath))
			return
		}
		id, err := readIdentity(keyPath)
		if err != nil {
			s.setVaultProblem(err)
			return
		}
		v.identity = id
		if _, err := v.loadLocked(); err != nil {
			s.setVaultProblem(err)
			return
		}
		s.markVaultReady(keyPath)
		return
	}

	// 密码本不存在：使用现有密钥，或仅在默认路径自动生成。
	if keyExists {
		id, err := readIdentity(keyPath)
		if err != nil {
			s.setVaultProblem(err)
			return
		}
		v.identity = id
	} else {
		if !samePath(keyPath, s.defaultKeyPath) {
			s.setVaultProblem(fmt.Errorf("%w: %s", ErrVaultKeyMissing, keyPath))
			return
		}
		id, err := newIdentity()
		if err != nil {
			s.setVaultProblem(err)
			return
		}
		if err := writeIdentity(keyPath, id); err != nil {
			s.setVaultProblem(fmt.Errorf("生成密钥文件失败: %w", err))
			return
		}
		v.identity = id
	}

	if err := v.saveLocked(map[string]string{}); err != nil {
		s.setVaultProblem(fmt.Errorf("创建密码本失败: %w", err))
		return
	}
	// 写入后立即读回验证，确认密钥/格式正确。
	if _, err := v.loadLocked(); err != nil {
		s.setVaultProblem(fmt.Errorf("验证新密码本失败: %w", err))
		return
	}
	s.markVaultReady(keyPath)
}

func (s *Store) setVaultInfoPaths(keyPath string) {
	s.infoMu.Lock()
	s.keyPath = keyPath
	s.keyInConfigDir = pathWithin(s.dir, keyPath)
	s.infoMu.Unlock()
}

func (s *Store) setVaultObject(v *Vault) {
	s.infoMu.Lock()
	s.vault = v
	s.infoMu.Unlock()
}

func (s *Store) setVaultProblem(err error) {
	s.infoMu.Lock()
	s.vaultReady = false
	s.vaultProblem = err.Error()
	s.infoMu.Unlock()
	applog.Errorf("密码库不可用: %v", err)
}

func (s *Store) markVaultReady(keyPath string) {
	s.infoMu.Lock()
	s.vaultReady = true
	s.vaultProblem = ""
	s.keyPath = keyPath
	s.keyInConfigDir = pathWithin(s.dir, keyPath)
	s.keyPermWarning = keyPermissionWarning(keyPath)
	s.infoMu.Unlock()
}

// configDir 返回应用配置目录。
func configDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "simple-connect"), nil
}

// setHosts 原子替换内存快照（写路径统一走这里）。
func (s *Store) setHosts(hosts []*model.Host) {
	s.mu.Lock()
	s.hosts = hosts
	s.mu.Unlock()
}

// hostsSnapshot 返回内存快照的浅拷贝，供读取路径在锁外使用。
func (s *Store) hostsSnapshot() []*model.Host {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*model.Host, len(s.hosts))
	copy(out, s.hosts)
	return out
}

// withExclusiveLock 持有排他锁执行 fn（读-改-写操作互斥）。
func (s *Store) withExclusiveLock(fn func() error) error {
	l, err := acquireLock(s.lockPath, false)
	if err != nil {
		return err
	}
	defer l.release()
	return fn()
}

// withSharedLock 持有共享锁执行 fn（只读重载，可并发）。
func (s *Store) withSharedLock(fn func() error) error {
	l, err := acquireLock(s.lockPath, true)
	if err != nil {
		return err
	}
	defer l.release()
	return fn()
}

// readHosts 从磁盘读取最新连接列表（文件不存在返回空切片）。
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

// writeHosts 原子写回连接列表。
func (s *Store) writeHosts(hosts []*model.Host) error {
	b, err := json.MarshalIndent(hosts, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(s.path, b, 0o600)
}

// Hosts 返回按名称排序的连接列表。
func (s *Store) Hosts() []*model.Host {
	out := s.hostsSnapshot()
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
		s.setHosts(hosts)
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
		// HasPassword 是密码本的派生标记，不随表单快照整体覆盖：保留磁盘最新值，
		// 由 SetPassword 统一翻转，避免多实例下旧快照把已保存密码写回 false。
		h.HasPassword = hosts[idx].HasPassword
		hosts[idx] = h
		if err := s.writeHosts(hosts); err != nil {
			return err
		}
		s.setHosts(hosts)
		return nil
	})
}

// Delete 删除连接（含对应密码）。密码删除失败不阻塞主机删除，但会记录日志。
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
		if v := s.vaultRef(); v != nil && s.VaultReady() {
			if err := v.Delete(id); err != nil {
				applog.Errorf("删除连接密码失败（已删除主机）: %v", err)
			}
		}
		s.setHosts(out)
		return nil
	})
}

// Find 按 ID 查找连接。
func (s *Store) Find(id string) *model.Host {
	for _, e := range s.hostsSnapshot() {
		if e.ID == id {
			return e
		}
	}
	return nil
}

// Password 读取连接密码。
func (s *Store) Password(h *model.Host) (string, bool, error) {
	v := s.vaultRef()
	if v == nil || !s.VaultReady() {
		return "", false, ErrVaultNotReady
	}
	return v.Get(h.ID)
}

// SetPassword 保存/清除连接密码。锁内同步更新 hosts 与 vault，保持一致性。
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
		v := s.vaultRef()
		if v == nil || !s.VaultReady() {
			return ErrVaultNotReady
		}
		if pass == "" {
			if err := v.Delete(h.ID); err != nil {
				return err
			}
			hosts[idx].HasPassword = false
			h.HasPassword = false
		} else {
			if err := v.Set(h.ID, pass); err != nil {
				return err
			}
			hosts[idx].HasPassword = true
			h.HasPassword = true
		}
		if err := s.writeHosts(hosts); err != nil {
			return err
		}
		s.setHosts(hosts)
		return nil
	})
}

// Reload 从磁盘重新加载连接列表（共享锁，多实例间看到彼此的修改）。
func (s *Store) Reload() error {
	return s.withSharedLock(func() error {
		hosts, err := s.readHosts()
		if err != nil {
			return err
		}
		s.setHosts(hosts)
		return nil
	})
}

// VaultReady 密码本是否已成功加载。
func (s *Store) VaultReady() bool {
	s.infoMu.RLock()
	defer s.infoMu.RUnlock()
	return s.vaultReady
}

func (s *Store) vaultRef() *Vault {
	s.infoMu.RLock()
	defer s.infoMu.RUnlock()
	return s.vault
}

// VaultInfo 返回 TUI 展示所需的密码库信息快照。
func (s *Store) VaultInfo() VaultInfo {
	s.infoMu.RLock()
	defer s.infoMu.RUnlock()
	return VaultInfo{
		VaultPath:      s.vaultPath,
		KeyPath:        s.keyPath,
		DefaultKeyPath: s.defaultKeyPath,
		KeyInConfigDir: s.keyInConfigDir,
		Ready:          s.vaultReady,
		Problem:        s.vaultProblem,
		KeyPermWarning: s.keyPermWarning,
	}
}

// SetKeyPath 验证并切换密钥文件路径；仅当密码本存在时要求能成功解密。
// 成功后才写 config.json，避免把错误路径持久化。
func (s *Store) SetKeyPath(input string) error {
	path := resolveStorePath(s.dir, input)
	if path == "" {
		path = s.defaultKeyPath
	}
	id, err := readIdentity(path)
	if err != nil {
		return err
	}

	l, err := acquireLock(filepath.Join(s.dir, vaultLock), false)
	if err != nil {
		return err
	}
	v := newVault(s.vaultPath, filepath.Join(s.dir, vaultLock))
	v.identity = id
	vaultExists := fileExists(s.vaultPath)
	if vaultExists {
		if _, err := v.loadLocked(); err != nil {
			l.release()
			return err
		}
	}

	cfg := configData{}
	if !samePath(path, s.defaultKeyPath) {
		cfg.KeyPath = path
	}
	if err := writeConfig(s.configPath, cfg); err != nil {
		l.release()
		return fmt.Errorf("保存密钥路径失败: %w", err)
	}

	if !vaultExists {
		if err := v.saveLocked(map[string]string{}); err != nil {
			l.release()
			return fmt.Errorf("创建密码本失败: %w", err)
		}
	}
	l.release()

	s.infoMu.Lock()
	s.vault = v
	s.vaultReady = true
	s.vaultProblem = ""
	s.keyPath = path
	s.keyInConfigDir = pathWithin(s.dir, path)
	s.keyPermWarning = keyPermissionWarning(path)
	s.infoMu.Unlock()

	if err := s.reconcilePasswordFlags(); err != nil {
		applog.Errorf("同步已保存密码标记失败: %v", err)
	}
	return nil
}

// ResetVault 作废当前密码本并生成新密钥对/空密码本。
// 这是破坏性操作，不尝试恢复旧密码；返回 warning 表示部分旧文件未能清理。
func (s *Store) ResetVault() (string, error) {
	var warning string
	err := s.withExclusiveLock(func() error {
		s.infoMu.RLock()
		oldKeyPath := s.keyPath
		s.infoMu.RUnlock()

		l, err := acquireLock(filepath.Join(s.dir, vaultLock), false)
		if err != nil {
			return err
		}
		defer l.release()

		// 记录密码本是否存在，供旧密钥校验与删除分支使用。
		vaultExists := fileExists(s.vaultPath)

		// 旧密钥：先验证（不删除），等密码本删除成功后再删，避免删了 key
		// 却因 vault 删除失败而留下无法解密的密码本。
		deleteOldKey := false
		if oldKeyPath != "" {
			if st, statErr := os.Stat(oldKeyPath); statErr == nil && !st.IsDir() {
				id, keyErr := readIdentity(oldKeyPath)
				switch {
				case keyErr != nil:
					if samePath(oldKeyPath, s.defaultKeyPath) {
						deleteOldKey = true // 默认路径由应用所有，重置时覆盖
					} else {
						warning = appendWarning(warning, fmt.Sprintf("旧密钥不是有效 age 密钥，未删除: %s", oldKeyPath))
					}
				case vaultExists && !samePath(oldKeyPath, s.defaultKeyPath) && !identityDecryptsVault(s.vaultPath, id):
					warning = appendWarning(warning, fmt.Sprintf("旧密钥与当前密码本不匹配，未删除: %s", oldKeyPath))
				default:
					deleteOldKey = true
				}
			} else if errors.Is(statErr, os.ErrNotExist) && !samePath(oldKeyPath, s.defaultKeyPath) {
				warning = appendWarning(warning, fmt.Sprintf("旧密钥文件不存在，未能删除（如 U 盘未插入请手动处理）: %s", oldKeyPath))
			}
		}

		for _, p := range []string{s.vaultPath, s.vaultPath + ".bak"} {
			if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("删除密码本失败: %w", err)
			}
		}
		if deleteOldKey {
			if err := os.Remove(oldKeyPath); err != nil {
				warning = appendWarning(warning, fmt.Sprintf("删除旧密钥失败: %v", err))
			}
		}

		// 旧版明文文件/锁文件不在新版管理范围内，不自动删除（避免误删用户数据）。
		id, err := newIdentity()
		if err != nil {
			return err
		}
		if err := writeIdentity(s.defaultKeyPath, id); err != nil {
			return fmt.Errorf("生成新密钥失败: %w", err)
		}
		v := newVault(s.vaultPath, filepath.Join(s.dir, vaultLock))
		v.identity = id
		if err := v.saveLocked(map[string]string{}); err != nil {
			return fmt.Errorf("创建新密码本失败: %w", err)
		}
		// 新密钥在默认路径，清空自定义路径配置。
		if err := writeConfig(s.configPath, configData{}); err != nil {
			return fmt.Errorf("清空密钥路径配置失败: %w", err)
		}

		hosts, err := s.readHosts()
		if err != nil {
			return err
		}
		for _, h := range hosts {
			h.HasPassword = false
		}
		if err := s.writeHosts(hosts); err != nil {
			return fmt.Errorf("清除已保存密码标记失败: %w", err)
		}
		s.setHosts(hosts)

		s.infoMu.Lock()
		s.vault = v
		s.vaultReady = true
		s.vaultProblem = ""
		s.keyPath = s.defaultKeyPath
		s.keyInConfigDir = true
		s.keyPermWarning = keyPermissionWarning(s.defaultKeyPath)
		s.infoMu.Unlock()
		return nil
	})
	if err == nil && warning != "" {
		applog.Infof("重置密码库: %s", warning)
	}
	return warning, err
}

func appendWarning(cur, add string) string {
	if cur == "" {
		return add
	}
	return cur + "；" + add
}

// reconcilePasswordFlags 以密码本为准修复 hosts.json 的 HasPassword 标记。
// 顺序为 hosts 锁 → vault 锁，与 SetPassword/Delete 保持一致。
func (s *Store) reconcilePasswordFlags() error {
	v := s.vaultRef()
	if v == nil || !s.VaultReady() {
		return nil
	}
	return s.withExclusiveLock(func() error {
		hosts, err := s.readHosts()
		if err != nil {
			return err
		}
		data, err := v.Snapshot()
		if err != nil {
			return err
		}
		changed := false
		for _, h := range hosts {
			_, ok := data[h.ID]
			if h.HasPassword != ok {
				h.HasPassword = ok
				changed = true
			}
		}
		if !changed {
			return nil
		}
		if err := s.writeHosts(hosts); err != nil {
			return err
		}
		s.setHosts(hosts)
		return nil
	})
}
