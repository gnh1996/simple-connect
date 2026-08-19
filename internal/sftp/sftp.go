package sftp

import (
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

// Remove 删除远程文件或目录
func Remove(cl *sftp.Client, path string, isDir bool) error {
	if isDir {
		return cl.RemoveDirectory(path)
	}
	return cl.Remove(path)
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
}

// NewTransfer 创建传输进度跟踪器
func NewTransfer(name string, up bool) *Transfer {
	return &Transfer{Name: name, Up: up}
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

// transferBufSize 上传/下载的拷贝 buffer 大小。sftp.File.Write 在收到超过
// maxPacket(32KB) 的 buffer 时才会拆分为并发分片（配合 UseConcurrentWrites），
// 1MB 可让单次 Write 触发 32 路并发，显著提升高延迟链路吞吐（RTT×32KB 不再是上限）。
const transferBufSize = 1 << 20

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
	defer f.Close()

	rf, err := cl.Create(remotePath)
	if err != nil {
		t.finish(err)
		return
	}
	_, err = io.CopyBuffer(io.MultiWriter(rf, countingWriter{t}), f, make([]byte, transferBufSize))
	cerr := rf.Close()
	if err == nil {
		err = cerr
	}
	if err != nil {
		t.finish(err)
	}
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
	defer rf.Close()

	f, err := os.Create(localPath)
	if err != nil {
		t.finish(err)
		return
	}
	_, err = io.Copy(io.MultiWriter(f, countingWriter{t}), rf)
	cerr := f.Close()
	if err == nil {
		err = cerr
	}
	if err != nil {
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
		rel, err := filepath.Rel(filepath.FromSlash(remotePath), filepath.FromSlash(p))
		if err != nil {
			t.finish(err)
			return
		}
		local := localPath
		if rel != "." {
			local = filepath.Join(localPath, rel)
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
