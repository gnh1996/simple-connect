package tui

import (
	"fmt"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/lipgloss"

	"simple-connect/internal/model"
	sshc "simple-connect/internal/ssh"
	"simple-connect/internal/store"
)

// ---- 连接列表页 ----

type listModel struct {
	store       *store.Store
	hosts       []*model.Host
	filtered    []*model.Host
	status      map[string]sshc.Status
	cursor      int
	filter      string
	filtering   bool
	confirmID   string // 待删除确认的连接 ID
	connectID   string // 待免密确认连接的连接 ID
	connectHint string // 免密提示文案
	err         string
}

func newListModel(s *store.Store) *listModel {
	m := &listModel{store: s, hosts: s.Hosts(), status: map[string]sshc.Status{}}
	m.applyFilter()
	return m
}

func (m *listModel) reload(hosts []*model.Host) {
	m.hosts = hosts
	m.status = map[string]sshc.Status{}
	m.cursor = 0
	m.confirmID = ""
	m.connectID = ""
	m.connectHint = ""
	m.err = ""
	m.applyFilter()
}

func (m *listModel) Init() tea.Cmd {
	return tea.Batch(m.runStatusChecks(), statusTick())
}

func statusTick() tea.Cmd {
	return tea.Tick(30*time.Second, func(time.Time) tea.Msg { return statusTickMsg{} })
}

// runStatusChecks 并发检测全部主机状态
func (m *listModel) runStatusChecks() tea.Cmd {
	hosts := m.hosts
	return func() tea.Msg {
		results := map[string]sshc.Status{}
		if len(hosts) == 0 {
			return statusResultMsg{results: results}
		}
		var wg sync.WaitGroup
		var mu sync.Mutex
		for _, h := range hosts {
			wg.Add(1)
			go func(h *model.Host) {
				defer wg.Done()
				s := sshc.CheckStatus(h, 3*time.Second)
				mu.Lock()
				results[h.ID] = s
				mu.Unlock()
			}(h)
		}
		wg.Wait()
		return statusResultMsg{results: results}
	}
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
		for id, s := range msg.results {
			m.status[id] = s
		}
		return m, nil
	case statusTickMsg:
		return m, m.runStatusChecks()
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
	k := msg.Key()
	switch k.Code {
	case tea.KeyEsc:
		m.filtering = false
		m.filter = ""
		m.applyFilter()
		return m, nil
	case tea.KeyEnter:
		m.filtering = false
		if m.cursor >= len(m.filtered) {
			m.cursor = 0
		}
		return m, nil
	case tea.KeyBackspace:
		if len(m.filter) > 0 {
			m.filter = m.filter[:len(m.filter)-1]
		}
	default:
		if k.Text != "" {
			m.filter += k.Text
		}
	}
	if m.cursor >= len(m.filtered) {
		m.cursor = 0
	}
	m.applyFilter()
	return m, nil
}

func (m *listModel) handleNav(msg tea.KeyPressMsg) (*listModel, tea.Cmd) {
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
		m.cursor = n - 1
	case tea.KeyPgUp:
		m.cursor -= 10
		if m.cursor < 0 {
			m.cursor = 0
		}
	case tea.KeyPgDown:
		m.cursor += 10
		if m.cursor > n-1 {
			m.cursor = n - 1
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
				return m, m.runStatusChecks()
			case "/":
				m.filtering = true
				m.filter = ""
				m.applyFilter()
			case "q":
				return m, tea.Cmd(func() tea.Msg { return quitMsg{} })
			}
		}
	}
	return m, nil
}

func (m *listModel) View() tea.View {
	var b strings.Builder

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
	} else {
		b.WriteString(renderListHeader())
		for i, h := range m.filtered {
			b.WriteString(m.renderRow(h, i) + "\n")
		}
	}

	if m.filtering {
		b.WriteString("\n" + styleCursor.Render("过滤: ") + m.filter + "▌\n")
	} else if m.connectID != "" {
		h := m.store.Find(m.connectID)
		name := ""
		if h != nil {
			name = h.Name
		}
		b.WriteString("\n" + styleInfo.Render("免密提示: "+m.connectHint) + "\n")
		b.WriteString(styleDim.Render(fmt.Sprintf("连接 %q 仍将进行？ Enter 继续连接 / Esc 取消", name)) + "\n")
	} else if m.confirmID != "" {
		h := m.store.Find(m.confirmID)
		name := ""
		if h != nil {
			name = h.Name
		}
		b.WriteString("\n" + styleInfo.Render(fmt.Sprintf("确认删除连接 %q？ (y/N)", name)) + "\n")
	} else if m.err != "" {
		b.WriteString("\n" + styleError.Render(m.err) + "\n")
		m.err = ""
	}

	b.WriteString("\n" + renderListFooter())
	return tea.NewView(lipgloss.NewStyle().Padding(1, 2).Render(b.String()))
}

func renderListHeader() string {
	name := styleHeader.Render(padRight("名称", 24))
	target := styleHeader.Render(padRight("目标", 34))
	auth := styleHeader.Render("认证")
	return lipgloss.JoinHorizontal(lipgloss.Top, name, target, auth) + "\n" +
		styleDim.Render(strings.Repeat("─", 68)) + "\n"
}

func (m *listModel) renderRow(h *model.Host, idx int) string {
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

	name := padRight(h.Name, 24)
	target := padRight(h.User+"@"+h.Addr(), 34)
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
