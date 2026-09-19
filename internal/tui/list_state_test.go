package tui

import (
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"simple-connect/internal/model"
	sshc "simple-connect/internal/ssh"
	"simple-connect/internal/store"
)

// addHosts 批量添加编号主机（测试常量前缀 + 序号）
func addHosts(t *testing.T, s *store.Store, prefix string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if err := s.Add(&model.Host{
			Name: fmt.Sprintf("%s-%02d", prefix, i),
			Host: fmt.Sprintf("10.0.0.%d", i+1),
			Port: 22, User: "root", Auth: model.AuthPassword,
		}); err != nil {
			t.Fatal(err)
		}
	}
}

// TestListStatusGeneration 世代号：Init 挂新链；过期 tick 不续订、不探测；
// 过期探测结果不得覆盖新结果（docs/ux-perf-review.md 2.1/8.7）。
func TestListStatusGeneration(t *testing.T) {
	s := testStore(t)
	addHosts(t, s, "web", 1)
	m := newListModel(s)

	cmd := m.Init()
	if m.tickGen != 1 || m.probeGen != 1 {
		t.Fatalf("Init 后世代号应为 tick=1 probe=1，实际 tick=%d probe=%d", m.tickGen, m.probeGen)
	}
	if cmd == nil {
		t.Fatal("Init 应返回轮询 + 探测命令")
	}
	// 命令为 Batch（不执行子命令，避免真实探测与 30s tick 等待）
	bm, ok := cmd().(tea.BatchMsg)
	if !ok || len(bm) != 2 {
		t.Fatalf("Init 命令应为含探测与 tick 的 BatchMsg，实际 %T", cmd())
	}

	pg, tg := m.probeGen, m.tickGen
	next, c := m.Update(statusTickMsg{gen: tg - 1}) // 过期世代号
	if c != nil {
		t.Fatal("过期 tick 不应再返回命令（不得续订）")
	}
	if next.probeGen != pg || next.tickGen != tg {
		t.Fatalf("过期 tick 不得触发探测或改动世代号，实际 probe=%d tick=%d", next.probeGen, next.tickGen)
	}

	next, c = next.Update(statusTickMsg{gen: tg}) // 当前世代号
	if c == nil {
		t.Fatal("当前 tick 应续订并触发探测")
	}
	if next.tickGen != tg+1 || next.probeGen != pg+1 {
		t.Fatalf("续订后世代号应递增，实际 tick=%d probe=%d", next.tickGen, next.probeGen)
	}

	// 过期探测结果丢弃
	next, _ = next.Update(statusResultMsg{gen: pg, id: "stale", status: sshc.StatusOnline})
	if _, ok := next.status["stale"]; ok {
		t.Fatal("过期探测结果不应写入 status")
	}
	// 当前世代结果写入
	next, _ = next.Update(statusResultMsg{gen: next.probeGen, id: "fresh", status: sshc.StatusOnline})
	if next.status["fresh"] != sshc.StatusOnline {
		t.Fatalf("当前探测结果应写入，实际 %v", next.status)
	}
}

// TestStatusProbeIncremental 探测按主机逐条回传（不再等全部完成），且限流不超上限。
func TestStatusProbeIncremental(t *testing.T) {
	oldTimeout := statusCheckTimeout
	statusCheckTimeout = 300 * time.Millisecond
	t.Cleanup(func() { statusCheckTimeout = oldTimeout })

	// 取一个刚关闭的本地端口：拨号必定快速失败，无需真实 SSH 服务
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()

	s := testStore(t)
	for i := 0; i < 3; i++ {
		if err := s.Add(&model.Host{
			Name: fmt.Sprintf("probe-%d", i), Host: "127.0.0.1", Port: port,
			User: "root", Auth: model.AuthPassword,
		}); err != nil {
			t.Fatal(err)
		}
	}
	m := newListModel(s)
	cmd := m.runStatusChecks()
	if cmd == nil {
		t.Fatal("应返回探测命令")
	}
	bm, ok := cmd().(tea.BatchMsg)
	if !ok || len(bm) != 3 {
		t.Fatalf("应每台主机一个命令（增量回传），实际 %T", cmd())
	}
	gen := m.probeGen
	ids := map[string]bool{}
	for _, sub := range bm {
		msg, ok := sub().(statusResultMsg)
		if !ok {
			t.Fatalf("子命令应返回单台结果 statusResultMsg，实际 %T", sub())
		}
		if msg.gen != gen {
			t.Fatalf("结果应携带当前世代号 %d，实际 %d", gen, msg.gen)
		}
		if msg.status != sshc.StatusOffline {
			t.Fatalf("已关闭端口应探测为离线，实际 %v", msg.status)
		}
		ids[msg.id] = true
	}
	if len(ids) != 3 {
		t.Fatalf("应回传 3 台主机结果，实际 %d", len(ids))
	}
}

