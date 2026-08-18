package sftp

import (
	"io"
	"os"
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

// Dial 建立 SFTP 连接（底层复用 sshc.Connect 与 known_hosts 校验）
func Dial(h *model.Host, password string, opts ...sshc.Option) (*Conn, error) {
	sshCl, err := sshc.Connect(h, password, opts...)
	if err != nil {
		return nil, err
	}
	sftpCl, err := sftp.NewClient(sshCl.Client)
	if err != nil {
		_ = sshCl.Close()
		return nil, err
	}
	return &Conn{Client: sftpCl, SSH: sshCl}, nil
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

// List 读取目录内容并按“目录优先、忽略大小写”排序
func List(cl *sftp.Client, path string) ([]os.FileInfo, error) {
	entries, err := cl.ReadDir(path)
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool {
		di, dj := entries[i].IsDir(), entries[j].IsDir()
		if di != dj {
			return di
		}
		return strings.ToLower(entries[i].Name()) < strings.ToLower(entries[j].Name())
	})
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
	f, err := os.Open(localPath)
	if err != nil {
		t.finish(err)
		return
	}
	defer f.Close()
	if st, err := f.Stat(); err == nil {
		t.setTotal(st.Size())
	}

	rf, err := cl.Create(remotePath)
	if err != nil {
		t.finish(err)
		return
	}
	_, err = io.Copy(io.MultiWriter(rf, countingWriter{t}), f)
	cerr := rf.Close()
	if err == nil {
		err = cerr
	}
	t.finish(err)
}

// Download 将远程文件异步下载到本地路径（在调用方 goroutine 中执行）
func Download(cl *sftp.Client, t *Transfer, remotePath, localPath string) {
	rf, err := cl.Open(remotePath)
	if err != nil {
		t.finish(err)
		return
	}
	defer rf.Close()
	if st, err := rf.Stat(); err == nil {
		t.setTotal(st.Size())
	}

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
	t.finish(err)
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
