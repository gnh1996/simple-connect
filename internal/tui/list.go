package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"

	"simple-connect/internal/model"
	sshc "simple-connect/internal/ssh"
	"simple-connect/internal/store"
)

// ---- 连接列表页 ----

// statusTickInterval 状态轮询间隔（包级变量便于测试；tea.Tick 无假时钟）。
var statusTickInterval = 30 * time.Second

// statusCheckTimeout 单台主机状态探测超时。
var statusCheckTimeout = 3 * time.Second

// statusProbeConcurrency 状态探测并发上限（避免 fd/限流风暴）。
const statusProbeConcurrency = 10

// ListState 列表页跨 tea.Program 生命周期的界面状态快照：
// SSH 会话必须退出 TUI（终端交还 raw 模式），main 在往返前后保存/注入，
// 避免过滤器、光标与已探测的在线状态全部重置。
type ListState struct {
	Filter   string
	CursorID string
	Status   map[string]sshc.Status
}

type listModel struct {
	store       *store.Store
	hosts       []*model.Host
	filtered    []*model.Host
	status      map[string]sshc.Status
	cursor      int
	top         int // 滚动窗口顶部（filtered 下标）
	filter      string
	filtering   bool
	confirmID   string // 待删除确认的连接 ID
	connectID   string // 待免密确认连接的连接 ID
	connectHint string // 免密提示文案
	err         string

	width  int // 终端尺寸（WindowSizeMsg；0=未知，不滚动/不截断）
	height int

	// tickGen 轮询链世代号：Init/回到列表时自增并挂一条带该 gen 的新 tick，
	// 过期 gen 的 tick 直接丢弃。不能用 armed 布尔——列表不在前台时 tick 进不了
	// list.Update（链已死），布尔残留会让回列表后不再挂链，周期性探测彻底消失。
	tickGen int
	// probeGen 探测结果世代号：每次发起探测自增；过期结果丢弃，避免 s 刷新与
	// 周期探测并发时旧结果覆盖新结果。
	probeGen int
}

func newListModel(s *store.Store) *listModel {
	return newListModelWithState(s, nil)
}

// newListModelWithState 创建列表模型并注入跨程序生命周期状态（nil 表示全新）。
func newListModelWithState(s *store.Store, st *ListState) *listModel {
	_ = s.Reload() // 进入列表页时同步最新配置（多实例并发编辑可见）
	m := &listModel{store: s, hosts: s.Hosts(), status: map[string]sshc.Status{}}
	if st != nil {
		m.filter = st.Filter
		for id, v := range st.Status {
			m.status[id] = v
		}
	}
	m.applyFilter()
	if st != nil {
		if idx := indexOfHost(m.filtered, st.CursorID); idx >= 0 {
			m.cursor = idx
		}
	}
	m.ensureVisible()
	return m
}

func indexOfHost(hosts []*model.Host, id string) int {
	if id == "" {
		return -1
	}
	for i, h := range hosts {
		if h.ID == id {
			return i
		}
	}
	return -1
}

// selectedID 当前光标选中的连接 ID（无选中返回空串）。
func (m *listModel) selectedID() string {
	if m.cursor >= 0 && m.cursor < len(m.filtered) {
		return m.filtered[m.cursor].ID
	}
	return ""
}

// reload 用最新配置重建列表：保留已探测状态与选中项（按 ID 恢复；
// 原选中项已被删除时落到邻近位置而非跳回顶部）。
func (m *listModel) reload(hosts []*model.Host) {
	prevID := m.selectedID()
	prevIdx := m.cursor
	m.hosts = hosts
	m.confirmID = ""
	m.connectID = ""
	m.connectHint = ""
	m.err = ""
	m.applyFilter()
	switch idx := indexOfHost(m.filtered, prevID); {
	case idx >= 0:
		m.cursor = idx
	case len(m.filtered) == 0:
		m.cursor = 0
	case prevIdx >= len(m.filtered):
		m.cursor = len(m.filtered) - 1
	default:
		m.cursor = prevIdx
	}
	m.ensureVisible()
}

