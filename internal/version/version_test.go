package version

import (
	"runtime/debug"
	"testing"
)

func TestResolve(t *testing.T) {
	t.Parallel()
	const sha = "5f027a0d4c09c7d66fe504283eceb13ff893d38e"

	// devInfo 模拟本地 go build 的构建信息（VCS stamping）
	devInfo := func(modified bool) *debug.BuildInfo {
		m := "false"
		if modified {
			m = "true"
		}
		return &debug.BuildInfo{
			Main: debug.Module{Version: "(devel)"},
			Settings: []debug.BuildSetting{
				{Key: "vcs.revision", Value: sha},
				{Key: "vcs.modified", Value: m},
			},
		}
	}

	cases := []struct {
		name   string
		tag    string
		commit string
		info   *debug.BuildInfo
		want   string
	}{
		{"发布构建优先注入 tag", "v0.2.0", "", devInfo(false), "v0.2.0"},
		{"tag 构建忽略 dirty 标记", " v0.2.0 ", sha, devInfo(true), "v0.2.0"},
		{"模块版本降级", "", "", &debug.BuildInfo{Main: debug.Module{Version: "v1.2.3"}}, "v1.2.3"},
		{"本地构建取 VCS 短 SHA", "", "", devInfo(false), "dev (5f027a0)"},
		{"本地脏工作区", "", "", devInfo(true), "dev (5f027a0, dirty)"},
		{"仅注入 Commit", "", sha, nil, "dev (5f027a0)"},
		{"无 VCS 信息", "", "", &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}, "dev"},
		{"nil 构建信息", "", "", nil, "dev"},
		{"短提交号原样返回", "", "abc12", nil, "dev (abc12)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := resolve(tc.tag, tc.commit, tc.info); got != tc.want {
				t.Fatalf("resolve(%q, %q, …) = %q, want %q", tc.tag, tc.commit, got, tc.want)
			}
		})
	}
}

func TestStringNonEmpty(t *testing.T) {
	t.Parallel()
	if got := String(); got == "" {
		t.Fatal("String() 不得返回空串")
	}
}

func TestFull(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, ver, commit, date, want string
	}{
		{"发布构建带全部元数据", "v0.2.0", "5f027a0d4c09", "2026-09-19T00:00:00Z",
			"v0.2.0 commit=5f027a0d4c09 date=2026-09-19T00:00:00Z"},
		{"本地构建省略空值", "dev (5f027a0)", "", "", "dev (5f027a0)"},
		{"仅日期且去除空白", "v0.2.0", "  ", "2026-09-19", "v0.2.0 date=2026-09-19"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := full(tc.ver, tc.commit, tc.date); got != tc.want {
				t.Fatalf("full(%q, %q, %q) = %q, want %q", tc.ver, tc.commit, tc.date, got, tc.want)
			}
		})
	}
}
