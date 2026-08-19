//go:build !windows

package session

import (
	"io"
	"os"
	"testing"
	"time"
)

// TestPollInputDataAndStop 验证 poll 输入源：
// stdin 有数据时返回数据；detach（stop 关闭）时经自管道唤醒并返回 EOF。
func TestPollInputDataAndStop(t *testing.T) {
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer inR.Close()
	defer inW.Close()
	stopR, stopW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stopR.Close()
	defer stopW.Close()

	p := &pollInput{fd: int(inR.Fd()), stopR: stopR, stopW: stopW}
	stop := make(chan struct{})
	p.setStop(stop)

	// 数据就绪 → Read 返回数据
	if _, err := inW.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 8)
	n, err := p.Read(buf)
	if err != nil || n != 5 || string(buf[:n]) != "hello" {
		t.Fatalf("应读到 hello，实际 n=%d data=%q err=%v", n, buf[:n], err)
	}

	// detach：关闭 stop → 自管道唤醒 → Read 返回 EOF（干净退出）
	close(stop)
	deadline := time.After(2 * time.Second)
	for {
		_, err := p.Read(buf)
		if err == io.EOF {
			return
		}
		if err != nil {
			t.Fatalf("stop 后应返回 EOF，实际: %v", err)
		}
		select {
		case <-deadline:
			t.Fatal("stop 未唤醒阻塞的 Read")
		default:
		}
	}
}

// TestPollInputStdinEOF 验证 stdin 关闭（HUP）→ Read 返回 EOF
func TestPollInputStdinEOF(t *testing.T) {
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer inR.Close()
	stopR, stopW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stopR.Close()
	defer stopW.Close()

	p := &pollInput{fd: int(inR.Fd()), stopR: stopR, stopW: stopW}
	if err := inW.Close(); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 8)
	for {
		_, err := p.Read(buf)
		if err == io.EOF {
			return
		}
		if err != nil {
			t.Fatalf("stdin 关闭应返回 EOF，实际: %v", err)
		}
	}
}