func (m *listModel) Init() tea.Cmd {
	return tea.Batch(m.runStatusChecks(), m.armStatusTick())
}

// armStatusTick 挂一条带当前世代号的轮询 tick：世代号自增使旧链作废，
// 列表往返（表单/SFTP/SSH）后一定存在且仅存在一条有效链。
func (m *listModel) armStatusTick() tea.Cmd {
	m.tickGen++
	gen := m.tickGen
	return tea.Tick(statusTickInterval, func(time.Time) tea.Msg { return statusTickMsg{gen: gen} })
}

// runStatusChecks 启动一轮状态探测：每台主机一个 Cmd（tea.Batch 并发执行，
// 探完一台回传一条结果，不再等全部探完才一起点亮），信号量限流
// statusProbeConcurrency。结果携带本次 probeGen，过期结果由 Update 丢弃。
func (m *listModel) runStatusChecks() tea.Cmd {
	m.probeGen++
	gen := m.probeGen
	hosts := m.hosts
	if len(hosts) == 0 {
		return nil
	}
	sem := make(chan struct{}, statusProbeConcurrency)
	cmds := make([]tea.Cmd, 0, len(hosts))
	for _, h := range hosts {
		h := h
		cmds = append(cmds, func() tea.Msg {
			sem <- struct{}{}
			defer func() { <-sem }()
			return statusResultMsg{gen: gen, id: h.ID, status: sshc.CheckStatus(h, statusCheckTimeout)}
		})
	}
	return tea.Batch(cmds...)
}

func (m *listModel) applyFilter() {
	if m.filter == "" {
		m.filtered = m.hosts
		return
	}
	q := strings.ToLower(m.filter)
	m.filtered = nil
	for _, h := range m.hosts {
		if strings.Contains(strings.ToLower(h.Name), q) ||
			strings.Contains(strings.ToLower(h.Host), q) ||
			strings.Contains(strings.ToLower(h.User), q) {
			m.filtered = append(m.filtered, h)
		}
	}
}

func (m *listModel) Update(msg tea.Msg) (*listModel, tea.Cmd) {
	switch msg := msg.(type) {
	case statusResultMsg:
		if msg.gen != m.probeGen {
			return m, nil // 过期探测结果（s 刷新或周期轮询已发起新一轮）
		}
		m.status[msg.id] = msg.status
		return m, nil
	case statusTickMsg:
		if msg.gen != m.tickGen {
			return m, nil // 过期 tick（列表不在前台期间触发的旧链）：不续订、不探测
		}
		return m, tea.Batch(m.runStatusChecks(), m.armStatusTick())
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.ensureVisible()
		return m, nil
	case tea.KeyPressMsg:
		if m.connectID != "" {
			return m.handleConnectConfirm(msg)
		}
		if m.confirmID != "" {
			return m.handleConfirm(msg)
		}
		if m.filtering {
			return m.handleFilter(msg)
		}
		return m.handleNav(msg)
	}
	return m, nil
}

// handleConnectConfirm 处理免密提示的二次确认
func (m *listModel) handleConnectConfirm(msg tea.KeyPressMsg) (*listModel, tea.Cmd) {
	k := msg.Key()
	if k.Code == tea.KeyEnter {
		h := m.store.Find(m.connectID)
		m.connectID = ""
		m.connectHint = ""
		if h != nil {
			return m, tea.Cmd(func() tea.Msg { return connectMsg{host: h} })
		}
		return m, nil
	}
	if k.Code == tea.KeyEsc || k.Text == "n" || k.Text == "N" || k.Text == "q" || k.Text == "c" {
		m.connectID = ""
		m.connectHint = ""
	}
	return m, nil
}

