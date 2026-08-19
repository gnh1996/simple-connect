package sftp

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"simple-connect/internal/model"
	sshc "simple-connect/internal/ssh"
	"simple-connect/internal/testutil"
)

// benchSFTPClient 构建 sftp 客户端（并发写开关可控）
func benchSFTPClient(b *testing.B, env testutil.SFTPEnv, concurrent bool) *sftp.Client {
	b.Helper()
	h, p := testutil.SplitHostPort(env.Addr)
	host := &model.Host{Name: "bench", Host: h, Port: p, User: "tester", Auth: model.AuthPassword}
	sshCl, err := sshc.ConnectRaw(host, "secret", sshc.WithHostKeyCallback(ssh.InsecureIgnoreHostKey()))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = sshCl.Close() })

	opts := []sftp.ClientOption{}
	if concurrent {
		opts = append(opts, sftp.UseConcurrentWrites(true))
	}
	cl, err := sftp.NewClient(sshCl.Client, opts...)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = cl.Close() })
	return cl
}

// BenchmarkUploadBufSize 上传路径的回归基准，对比三种写法：
//
// 历史 bug：`io.CopyBuffer(mw, f, buf)` 的 src 是 *os.File，它实现了 io.WriterTo，
// 于是 io.CopyBuffer 直接调用 f.WriteTo(dst) 并忽略传入的 buf；os.File.WriteTo 在
// dst 非 *os.File（这里是 MultiWriter）时走 genericWriteTo 固定 32KB 回退缓冲，
// 导致 sftp.File.Write 每次只收到 32KB（并发分片=2），上传吞吐仅约下载的 1/4。
//
// 修复：uploadFile 改用 rf.ReadFrom(countingReader{f, t})——sftp.File.ReadFrom 经
// reader.Stat 推断文件大小后走并发分片写（需 UseConcurrentWrites）。
//
// 观察点：① CopyBuffer 参照扫各 buffer 大小应全部持平（证明旧路径 buffer 被忽略）；
// ② 修复后的生产 Upload 应与裸 ReadFrom 吞吐相当（数倍于 CopyBuffer 参照），
// 若持平则说明并发写路径又失效了，需排查计数包装是否丢掉了 Stat。
func BenchmarkUploadBufSize(b *testing.B) {
	env := testutil.StartSFTP(b)

	content := make([]byte, 8<<20)
	for i := range content {
		content[i] = byte(i * 31)
	}
	local := filepath.Join(b.TempDir(), "up.bin")
	if err := os.WriteFile(local, content, 0o644); err != nil {
		b.Fatal(err)
	}
	b.SetBytes(int64(len(content)))

	conn := benchDial(b, env) // 生产连接：UseConcurrentWrites(true)

	// ① 旧 CopyBuffer 参照：buffer 大小扫描，验证全部持平（历史 bug 的写照，勿回退）
	sizes := []int{32 << 10, 128 << 10, 1 << 20, 4 << 20}
	for _, sz := range sizes {
		sz := sz
		b.Run(fmt.Sprintf("旧CopyBuffer参照/buf=%dK", sz>>10), func(b *testing.B) {
			remote := filepath.Join(env.Root, fmt.Sprintf("up-copy-%d.bin", sz))
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				f, err := os.Open(local)
				if err != nil {
					b.Fatal(err)
				}
				rf, err := conn.Client.Create(remote)
				if err != nil {
					b.Fatal(err)
				}
				t := NewTransfer("up", true)
				_, err = io.CopyBuffer(io.MultiWriter(rf, countingWriter{t}), f, make([]byte, sz))
				cerr := rf.Close()
				f.Close()
				if err != nil {
					b.Fatal(err)
				}
				if cerr != nil {
					b.Fatal(cerr)
				}
			}
		})
	}

	// ② 修复后的生产 Upload 路径（走 sftp.File.ReadFrom + countingReader 计数）
	remote := filepath.Join(env.Root, "up-prod.bin")
	b.Run("生产Upload修复后", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			t := NewTransfer("up.bin", true)
			Upload(conn.Client, t, local, remote)
			if _, _, finished, err := t.Snapshot(); !finished || err != nil {
				b.Fatal(err)
			}
		}
	})

	// ③ 裸 rf.ReadFrom(f) 参照（无计数包装，上限参考）
	remote = filepath.Join(env.Root, "up-raw.bin")
	b.Run("裸ReadFrom参照", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			f, err := os.Open(local)
			if err != nil {
				b.Fatal(err)
			}
			rf, err := conn.Client.Create(remote)
			if err != nil {
				b.Fatal(err)
			}
			_, err = rf.ReadFrom(f)
			cerr := rf.Close()
			f.Close()
			if err != nil {
				b.Fatal(err)
			}
			if cerr != nil {
				b.Fatal(cerr)
			}
		}
	})
}