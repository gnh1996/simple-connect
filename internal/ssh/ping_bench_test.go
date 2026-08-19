package sshc

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"simple-connect/internal/model"
	"simple-connect/internal/testutil"
)

// BenchmarkCheckStatus 单主机在线状态检测：TCP 拨号 + SSH 协议头校验。
// 走内存测试服务器，反映最接近真实"探活"的往返成本。
func BenchmarkCheckStatus(b *testing.B) {
	env := testutil.StartSFTP(b)
	h, p := testutil.SplitHostPort(env.Addr)
	host := &model.Host{Name: "bench", Host: h, Port: p, User: "tester", Auth: model.AuthPassword}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if st := CheckStatus(host, 3*time.Second); st != StatusOnline {
			b.Fatalf("内存服务器应在线，实际 %v", st)
		}
	}
}

// BenchmarkStatusCheckBatch 批量并发探活（对齐列表页 runStatusChecks：
// 每主机一个 goroutine，全量并发）。50 个主机共享同一服务器。
func BenchmarkStatusCheckBatch(b *testing.B) {
	env := testutil.StartSFTP(b)
	h, p := testutil.SplitHostPort(env.Addr)
	const n = 50
	hosts := make([]*model.Host, n)
	for i := range hosts {
		hosts[i] = &model.Host{Name: fmt.Sprintf("host-%02d", i), Host: h, Port: p,
			User: "tester", Auth: model.AuthPassword}
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var wg sync.WaitGroup
		for _, h := range hosts {
			wg.Add(1)
			go func(h *model.Host) {
				defer wg.Done()
				_ = CheckStatus(h, 3*time.Second)
			}(h)
		}
		wg.Wait()
	}
}