// connectHint 检测选中主机连接时是否可能无法自动认证，返回提示文案（空串表示可直接连接）
func connectHint(h *model.Host) string {
	if h.Auth == model.AuthPassword {
		if !h.HasPassword {
			return "该连接未保存密码，将无法自动认证；请按 e 编辑并设置密码"
		}
		return "" // 已保存密码，自建会话直接免密
	}
	if h.Auth == model.AuthKey {
		if !sshc.AgentAvailable() {
			return sshc.AgentHint()
		}
		if h.KeyPath != "" {
			if in, known := sshc.KeyInAgent(sshc.ExpandPath(h.KeyPath)); known && !in {
				return fmt.Sprintf("私钥 %s 未加入 ssh-agent，建议: ssh-add %s", h.KeyPath, h.KeyPath)
			}
		}
	}
	return ""
}

func (m *listModel) handleConfirm(msg tea.KeyPressMsg) (*listModel, tea.Cmd) {
	m.err = ""
	k := msg.Key()
	switch {
	case k.Code == tea.KeyEnter:
		fallthrough
	case k.Text == "y" || k.Text == "Y":
		id := m.confirmID
		m.confirmID = ""
		if err := m.store.Delete(id); err != nil {
			m.err = err.Error()
			return m, nil
		}
		m.reload(m.store.Hosts())
	case k.Code == tea.KeyEsc || k.Text == "n" || k.Text == "N" || k.Text == "q":
		m.confirmID = ""
	}
	return m, nil
}

func (m *listModel) handleFilter(msg tea.KeyPressMsg) (*listModel, tea.Cmd) {
	m.err = ""
	k := msg.Key()
	switch k.Code {
	case tea.KeyEsc:
		m.filtering = false
		m.filter = ""
		m.applyFilter()
		m.clampCursorToFiltered()
		m.ensureVisible()
		return m, nil
	case tea.KeyEnter:
		m.filtering = false
		m.clampCursorToFiltered()
		m.ensureVisible()
		return m, nil
	case tea.KeyBackspace:
		if len(m.filter) > 0 {
			runes := []rune(m.filter)
			m.filter = string(runes[:len(runes)-1])
		}
	default:
		if k.Text != "" {
			m.filter += k.Text
		}
	}
	// 先重算过滤结果，再按新结果钳位光标：旧顺序用过滤前长度钳位，
	// 结果收窄后光标会越界导致无高亮（见 docs/ux-perf-review.md 2.2）。
	m.applyFilter()
	m.clampCursorToFiltered()
	m.ensureVisible()
	return m, nil
}

