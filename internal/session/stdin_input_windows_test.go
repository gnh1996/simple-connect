//go:build windows

package session

import (
	"errors"
	"io"
	"os"
	"testing"
	"time"
)

// TestCancelableStdinStopUnblocksRead 回归 Windows 吞键根因：
// setStop 必须唤醒阻塞在 stdin 的读取，Read 返回 io.EOF（而非挂住或把取消错误
// 当作输入结束）。
func TestCancelableStdinStopUnblocksRead(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()

	in := newCancelableInput(r)
	stop := make(chan struct{})
	in.setStop(stop)

	readErr := make(chan error, 1)
	go func() {
		buf := make([]byte, 16)
		_, err := in.Read(buf)
		readErr <- err
	}()

	waitForInRead(t, in) // 确保读取已进入阻塞状态
	close(stop)

	select {
	case err := <-readErr:
		if !errors.Is(err, io.EOF) {
			t.Fatalf("取消后的读取应返回 io.EOF，实际: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("setStop 未能唤醒阻塞的 stdin 读取（会吞掉回 TUI 的首个按键）")
	}

	// 取消只影响本次读取：句柄仍可继续读取（Resume 复用同一 stdin）
	next := newCancelableInput(r)
	if _, err := w.Write([]byte("k")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 1)
	n, err := next.Read(buf)
	if err != nil || n != 1 || buf[0] != 'k' {
		t.Fatalf("取消后 stdin 不可再读: n=%d data=%q err=%v", n, buf[:n], err)
	}
}

// TestCancelableStdinStopBeforeRead 覆盖「取消先于读取发起」的竞态：
// 已取消的输入源不得再发起阻塞读取。
func TestCancelableStdinStopBeforeRead(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()

	in := newCancelableInput(r)
	stop := make(chan struct{})
	in.setStop(stop)
	close(stop)
	deadline := time.Now().Add(time.Second)
	for {
		in.mu.Lock()
		canceled := in.canceled
		in.mu.Unlock()
		if canceled {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("stop 关闭后未置取消标记")
		}
		time.Sleep(time.Millisecond)
	}

	buf := make([]byte, 1)
	n, err := in.Read(buf)
	if !errors.Is(err, io.EOF) || n != 0 {
		t.Fatalf("已取消的输入源应直接返回 EOF，实际: n=%d err=%v", n, err)
	}
}

// TestSessionEndDoesNotSwallowNextKey 端到端回归 Windows 已知问题：
// 会话正常结束（远程 shell 退出）后，runOnce 必须等输入泵退出再返回，
// 否则残留的阻塞读取会吞掉回 TUI 后的第一个按键。
func TestSessionEndDoesNotSwallowNextKey(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()

	in := newCancelableInput(r)
	tracker := newOSCTracker(io.Discard)
	waitDone := make(chan error, 1)
	runDone := make(chan struct{})
	go func() {
		_, _, _ = runOnce(in, tracker, nopWriteCloser{}, waitDone)
		close(runDone)
	}()

	waitForInRead(t, in)
	waitDone <- nil // 模拟远程 shell 正常退出

	select {
	case <-runDone:
	case <-time.After(2 * time.Second):
		t.Fatal("会话结束后 runOnce 未返回（取消 stdin 读取失败）")
	}

	// 会话结束后到达的按键必须留给下一个读取者（TUI）
	if _, err := w.Write([]byte("k")); err != nil {
		t.Fatal(err)
	}
	got := make(chan byte, 1)
	go func() {
		buf := make([]byte, 1)
		if _, err := r.Read(buf); err == nil {
			got <- buf[0]
		}
	}()
	select {
	case b := <-got:
		if b != 'k' {
			t.Fatalf("下一个读取者读到 %q，期望 'k'", b)
		}
	case <-time.After(time.Second):
		t.Fatal("回 TUI 的首个按键被残留读取吞掉")
	}
}

// waitForInRead 等待 cancelableStdin 真正进入阻塞读取（避免测试早于读取发起）。
func waitForInRead(t *testing.T, in *cancelableStdin) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		in.mu.Lock()
		reading := in.inRead
		in.mu.Unlock()
		if reading {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("输入泵未进入阻塞读取")
}
