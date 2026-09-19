//go:build windows

package tui

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestLocalParentWindowsDrivePath Windows 盘符路径的上级目录必须用 filepath 语义：
// path.Dir(`C:\Users\foo`) 会因无 "/" 返回 "."（第一次 Backspace 就坏）。见 2.4。
func TestLocalParentWindowsDrivePath(t *testing.T) {
	cases := []struct{ in, want string }{
		{`C:\Users\foo`, `C:\Users`},
		{`C:\Users`, `C:\`},
		{`C:\`, `C:\`},
		{filepath.Join(`C:\Users\foo`, "Documents"), `C:\Users\foo`},
	}
	for _, c := range cases {
		if got := localParent(c.in); got != c.want {
			t.Fatalf("localParent(%q) = %q，期望 %q", c.in, got, c.want)
		}
	}
}

// TestLocalJoinWindowsDrivePath 本地目录拼接不得引入 POSIX 分隔符（混用会与 filepath 导航不一致）。
func TestLocalJoinWindowsDrivePath(t *testing.T) {
	got := filepath.Join(`C:\Users\foo`, "Documents")
	if strings.Contains(got, "/") {
		t.Fatalf("本地路径拼接到 POSIX 分隔符: %q", got)
	}
	if got != `C:\Users\foo\Documents` {
		t.Fatalf("filepath.Join 结果异常: %q", got)
	}
}
