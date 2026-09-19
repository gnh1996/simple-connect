package sftp

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"simple-connect/internal/testutil"
)

// plainReader 仅暴露 Read 的包装器，用于对照「源被包装后丢失 io.WriterTo →
// 下载退回 32KB 串行读」的历史实现（对应修复前 downloadFile 里的 cancelReader）。
type plainReader struct{ r io.Reader }

func (p plainReader) Read(b []byte) (int, error) { return p.r.Read(b) }

// BenchmarkDownloadBufSize 下载路径的回归基准，对比三种写法：
//
// 历史实现：`io.Copy(io.MultiWriter(f, countingWriter{t}), cancelReader{rf, t})`——
// cancelReader 只暴露 Read，挡住 *sftp.File 的 io.WriterTo，io.Copy 落入 32KB
// 串行读；改为 `rf.WriteTo(progressWriter{f, t})` 后走 pkg/sftp 默认并发读
// （UseConcurrentReads 默认开启），进度计数与取消检查在写侧完成。
//
// 观察点：生产 Download 应与裸 WriteTo 吞吐相当（明显快于旧 Reader 包装参照），
// 若持平则说明再度被包装丢掉了并发读路径。
func BenchmarkDownloadBufSize(b *testing.B) {
	env := testutil.StartSFTP(b)

	content := make([]byte, 8<<20)
	for i := range content {
		content[i] = byte(i * 17)
	}
	remote := filepath.Join(env.Root, "down.bin")
	if err := os.WriteFile(remote, content, 0o644); err != nil {
		b.Fatal(err)
	}
	b.SetBytes(int64(len(content)))

	conn := benchDial(b, env) // 生产连接（并发读默认开启）

	// ① 旧 Reader 包装参照：并发读被包装挡住，退回串行 32KB
	b.Run("旧Reader包装参照", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			rf, err := conn.Client.Open(remote)
			if err != nil {
				b.Fatal(err)
			}
			f, err := os.CreateTemp(b.TempDir(), "old-*.bin")
			if err != nil {
				b.Fatal(err)
			}
			t := NewTransfer("down.bin", false)
			_, err = io.Copy(io.MultiWriter(f, countingWriter{t}), plainReader{rf})
			cerr := f.Close()
			_ = rf.Close()
			if err != nil {
				b.Fatal(err)
			}
			if cerr != nil {
				b.Fatal(cerr)
			}
		}
	})

	// ② 修复后的生产 Download 路径（rf.WriteTo + progressWriter 计数/取消）
	dst := filepath.Join(b.TempDir(), "down-prod.bin")
	b.Run("生产Download修复后", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			t := NewTransfer("down.bin", false)
			Download(conn.Client, t, remote, dst)
			if _, _, finished, err := t.Snapshot(); !finished || err != nil {
				b.Fatal(err)
			}
		}
	})

	// ③ 裸 WriteTo 参照（无计数包装，上限参考）
	b.Run("裸WriteTo参照", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			rf, err := conn.Client.Open(remote)
			if err != nil {
				b.Fatal(err)
			}
			f, err := os.CreateTemp(b.TempDir(), "raw-*.bin")
			if err != nil {
				b.Fatal(err)
			}
			_, err = rf.WriteTo(f)
			cerr := f.Close()
			_ = rf.Close()
			if err != nil {
				b.Fatal(err)
			}
			if cerr != nil {
				b.Fatal(cerr)
			}
		}
	})
}

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
				_ = f.Close()
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
			_ = f.Close()
			if err != nil {
				b.Fatal(err)
			}
			if cerr != nil {
				b.Fatal(cerr)
			}
		}
	})
}
