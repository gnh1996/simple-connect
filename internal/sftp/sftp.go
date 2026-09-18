package sftp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/pkg/sftp"

	"simple-connect/internal/model"
	sshc "simple-connect/internal/ssh"
)

// Conn 表示一个已建立的 SSH+SFTP 连接
type Conn struct {
	Client *sftp.Client
	SSH    *sshc.Client
}

// Dial 建立 SFTP 连接（认证走原始凭据，不合并 ~/.ssh/config）
func Dial(h *model.Host, password string, opts ...sshc.Option) (*Conn, error) {
	sshCl, err := sshc.ConnectRaw(h, password, opts...)
	if err != nil {
		return nil, err
	}
	sftpCl, err := sftp.NewClient(sshCl.Client, sftp.UseConcurrentWrites(true))
	if err != nil {
		_ = sshCl.Close()
		return nil, err
	}
	return &Conn{Client: sftpCl, SSH: sshCl}, nil
}

// NewConnFromSSH 复用已有 SSH 连接建立 SFTP 通道（会话挂起场景，免重新认证）。
// 返回的 Conn 不拥有底层连接：Close 只关闭 SFTP 通道，不影响 ssh.Client。
func NewConnFromSSH(sshCl *sshc.Client) (*Conn, error) {
	if sshCl == nil {
		return nil, fmt.Errorf("nil SSH client")
	}
	sftpCl, err := sftp.NewClient(sshCl.Client, sftp.UseConcurrentWrites(true))
	if err != nil {
		return nil, err
	}
	return &Conn{Client: sftpCl}, nil
}

// Close 关闭连接
func (c *Conn) Close() {
	if c.Client != nil {
		_ = c.Client.Close()
	}
	if c.SSH != nil {
		_ = c.SSH.Close()
	}
}

// SortEntries 按"目录优先、忽略大小写"排序文件列表（远程/本地通用）
func SortEntries(entries []os.FileInfo) {
	sort.Slice(entries, func(i, j int) bool {
		di, dj := entries[i].IsDir(), entries[j].IsDir()
		if di != dj {
			return di
		}
		return strings.ToLower(entries[i].Name()) < strings.ToLower(entries[j].Name())
	})
}

// List 读取目录内容并按"目录优先、忽略大小写"排序
func List(cl *sftp.Client, path string) ([]os.FileInfo, error) {
	entries, err := cl.ReadDir(path)
	if err != nil {
		return nil, err
	}
	SortEntries(entries)
	return entries, nil
}

// Remove 删除远程文件或目录（目录递归删除，非空目录也可用）
func Remove(cl *sftp.Client, p string, isDir bool) error {
	if !isDir {
		return cl.Remove(p)
	}
	// 先尝试直接删空目录，成功则返回
	if err := cl.RemoveDirectory(p); err == nil {
		return nil
	}
	// 非空目录：递归删除（先删文件，再从深到浅删目录）
	var dirs []string
	walker := cl.Walk(p)
	for walker.Step() {
		if walker.Err() != nil {
			return walker.Err()
		}
		wp := walker.Path()
		// Walk 包含根目录本身，递归时跳过根（最后单独删除）
		if wp == p {
			continue
		}
		if walker.Stat().IsDir() {
			dirs = append(dirs, wp)
		} else {
			if err := cl.Remove(wp); err != nil {
				return err
			}
		}
	}
	// 目录按深度倒序删除（先删子目录）
	for i := len(dirs) - 1; i >= 0; i-- {
		if err := cl.RemoveDirectory(dirs[i]); err != nil {
			return err
		}
	}
	return cl.RemoveDirectory(p)
}

// Transfer 传输进度跟踪器（并发安全）
type Transfer struct {
	mu       sync.Mutex
	Name     string
	Up       bool
	done     int64
	total    int64
	finished bool
	err      error

	// ctx/cancel 支持协作式取消：上层请求取消后，读写循环在下一块读取前退出，
	// 便于界面「退出时妥善终止」（不必只靠关闭连接硬中断），并让上层区分
	// 「用户取消」与「真实错误」。
	ctx      context.Context
	cancel   context.CancelFunc
	canceled bool
}

// NewTransfer 创建传输进度跟踪器
func NewTransfer(name string, up bool) *Transfer {
	ctx, cancel := context.WithCancel(context.Background())
	return &Transfer{Name: name, Up: up, ctx: ctx, cancel: cancel}
}

