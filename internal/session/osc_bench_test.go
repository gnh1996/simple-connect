package session

import (
	"bytes"
	"fmt"
	"io"
	"testing"
)

// BenchmarkOSCTrackerPlain 纯文本透传吞吐（无 ESC 序列的快速路径）。
// 衡量登录横幅/大段输出时的字节透传 + 扫描开销。
func BenchmarkOSCTrackerPlain(b *testing.B) {
	data := bytes.Repeat([]byte("plain terminal output line\n"), 2048)
	tr := newOSCTracker(io.Discard)

	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := tr.Write(data); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkOSCTrackerWithOSC 高频 cwd 上报流（模拟 bash PROMPT_COMMAND 每提示符
// 上报一次 OSC 133;cwd）。衡量 scan 的定位 + 剔除 + 透传成本。
func BenchmarkOSCTrackerWithOSC(b *testing.B) {
	var chunk []byte
	for i := 0; i < 50; i++ {
		chunk = append(chunk,
			[]byte(fmt.Sprintf("root@host:%d# \x1b]133;cwd=/srv/app/%d\x07ls -la\r\n", i, i))...)
	}
	b.SetBytes(int64(len(chunk)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tr := newOSCTracker(io.Discard)
		if _, err := tr.Write(chunk); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkOSCTrackerBoundary 真实网络分片：小 Write 且 OSC 序列跨边界拆分，
// 衡量跨 Write 残留合并（t.buf 拷贝 + 重新扫描）的开销。
func BenchmarkOSCTrackerBoundary(b *testing.B) {
	payload := []byte("host:~$ ls -la\r\n\x1b]133;cwd=/home/user\x07drwxr-xr-x  1 user user 4096 Aug 19 10:00 .\r\nhost:~$ echo hi\r\nhi\r\n")

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tr := newOSCTracker(io.Discard)
		for off := 0; off < len(payload); off += 7 {
			end := off + 7
			if end > len(payload) {
				end = len(payload)
			}
			if _, err := tr.Write(payload[off:end]); err != nil {
				b.Fatal(err)
			}
		}
	}
}

// BenchmarkDetachScanner 输入泵热键扫描吞吐（512B 块，无 Ctrl+X 命中）。
// 对应"粘贴吞吐"已知限制的扫描侧成本（限制主因是 10ms 轮询 + 512B 读缓冲）。
func BenchmarkDetachScanner(b *testing.B) {
	payload := make([]byte, 512)
	for i := range payload {
		payload[i] = byte('a' + i%26)
	}
	b.SetBytes(int64(len(payload)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sc := detachScanner{}
		for j := 0; j < 16; j++ {
			fwd, hit := sc.feed(payload)
			if hit {
				b.Fatal("不应命中 detach")
			}
			if len(fwd) != len(payload) {
				b.Fatal("应原样转发全部字节")
			}
		}
	}
}