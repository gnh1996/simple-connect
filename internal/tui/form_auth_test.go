package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// TestFormErrorPersistsUntilNextKey View 只读：保存失败后错误保留，
// 直到用户下一次按键才清除（非编辑类消息不清）。见 docs/ux-perf-review.md 2.3。
func TestFormErrorPersistsUntilNextKey(t *testing.T) {
	s := testStore(t)
	root := NewRoot(s)
	root = upd(root, press("a")) // 进入新增表单

	// 空名称直接触发保存失败
	fm, _ := root.form.save()
	root.form = fm
	if root.form.err == "" {
		t.Fatal("空名称保存应报错")
	}
	if !strings.Contains(root.View().Content, "名称不能为空") {
		t.Fatalf("应渲染错误提示:\n%s", root.View().Content)
	}

	// 窗口尺寸消息不应清除错误（旧实现 View 里清模型，任何下一条 Msg 都会让提示消失）
	root = upd(root, tea.WindowSizeMsg{Width: 80, Height: 24})
	if root.form.err == "" {
		t.Fatal("WindowSizeMsg 不应清除错误")
	}
	if !strings.Contains(root.View().Content, "名称不能为空") {
		t.Fatal("错误提示应仍可见")
	}

	// 下一次按键清除错误
	root = upd(root, press("x"))
	if root.form.err != "" {
		t.Fatalf("按键后应清除错误，实际 %q", root.form.err)
	}
	if strings.Contains(root.View().Content, "名称不能为空") {
		t.Fatal("错误提示应已消失")
	}
}

// TestFormAuthFieldVisibility 认证方式二选一：隐藏的字段不渲染也不参与 Tab 导航（3.7.4）。
func TestFormAuthFieldVisibility(t *testing.T) {
	s := testStore(t)
	m := newFormModel(s, nil)

	// 密码模式：隐藏私钥路径
	if !m.visible(fieldPasswordIdx) || m.visible(fieldKeyIdx) {
		t.Fatal("密码模式应只显示密码字段")
	}
	if m.lastVisible() != 7 {
		t.Fatalf("最后一个可见字段应为本地目录(7)，实际 %d", m.lastVisible())
	}
	m.cursor = fieldPasswordIdx
	m.next()
	if m.cursor != 7 {
		t.Fatalf("密码模式应从密码直接跳到本地目录，实际 %d", m.cursor)
	}
	m.prev()
	if m.cursor != fieldPasswordIdx {
		t.Fatalf("反向应跳过隐藏字段回到密码，实际 %d", m.cursor)
	}

	// 切到私钥模式：隐藏密码、显示私钥路径
	m.cursor = fieldAuthIdx
	_, _ = m.Update(pressKey(tea.KeyRight))
	if m.visible(fieldPasswordIdx) || !m.visible(fieldKeyIdx) {
		t.Fatal("私钥模式应只显示私钥路径字段")
	}
	m.cursor = fieldAuthIdx
	m.next()
	if m.cursor != fieldKeyIdx {
		t.Fatalf("私钥模式应从认证方式跳到私钥路径，实际 %d", m.cursor)
	}
	if !strings.Contains(m.View().Content, "私钥路径") {
		t.Fatal("私钥模式应渲染私钥路径字段")
	}

	// 切回密码模式：私钥路径不再渲染
	m.cursor = fieldAuthIdx
	_, _ = m.Update(pressKey(tea.KeyLeft))
	if strings.Contains(m.View().Content, "私钥路径") {
		t.Fatal("密码模式不应渲染私钥路径字段")
	}
}

// TestFormKeyAuthSave 私钥模式保存：不要求密码字段，且保存 KeyPath。
func TestFormKeyAuthSave(t *testing.T) {
	s := testStore(t)
	root := NewRoot(s)
	root = upd(root, press("a"))
	fill := func(text string) {
		for _, r := range text {
			root = upd(root, press(string(r)))
		}
	}
	fill("密钥机")
	root = upd(root, pressKey(tea.KeyTab)) // 主机
	fill("10.9.9.9")
	root = upd(root, pressKey(tea.KeyTab)) // 端口
	root = upd(root, pressKey(tea.KeyTab)) // 用户名
	fill("root")
	root = upd(root, pressKey(tea.KeyTab)) // 认证方式
	root = upd(root, pressKey(tea.KeyRight))
	if root.form.visible(fieldPasswordIdx) || !root.form.visible(fieldKeyIdx) {
		t.Fatal("切换到私钥后字段可见性应变化")
	}
	root = upd(root, pressKey(tea.KeyTab)) // 私钥路径（密码隐藏）
	fill("~/.ssh/id_ed25519")
	root = upd(root, pressKey(tea.KeyTab)) // 本地目录
	root = upd(root, pressKey(tea.KeyEnter))

	if root.page != pageList {
		t.Fatalf("保存后应回列表，实际 %v", root.page)
	}
	hosts := root.Store.Hosts()
	if len(hosts) != 1 {
		t.Fatalf("应保存 1 条，实际 %d", len(hosts))
	}
	if hosts[0].KeyPath == "" {
		t.Fatal("应保存私钥路径")
	}
}