// Cancel 请求取消传输（协作式，幂等）。已完成或未启动的传输调用无副作用。
func (t *Transfer) Cancel() {
	t.mu.Lock()
	t.canceled = true
	cancel := t.cancel
	t.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// Canceled 返回传输是否被用户取消过。
func (t *Transfer) Canceled() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.canceled
}

// ctxErr 返回取消状态对应的错误（未取消时返回 nil）。
func (t *Transfer) ctxErr() error {
	if t.ctx == nil {
		return nil
	}
	select {
	case <-t.ctx.Done():
		return t.ctx.Err()
	default:
		return nil
	}
}

// Snapshot 返回当前进度快照
func (t *Transfer) Snapshot() (done, total int64, finished bool, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.done, t.total, t.finished, t.err
}

func (t *Transfer) setTotal(n int64) {
	t.mu.Lock()
	t.total = n
	t.mu.Unlock()
}

// Err 返回传输结果错误（finished 前通常为 nil）
func (t *Transfer) Err() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.err
}

func (t *Transfer) add(n int) {
	t.mu.Lock()
	t.done += int64(n)
	t.mu.Unlock()
}

func (t *Transfer) finish(err error) {
	t.mu.Lock()
	t.finished = true
	t.err = err
	t.mu.Unlock()
}

// Upload 将本地文件异步上传到远程路径（在调用方 goroutine 中执行）
func Upload(cl *sftp.Client, t *Transfer, localPath, remotePath string) {
	t.setTotal(statSizeLocal(localPath))
	uploadFile(cl, t, localPath, remotePath)
	t.finish(t.Err())
}

func uploadFile(cl *sftp.Client, t *Transfer, localPath, remotePath string) {
	f, err := os.Open(localPath)
	if err != nil {
		t.finish(err)
		return
	}
	defer func() { _ = f.Close() }() // 只读源文件，关闭错误不影响传输结果

	// 原子写入：先写临时文件，成功后再改名为目标，避免取消/失败留下半截目标文件。
	tmpPath := remotePath + partSuffix
	rf, err := cl.Create(tmpPath)
	if err != nil {
		t.finish(err)
		return
	}
	// 关键：不能再用 io.CopyBuffer(mw, f, bigBuf)——src 是 *os.File，实现了 io.WriterTo，
	// io.CopyBuffer 会直接调用 f.WriteTo 并忽略传入的 buffer（历史 bug：1MB 并发分片
	// 从未生效，落到 32KB 串行写，上传吞吐仅约下载的 1/4，详见 bufsize_bench_test.go）。
	// 改用 sftp.File.ReadFrom：其经 reader.Stat 推断文件大小后走并发分片写（需 UseConcurrentWrites）。
	// countingReader 透传 Stat() 给 ReadFrom 的并发判定，同时从源侧计数进度并响应取消。
	_, err = rf.ReadFrom(countingReader{r: f, t: t})
	cerr := rf.Close()
	if err == nil {
		err = cerr
	}
	if err != nil {
		_ = cl.Remove(tmpPath) // 清理临时文件（忽略清理失败）
		t.finish(err)
		return
	}
	if err := renameRemote(cl, tmpPath, remotePath); err != nil {
		_ = cl.Remove(tmpPath)
		t.finish(err)
	}
}

// partSuffix 传输临时文件后缀：写入完成后改名为目标，取消/失败时删除。
const partSuffix = ".part"

// renameRemote 将远程临时文件改名为目标路径，覆盖已存在目标。
// 优先用 posix-rename 扩展（OpenSSH 均支持，明确覆盖语义）；服务端不支持时
// 回退普通 Rename。回退失败时保留原目标与临时文件，交由上层报错，避免丢数据。
func renameRemote(cl *sftp.Client, oldPath, newPath string) error {
	if err := cl.PosixRename(oldPath, newPath); err == nil {
		return nil
	}
	return cl.Rename(oldPath, newPath)
}

// Download 将远程文件异步下载到本地路径（在调用方 goroutine 中执行）
func Download(cl *sftp.Client, t *Transfer, remotePath, localPath string) {
	t.setTotal(statSizeRemote(cl, remotePath))
	downloadFile(cl, t, remotePath, localPath)
	t.finish(t.Err())
}

