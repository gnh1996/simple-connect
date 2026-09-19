// Package version 提供应用版本号。
//
// 发布构建由 -ldflags -X 注入 git tag（见 .github/workflows/release.yml），
// 因此打 tag 即版本源，无需改代码；本地构建降级为 dev + 短提交号
// （Go 的 VCS stamping，数据来自 debug.BuildInfo）。
package version

import (
	"runtime/debug"
	"strings"
)

// 构建时注入点：
//
//	go build -ldflags "-X simple-connect/internal/version.Version=v0.2.0 \
//	  -X simple-connect/internal/version.Commit=<sha> \
//	  -X simple-connect/internal/version.Date=<UTC RFC3339>"
//
// 本地 go build 不注入，保持空值。
var (
	Version string // git tag，如 v0.2.0
	Commit  string // 完整提交 SHA
	Date    string // 构建时间（UTC, RFC3339）
)

// String 返回展示用版本号，任何构建方式下都非空：
//
//	v0.2.0               发布构建（注入 tag）
//	v1.2.3               模块版本（go install 场景，防御性保留）
//	dev (5f027a0)        本地构建（VCS stamping 提供短提交号）
//	dev (5f027a0, dirty) 本地构建且有未提交修改
//	dev                  无 VCS 信息（如 -buildvcs=false）
func String() string {
	info, _ := debug.ReadBuildInfo() // 无模块信息时返回 nil，resolve 已兜底
	return resolve(Version, Commit, info)
}

// Full 返回用于诊断日志的完整构建信息：String() 基础上追加注入的
// commit/date（空值省略）。发布构建形如 "v0.2.0 commit=<sha> date=<UTC>"。
func Full() string {
	return full(String(), Commit, Date)
}

// full 构建信息拼接纯函数（便于单测）。
func full(ver, commit, date string) string {
	parts := []string{ver}
	if c := strings.TrimSpace(commit); c != "" {
		parts = append(parts, "commit="+c)
	}
	if d := strings.TrimSpace(date); d != "" {
		parts = append(parts, "date="+d)
	}
	return strings.Join(parts, " ")
}

// resolve 版本解析纯函数：注入 tag 优先，其次模块版本，最后 dev + 短提交号。
func resolve(tag, commit string, info *debug.BuildInfo) string {
	if v := strings.TrimSpace(tag); v != "" {
		return v
	}
	if info != nil {
		// 模块版本在从模块缓存安装时才非 "(devel)"；本仓库 module path 非 URL
		// 实际取不到，属防御性分支。
		if mv := info.Main.Version; mv != "" && mv != "(devel)" {
			return mv
		}
		if strings.TrimSpace(commit) == "" {
			if v, ok := vcsSetting(info, "vcs.revision"); ok {
				commit = v
			}
		}
	}
	short := shortCommit(commit)
	if short == "" {
		return "dev"
	}
	if dirty, _ := vcsSetting(info, "vcs.modified"); dirty == "true" {
		return "dev (" + short + ", dirty)"
	}
	return "dev (" + short + ")"
}

// vcsSetting 读取 build info 中的 VCS 设置项（vcs.revision / vcs.modified）。
func vcsSetting(info *debug.BuildInfo, key string) (string, bool) {
	if info == nil {
		return "", false
	}
	for _, s := range info.Settings {
		if s.Key == key {
			return s.Value, true
		}
	}
	return "", false
}

// shortCommit 截取 git 短 SHA（7 位；不足则原样返回）。
func shortCommit(commit string) string {
	commit = strings.TrimSpace(commit)
	if len(commit) > 7 {
		return commit[:7]
	}
	return commit
}