// TestListFilterCursorStaysValid 过滤收窄后光标必须落在新结果范围内（2.2）。
func TestListFilterCursorStaysValid(t *testing.T) {
	s := testStore(t)
	addHosts(t, s, "web", 8)
	addHosts(t, s, "db", 2)
	m := newListModel(s)
	if len(m.filtered) != 10 {
		t.Fatalf("初始应有 10 条，实际 %d", len(m.filtered))
	}

	m.cursor = 9 // 停在列表末尾
	m.filtering = true
	next, _ := m.Update(press("d").(tea.KeyPressMsg))
	if len(next.filtered) != 2 {
		t.Fatalf("过滤后应命中 2 条，实际 %d", len(next.filtered))
	}
	if next.cursor < 0 || next.cursor >= len(next.filtered) {
		t.Fatalf("光标越界: cursor=%d len=%d", next.cursor, len(next.filtered))
	}
	if !strings.Contains(next.View().Content, "▸ ") {
		t.Fatalf("过滤后应有高亮行:\n%s", next.View().Content)
	}
}

// TestListEmptyNavNoNegativeCursor 空列表 End/PgDn 不得把光标打成 -1（8.1）。
func TestListEmptyNavNoNegativeCursor(t *testing.T) {
	m := &listModel{status: map[string]sshc.Status{}}
	for _, code := range []rune{tea.KeyEnd, tea.KeyPgDown, tea.KeyPgUp} {
		next, _ := m.Update(pressKey(code))
		if next.cursor != 0 {
			t.Fatalf("空列表按键 %d 后 cursor 应为 0，实际 %d", code, next.cursor)
		}
		m = next
	}
}

// TestListReloadKeepsSelectionAndStatus reload 按 ID 保留选中项与在线状态；
// 当前项被删除时落到邻近位置而非跳回顶部（3.2/3.7.2/8.7）。
func TestListReloadKeepsSelectionAndStatus(t *testing.T) {
	s := testStore(t)
	addHosts(t, s, "host", 3)
	m := newListModel(s)

	m.cursor = 1
	id := m.selectedID()
	if id == "" {
		t.Fatal("应有选中项")
	}
	m.status[id] = sshc.StatusOnline
	m.reload(s.Hosts()) // 模拟从表单/SFTP 返回
	if m.selectedID() != id {
		t.Fatalf("reload 后应保持选中项 %s，实际 %s", id, m.selectedID())
	}
	if m.status[id] != sshc.StatusOnline {
		t.Fatal("reload 后应保留已探测到的在线状态")
	}

	// 删除当前项：落到邻近下标（原下标 1）
	hosts := s.Hosts()
	m.reload([]*model.Host{hosts[0], hosts[2]})
	if len(m.filtered) != 2 {
		t.Fatalf("应剩 2 条，实际 %d", len(m.filtered))
	}
	if m.cursor != 1 {
		t.Fatalf("删除当前项后应落到邻近位置 1，实际 %d", m.cursor)
	}
}

// TestListStateSnapshotRoundTrip 跨 tea.Program 生命周期快照：过滤/光标/状态可恢复，
// 且快照与模型解耦（SSH 往返后列表不再全部重置）。
func TestListStateSnapshotRoundTrip(t *testing.T) {
	s := testStore(t)
	addHosts(t, s, "web", 2)
	addHosts(t, s, "db", 2)
	root := NewRoot(s)

	root.list.filter = "db"
	root.list.applyFilter()
	root.list.cursor = 1
	id := root.list.selectedID()
	root.list.status[id] = sshc.StatusOnline

	st := root.ListState()
	if st == nil {
		t.Fatal("ListState 不应返回 nil")
	}
	if st.Filter != "db" || st.CursorID != id || st.Status[id] != sshc.StatusOnline {
		t.Fatalf("快照内容不符: %+v", st)
	}

	root2 := NewRootWithListState(s, st)
	if root2.list.filter != "db" {
		t.Fatalf("应恢复过滤词，实际 %q", root2.list.filter)
	}
	if root2.list.selectedID() != id {
		t.Fatalf("应恢复光标选中项 %s，实际 %s", id, root2.list.selectedID())
	}
	if root2.list.status[id] != sshc.StatusOnline {
		t.Fatal("应恢复在线状态")
	}

	// 快照为副本：后续变更不影响已保存状态
	root.list.status[id] = sshc.StatusOffline
	if st.Status[id] != sshc.StatusOnline {
		t.Fatal("快照状态应为独立副本")
	}
}