func downloadFile(cl *sftp.Client, t *Transfer, remotePath, localPath string) {
	rf, err := cl.Open(remotePath)
	if err != nil {
		t.finish(err)
		return
	}
	defer func() { _ = rf.Close() }() // 只读远程源文件，关闭错误不影响传输结果

	// 原子写入：先写临时文件，成功后再改名为目标，避免取消/失败留下半截目标文件。
	tmpPath := localPath + partSuffix
	f, err := os.Create(tmpPath)
	if err != nil {
		t.finish(err)
		return
	}
	_, err = io.Copy(io.MultiWriter(f, countingWriter{t}), cancelReader{r: rf, t: t})
	cerr := f.Close()
	if err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(tmpPath)
		t.finish(err)
		return
	}
	if err := os.Rename(tmpPath, localPath); err != nil {
		_ = os.Remove(tmpPath)
		t.finish(err)
	}
}

// BatchItem 批量传输条目（Src 为源，Dst 为目标）
type BatchItem struct {
	Src string
	Dst string
}

// UploadPath 传输单个本地路径（文件或目录递归，进度汇总到 t）。
func UploadPath(cl *sftp.Client, t *Transfer, localPath, remotePath string) {
	total, err := dirSizeLocal(localPath)
	if err != nil {
		t.finish(err)
		return
	}
	t.setTotal(total)
	uploadItem(cl, t, localPath, remotePath)
	t.finish(t.Err())
}

// DownloadPath 传输单个远程路径（文件或目录递归，进度汇总到 t）。
func DownloadPath(cl *sftp.Client, t *Transfer, remotePath, localPath string) {
	total, err := dirSizeRemote(cl, remotePath)
	if err != nil {
		t.finish(err)
		return
	}
	t.setTotal(total)
	downloadItem(cl, t, remotePath, localPath)
	t.finish(t.Err())
}

// BatchTransfer 批量传输（文件或目录递归，进度汇总到 t）。
// 在调用方 goroutine 中执行；首个失败即中止。
func BatchTransfer(cl *sftp.Client, t *Transfer, up bool, items []BatchItem) {
	total, err := batchTotal(cl, up, items)
	if err != nil {
		t.finish(err)
		return
	}
	t.setTotal(total)
	for _, it := range items {
		if up {
			uploadItem(cl, t, it.Src, it.Dst)
		} else {
			downloadItem(cl, t, it.Src, it.Dst)
		}
		if t.Err() != nil {
			break
		}
	}
	t.finish(t.Err())
}

func batchTotal(cl *sftp.Client, up bool, items []BatchItem) (int64, error) {
	var total int64
	for _, it := range items {
		if up {
			n, err := dirSizeLocal(it.Src)
			if err != nil {
				return 0, err
			}
			total += n
		} else {
			n, err := dirSizeRemote(cl, it.Src)
			if err != nil {
				return 0, err
			}
			total += n
		}
	}
	return total, nil
}

// uploadItem 传输单个本地路径（文件或目录递归）
func uploadItem(cl *sftp.Client, t *Transfer, localPath, remotePath string) {
	st, err := os.Stat(localPath)
	if err != nil {
		t.finish(err)
		return
	}
	if !st.IsDir() {
		if err := cl.MkdirAll(path.Dir(remotePath)); err != nil {
			t.finish(err)
			return
		}
		uploadFile(cl, t, localPath, remotePath)
		return
	}
	err = filepath.WalkDir(localPath, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(localPath, p)
		if err != nil {
			return err
		}
		remote := remotePath
		if rel != "." {
			remote = path.Join(remotePath, filepath.ToSlash(rel))
		}
		if d.IsDir() {
			return cl.MkdirAll(remote)
		}
		if d.Type().IsRegular() {
			uploadFile(cl, t, p, remote)
		}
		return t.Err()
	})
	if err != nil {
		t.finish(err)
	}
}