// clampCursorToFiltered 确保光标落在过滤结果范围内。
func (m *listModel) clampCursorToFiltered() {
	if m.cursor >= len(m.filtered) {
		m.cursor = 0
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
}

func (m *listModel) handleNav(msg tea.KeyPressMsg) (*listModel, tea.Cmd) {
	m.err = ""
	k := msg.Key()
	n := len(m.filtered)
	switch k.Code {
	case tea.KeyUp:
		if n > 0 {
			m.cursor = (m.cursor - 1 + n) % n
		}
	case tea.KeyDown:
		if n > 0 {
			m.cursor = (m.cursor + 1) % n
		}
	case tea.KeyTab:
		if k.Mod.Contains(tea.ModShift) {
			if n > 0 {
				m.cursor = (m.cursor - 1 + n) % n
			}
		} else if n > 0 {
			m.cursor = (m.cursor + 1) % n
		}
	case tea.KeyHome:
		m.cursor = 0
	case tea.KeyEnd:
		if n > 0 {
			m.cursor = n - 1
		}
	case tea.KeyPgUp:
		if n > 0 {
			m.cursor -= 10
			if m.cursor < 0 {
				m.cursor = 0
			}
		}
	case tea.KeyPgDown:
		if n > 0 { // 空列表时不得把光标打成 -1
			m.cursor += 10
			if m.cursor > n-1 {
				m.cursor = n - 1
			}
		}
	case tea.KeyEnter:
		if n > 0 {
			h := m.filtered[m.cursor]
			if hint := connectHint(h); hint != "" {
				m.connectID = h.ID
				m.connectHint = hint
				return m, nil
			}
			return m, tea.Cmd(func() tea.Msg {
				return connectMsg{host: h}
			})
		}
	default:
		if k.Text != "" {
			switch k.Text {
			case "f":
				if n > 0 {
					sel := m.filtered[m.cursor]
					return m, tea.Cmd(func() tea.Msg { return navSFTPMsg{host: sel} })
				}
			case "a":
				return m, tea.Cmd(func() tea.Msg { return navFormMsg{} })
			case "e":
				if n > 0 {
					sel := m.filtered[m.cursor]
					return m, tea.Cmd(func() tea.Msg { return navFormMsg{host: sel} })
				}
			case "d":
				if n > 0 {
					m.confirmID = m.filtered[m.cursor].ID
				}
			case "s":
				_ = m.store.Reload() // 刷新时同步其他实例的配置变更
				m.hosts = m.store.Hosts()
				m.applyFilter()
				m.clampCursorToFiltered()
				m.ensureVisible()
				return m, m.runStatusChecks() // 只跑探测，不 bump tickGen（不动已挂的轮询链）
			case "/":
				// 保留已有关键字：已过滤后再按 / 想改一个字符时列表不再瞬间全量
				m.filtering = true
				m.ensureVisible() // 过滤输入行占一行，压缩后保持光标可见
				return m, nil
			case "q":
				return m, tea.Cmd(func() tea.Msg { return quitMsg{} })
			}
		}
	}
	m.ensureVisible()
	return m, nil
}

// ---- 滚动与渲染 ----

// bodyHeight 列表可视条目行数；0 表示尺寸未知（显示全部）。
// 行数构成：外层 Padding 上下 2 + 标题 2 + 表头/分隔线 2 + 动态行 D +
// footer 前空行 1 + 带边框 footer 3 = 10 + D，其余留给条目。
func (m *listModel) bodyHeight() int {
	if m.height <= 0 {
		return 0
	}
	body := m.height - 10 - len(m.dynamicLines())
	if body < 1 {
		body = 1
	}
	return body
}

// visibleRange 当前滚动窗口对应的 filtered 下标区间。
func (m *listModel) visibleRange() (start, end int) {
	n := len(m.filtered)
	body := m.bodyHeight()
	if body <= 0 || body >= n {
		return 0, n
	}
	start = m.top
	if start < 0 {
		start = 0
	}
	if start > n-body {
		start = n - body
	}
	return start, start + body
}

// ensureVisible 调整滚动窗口使光标可见。
func (m *listModel) ensureVisible() {
	n := len(m.filtered)
	if n == 0 {
		m.top = 0
		return
	}
	body := m.bodyHeight()
	if body <= 0 || body >= n {
		m.top = 0
		return
	}
	if m.cursor < m.top {
		m.top = m.cursor
	}
	if m.cursor >= m.top+body {
		m.top = m.cursor - body + 1
	}
	if m.top > n-body {
		m.top = n - body
	}
	if m.top < 0 {
		m.top = 0
	}
}

// dynamicLines 列表区与 footer 之间的动态区块行（过滤/确认/错误提示），
// 渲染与高度计算共用，保证有滚动时 footer 行数稳定。
func (m *listModel) dynamicLines() []string {
	var lines []string
	if m.filtering {
		lines = append(lines, styleCursor.Render("过滤: ")+m.filter+"▌")
	} else if m.connectID != "" {
		h := m.store.Find(m.connectID)
		name := ""
		if h != nil {
			name = h.Name
		}
		lines = append(lines, styleInfo.Render("免密提示: "+m.connectHint))
		lines = append(lines, styleDim.Render(fmt.Sprintf("连接 %q 仍将进行？ Enter 继续连接 / Esc 取消", name)))
	} else if m.confirmID != "" {
		h := m.store.Find(m.confirmID)
		name := ""
		if h != nil {
			name = h.Name
		}
		lines = append(lines, styleInfo.Render(fmt.Sprintf("确认删除连接 %q？ (y/N)", name)))
	}
	if m.err != "" {
		lines = append(lines, styleError.Render(m.err))
	}
	return lines
}

// listLayout 列表列宽：终端过窄时先压缩目标列，再压缩名称列。
type listLayout struct {
	nameW   int
	targetW int
}

func (m *listModel) layout() listLayout {
	const (
		defNameW   = 24
		defTargetW = 34
		minNameW   = 8
		minTargetW = 10
	)
	lay := listLayout{nameW: defNameW, targetW: defTargetW}
	if m.width <= 0 {
		return lay
	}
	// 可用内容宽 = 终端宽 - 外层 Padding(1,2) 的 4 列 - 固定列
	//（前缀 2 + 图标 2 + 认证 8：AuthType 取值为 password/key）
	avail := m.width - 4 - 12
	if avail >= defNameW+defTargetW {
		return lay
	}
	lay.targetW = avail - defNameW
	if lay.targetW < minTargetW {
		lay.targetW = minTargetW
		lay.nameW = avail - lay.targetW
		if lay.nameW < minNameW {
			lay.nameW = minNameW
		}
	}
	return lay
}

func (m *listModel) View() tea.View {
	var b strings.Builder
	lay := m.layout()

	title := styleTitle.Render("simple-connect")
	keyring := ""
	if !m.store.UsingKeyring() {
		keyring = styleDim.Render(" [密码明文存储]")
	}
	b.WriteString(lipgloss.JoinHorizontal(lipgloss.Top,
		title,
		styleDim.Render(fmt.Sprintf("   共 %d 个连接", len(m.hosts)))+keyring,
	))
	b.WriteString("\n\n")

	if len(m.filtered) == 0 {
		if len(m.hosts) == 0 {
			b.WriteString(styleDim.Render("暂无连接，按 a 添加第一个连接"))
		} else {
			b.WriteString(styleDim.Render("无匹配结果，按 Esc 清除过滤"))
		}
		b.WriteString("\n")
	} else {
		b.WriteString(renderListHeader(lay) + "\n")
		start, end := m.visibleRange()
		for i := start; i < end; i++ {
			b.WriteString(m.renderRow(m.filtered[i], i, lay) + "\n")
		}
	}

	for _, l := range m.dynamicLines() {
		b.WriteString(l + "\n")
	}
	b.WriteString("\n" + renderListFooter())
	return tea.NewView(lipgloss.NewStyle().Padding(1, 2).Render(b.String()))
}

func renderListHeader(lay listLayout) string {
	name := styleHeader.Render(padRight("名称", lay.nameW))
	target := styleHeader.Render(padRight("目标", lay.targetW))
	auth := styleHeader.Render("认证")
	return lipgloss.JoinHorizontal(lipgloss.Top, name, target, auth) + "\n" +
		styleDim.Render(strings.Repeat("─", lay.nameW+lay.targetW+8))
}

func (m *listModel) renderRow(h *model.Host, idx int, lay listLayout) string {
	st, ok := m.status[h.ID]
	if !ok {
		st = sshc.StatusUnknown
	}
	icon := "○"
	switch st {
	case sshc.StatusOnline:
		icon = styleOnline.Render("●")
	case sshc.StatusOffline:
		icon = styleOffline.Render("○")
	default:
		icon = styleUnknown.Render("?")
	}

	name := padRight(runewidth.Truncate(h.Name, lay.nameW, "…"), lay.nameW)
	target := padRight(runewidth.Truncate(h.User+"@"+h.Addr(), lay.targetW, "…"), lay.targetW)
	auth := string(h.Auth)

	row := lipgloss.JoinHorizontal(lipgloss.Top,
		icon+" ",
		name,
		styleDim.Render(target),
		styleDim.Render(auth),
	)
	if idx == m.cursor {
		row = styleCursor.Render("▸ ") + row
	} else {
		row = "  " + row
	}
	return row
}

func renderListFooter() string {
	keys := styleDim.Render(
		"↑/↓ 移动  Enter 连接  f SFTP  a 新增  e 编辑  d 删除  s 刷新  / 过滤  q 退出",
	)
	hint := styleHint.Render("  免密: ssh-add")
	return styleFooter.Render(lipgloss.JoinHorizontal(lipgloss.Top, keys, hint))
}