// TestListScrollKeepsCursorVisible 列表滚动：光标始终可见、footer 固定最后一行（3.3）。
func TestListScrollKeepsCursorVisible(t *testing.T) {
	s := testStore(t)
	addHosts(t, s, "host", 50)
	m := newListModel(s)
	m.width, m.height = 100, 20

	m.cursor = 30
	m.ensureVisible()
	start, end := m.visibleRange()
	if m.cursor < start || m.cursor >= end {
		t.Fatalf("光标应落在可视区 [%d,%d)，实际 %d", start, end, m.cursor)
	}
	if end-start != 10 {
		t.Fatalf("可视条目应为 10 行（20-10），实际 %d", end-start)
	}
	if !strings.Contains(m.View().Content, "host-30") {
		t.Fatal("当前光标行应在渲染结果中")
	}

	view := m.View().Content
	lines := strings.Split(view, "\n")
	if len(lines) != m.height {
		t.Fatalf("View 行数应等于终端高度 %d，实际 %d", m.height, len(lines))
	}
	// 带边框 footer 占 3 行（上边框/内容/下边框），底部 padding 占最后一行
	found := false
	for _, l := range lines[len(lines)-4:] {
		if strings.Contains(l, "q 退出") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("footer 应固定在底部区域，实际末尾行: %q", lines[len(lines)-4:])
	}

	// Home 回到顶部，滚动窗口归零
	next, _ := m.Update(pressKey(tea.KeyHome))
	if next.top != 0 || next.cursor != 0 {
		t.Fatalf("Home 后应回到顶部，实际 top=%d cursor=%d", next.top, next.cursor)
	}
}

// TestListNarrowWidthLayout 窄终端按比例收缩列宽并截断内容（3.3）。
func TestListNarrowWidthLayout(t *testing.T) {
	s := testStore(t)
	_ = s.Add(&model.Host{
		Name: "非常长的中文连接名称测试用例", Host: "10.0.0.1",
		Port: 22, User: "longuser", Auth: model.AuthPassword,
	})
	m := newListModel(s)
	m.width = 40
	lay := m.layout()
	if lay.nameW != 14 || lay.targetW != 10 {
		t.Fatalf("窄屏列宽应为 name=14 target=10，实际 name=%d target=%d", lay.nameW, lay.targetW)
	}
	view := m.View().Content
	if !strings.Contains(view, "…") {
		t.Fatalf("超长内容应截断显示省略号:\n%s", view)
	}
	// 渲染级：行内容确实按截断后的列宽拼装（外层 padding 会把每行补到最长行宽，不在此断言总宽）
	row := m.renderRow(m.filtered[0], 0, lay)
	if !strings.Contains(row, "…") {
		t.Fatalf("超长名称/目标应截断: %q", row)
	}
	if strings.Contains(row, "非常长的中文连接名称测试用例") || strings.Contains(row, "longuser@10.0.0.1:22") {
		t.Fatalf("超长列应被截断，实际 %q", row)
	}
}

// TestListFilterSlashKeepsKeyword 已过滤时再按 / 不瞬间清空关键字（3.7.1）。
func TestListFilterSlashKeepsKeyword(t *testing.T) {
	s := testStore(t)
	addHosts(t, s, "web", 2)
	addHosts(t, s, "db", 1)
	m := newListModel(s)

	m.filtering = true
	next, _ := m.Update(press("d").(tea.KeyPressMsg))
	next.filtering = false // 模拟 Enter 确认过滤
	next, _ = next.Update(press("/").(tea.KeyPressMsg))
	if next.filter != "d" {
		t.Fatalf("再按 / 应保留已有关键字，实际 %q", next.filter)
	}
	if len(next.filtered) != 1 {
		t.Fatalf("列表不应瞬间全量，实际 %d 条", len(next.filtered))
	}
	// Esc 清空
	next, _ = next.Update(pressKey(tea.KeyEsc))
	if next.filter != "" || len(next.filtered) != 3 {
		t.Fatalf("Esc 应清空过滤，实际 filter=%q n=%d", next.filter, len(next.filtered))
	}
}