// downloadItem 传输单个远程路径（文件或目录递归）
func downloadItem(cl *sftp.Client, t *Transfer, remotePath, localPath string) {
	st, err := cl.Stat(remotePath)
	if err != nil {
		t.finish(err)
		return
	}
	if !st.IsDir() {
		downloadFile(cl, t, remotePath, localPath)
		return
	}
	walker := cl.Walk(remotePath)
	for walker.Step() {
		if walker.Err() != nil {
			t.finish(walker.Err())
			return
		}
		p := walker.Path()
		rel, err := posixRel(remotePath, p)
		if err != nil {
			t.finish(err)
			return
		}
		local := localPath
		if rel != "." {
			local = filepath.Join(localPath, filepath.FromSlash(rel))
		}
		if walker.Stat().IsDir() {
			if err := os.MkdirAll(local, 0o755); err != nil {
				t.finish(err)
				return
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
			t.finish(err)
			return
		}
		downloadFile(cl, t, p, local)
		if t.Err() != nil {
			return
		}
	}
}

// statSizeLocal 单文件大小（目录时返回 0）
func statSizeLocal(p string) int64 {
	if st, err := os.Stat(p); err == nil && !st.IsDir() {
		return st.Size()
	}
	return 0
}

func statSizeRemote(cl *sftp.Client, p string) int64 {
	if st, err := cl.Stat(p); err == nil && !st.IsDir() {
		return st.Size()
	}
	return 0
}

// dirSizeLocal 递归统计本地路径字节数（目录含子目录内全部文件）
func dirSizeLocal(p string) (int64, error) {
	st, err := os.Stat(p)
	if err != nil {
		return 0, err
	}
	if !st.IsDir() {
		return st.Size(), nil
	}
	var total int64
	err = filepath.WalkDir(p, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			total += info.Size()
		}
		return nil
	})
	return total, err
}

// dirSizeRemote 递归统计远程路径字节数（目录含子目录内全部文件）
func dirSizeRemote(cl *sftp.Client, p string) (int64, error) {
	st, err := cl.Stat(p)
	if err != nil {
		return 0, err
	}
	if !st.IsDir() {
		return st.Size(), nil
	}
	var total int64
	walker := cl.Walk(p)
	for walker.Step() {
		if walker.Err() != nil {
			return 0, walker.Err()
		}
		if !walker.Stat().IsDir() {
			total += walker.Stat().Size()
		}
	}
	return total, nil
}

type countingWriter struct{ t *Transfer }

func (w countingWriter) Write(p []byte) (int, error) {
	w.t.add(len(p))
	return len(p), nil
}

// cancelReader 包裹读取端，在每次读取前检查取消状态，使下载可协作式取消。
type cancelReader struct {
	r io.Reader
	t *Transfer
}

func (c cancelReader) Read(p []byte) (int, error) {
	if err := c.t.ctxErr(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// countingReader 从源读取侧计数进度（供 uploadFile 的 ReadFrom 使用）。
// 必须透传 Stat()：sftp.File.ReadFrom 依赖 reader 的大小推断并发分片数，
// 若包装成普通 Reader 会丢失该信息，退化为串行 32KB 写（上传性能回退）。
type countingReader struct {
	r io.Reader
	t *Transfer
}

func (r countingReader) Read(p []byte) (int, error) {
	// 取消检查：返回错误让 sftp.File.ReadFrom 的并发写循环尽快收敛。
	if err := r.t.ctxErr(); err != nil {
		return 0, err
	}
	n, err := r.r.Read(p)
	if n > 0 {
		r.t.add(n)
	}
	return n, err
}

func (r countingReader) Stat() (os.FileInfo, error) {
	// 仅 *os.File 经过验证可在 sftp.File.ReadFrom 中触发并发分片（高吞吐）。
	// 其他 Reader 退化为串行 32KB 写；未来若需包装 Reader，应在此扩展对
	// interface{Stat() (os.FileInfo, error)} 的探测并确保返回有效大小，
	// 否则上传会静默性能回退（详见 bufsize_bench_test.go）。
	if f, ok := r.r.(*os.File); ok {
		return f.Stat()
	}
	// 尝试通用 Stat 接口（如自定义包装仍暴露底层 FileInfo）
	if st, ok := r.r.(interface{ Stat() (os.FileInfo, error) }); ok {
		return st.Stat()
	}
	return nil, os.ErrInvalid
}

// posixRel 计算 POSIX 路径的相对路径（不依赖 OS 路径分隔符，Windows 下也正确）。
// 语义对齐 path/filepath.Rel，但强制使用 "/" 分隔，避免 filepath 在 Windows 下按 "\" 切分。
func posixRel(base, target string) (string, error) {
	base = path.Clean(base)
	target = path.Clean(target)
	if base == target {
		return ".", nil
	}
	if base == "." {
		base = ""
	}
	if base == "/" {
		if strings.HasPrefix(target, "/") {
			return strings.TrimPrefix(target, "/"), nil
		}
		return target, nil
	}
	// 必须为目录前缀（base + "/"）才算子路径，避免 /foo 与 /foobar 误匹配
	if strings.HasPrefix(target, base+"/") {
		return strings.TrimPrefix(target, base+"/"), nil
	}
	// 非子路径（理论上 Walk 不会触发），兜底用 filepath.Rel
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return "", err
	}
	return filepath.ToSlash(rel), nil
}

// UploadConflicts 检测上传本地路径到远程路径是否会覆盖已存在文件。
// 若 localPath 为文件：检查 remotePath 是否已存在；
// 若为目录：递归检查目录内每个普通文件对应的远程路径是否已存在（目录本身已存在不算冲突，仅文件覆盖算）。
// 返回冲突的远程路径列表（可能为空），异常时返回 error。
func UploadConflicts(cl *sftp.Client, localPath, remotePath string) ([]string, error) {
	st, err := os.Stat(localPath)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() {
		if _, err := cl.Stat(remotePath); err == nil {
			return []string{remotePath}, nil
		} else if !isNotExist(err) {
			return nil, err
		}
		return nil, nil
	}
	// 目录：检查目标类型冲突（远程已存在且为文件）
	if rst, err := cl.Stat(remotePath); err == nil {
		if !rst.IsDir() {
			return []string{remotePath}, nil
		}
	} else if !isNotExist(err) {
		return nil, err
	}
	var conflicts []string
	err = filepath.WalkDir(localPath, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(localPath, p)
		if err != nil {
			return err
		}
		remote := path.Join(remotePath, filepath.ToSlash(rel))
		if _, err := cl.Stat(remote); err == nil {
			conflicts = append(conflicts, remote)
		} else if !isNotExist(err) {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return conflicts, nil
}

// DownloadConflicts 检测下载远程路径到本地路径是否会覆盖已存在文件。
func DownloadConflicts(cl *sftp.Client, remotePath, localPath string) ([]string, error) {
	st, err := cl.Stat(remotePath)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() {
		if _, err := os.Stat(localPath); err == nil {
			return []string{localPath}, nil
		} else if !os.IsNotExist(err) {
			return nil, err
		}
		return nil, nil
	}
	// 目录：检查目标类型冲突（本地已存在且为文件）
	if lst, err := os.Stat(localPath); err == nil {
		if !lst.IsDir() {
			return []string{localPath}, nil
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	var conflicts []string
	walker := cl.Walk(remotePath)
	for walker.Step() {
		if walker.Err() != nil {
			return nil, walker.Err()
		}
		if walker.Stat().IsDir() {
			continue
		}
		p := walker.Path()
		rel, err := posixRel(remotePath, p)
		if err != nil {
			return nil, err
		}
		local := localPath
		if rel != "." {
			local = filepath.Join(localPath, filepath.FromSlash(rel))
		}
		if _, err := os.Stat(local); err == nil {
			conflicts = append(conflicts, local)
		} else if !os.IsNotExist(err) {
			return nil, err
		}
	}
	return conflicts, nil
}

// BatchConflicts 批量检测覆盖冲突（up=true 为上传，false 为下载）
func BatchConflicts(cl *sftp.Client, up bool, items []BatchItem) ([]string, error) {
	var all []string
	for _, it := range items {
		var cur []string
		var err error
		if up {
			cur, err = UploadConflicts(cl, it.Src, it.Dst)
		} else {
			cur, err = DownloadConflicts(cl, it.Src, it.Dst)
		}
		if err != nil {
			return nil, err
		}
		all = append(all, cur...)
	}
	return all, nil
}

func isNotExist(err error) bool {
	if err == nil {
		return false
	}
	if os.IsNotExist(err) {
		return true
	}
	// pkg/sftp 返回 *StatusError，按 SFTP 状态码（SSH_FX_NO_SUCH_FILE）结构化判断，
	// 不依赖错误文案匹配（服务端实现/版本差异会导致文案不稳定）
	var se *sftp.StatusError
	if errors.As(err, &se) {
		return se.FxCode() == sftp.ErrSSHFxNoSuchFile
	}
	return false
}

// FormatSize 人类可读的文件大小
func FormatSize(n int64) string {
	if n < 0 {
		return "-"
	}
	const unit = 1024
	if n < unit {
		return strconv.FormatInt(n, 10) + " B"
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return strconv.FormatFloat(float64(n)/float64(div), 'f', 1, 64) + " " + "KMGTPE"[exp:exp+1] + "iB"
}
