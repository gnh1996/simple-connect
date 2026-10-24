package tui

import (
	"os"
	"path/filepath"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"

	"simple-connect/internal/store"
)

// vaultModel 是“密码库”页：展示/修改密钥路径，或在密钥丢失时重置密码库。
// 页面本身不接触密码明文，只调用 store 的验证/重置接口。
type vaultModel struct {
	store   *store.Store
	input   textinput.Model
	info    store.VaultInfo
	canBack bool

	err          string
	notice       string
	confirmReset bool
	resetTyped   string
}

func newVaultModel(s *store.Store, canBack bool) *vaultModel {
	info := s.VaultInfo()
	in := textInput(info.KeyPath, info.DefaultKeyPath)
	in.CharLimit = 512
	in.SetWidth(vaultInputWidth(0))
	in.Focus()
	return &vaultModel{store: s, input: in, info: info, canBack: canBack}
}

func vaultInputWidth(termWidth int) int {
	const def = 64
	if termWidth <= 0 {
		return def
	}
	w := termWidth - 14
	if w < 20 {
		return 20
	}
	if w > 100 {
		return 100
	}
	return w
}

func (m *vaultModel) Init() tea.Cmd {
	m.input.Focus()
	return nil
}

func (m *vaultModel) Update(msg tea.Msg) (*vaultModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.input.SetWidth(vaultInputWidth(msg.Width))
		return m, nil
	case tea.KeyPressMsg:
		m.err = ""
		if m.confirmReset {
			return m.handleResetKey(msg)
		}
		k := msg.Key()
		switch {
		case k.Mod.Contains(tea.ModCtrl) && k.Code == 'r':
			m.err = ""
			m.notice = ""
			m.confirmReset = true
			m.resetTyped = ""
			return m, nil
		case k.Code == tea.KeyEsc:
			if m.canBack {
				return m, tea.Cmd(func() tea.Msg { return vaultBackMsg{} })
			}
			return m, tea.Cmd(func() tea.Msg { return vaultQuitMsg{} })
		case k.Code == tea.KeyEnter:
			return m.save()
		}
	}
	in, cmd := m.input.Update(msg)
	m.input = in
	return m, cmd
}

func (m *vaultModel) save() (*vaultModel, tea.Cmd) {
	m.err = ""
	m.notice = ""
	if err := m.store.SetKeyPath(m.input.Value()); err != nil {
		m.err = err.Error()
		m.info = m.store.VaultInfo()
		return m, nil
	}
	m.info = m.store.VaultInfo()
	m.input.SetValue(m.info.KeyPath)
	return m, tea.Cmd(func() tea.Msg { return vaultReadyMsg{} })
}

func (m *vaultModel) handleResetKey(msg tea.KeyPressMsg) (*vaultModel, tea.Cmd) {
	k := msg.Key()
	switch k.Code {
	case tea.KeyEsc:
		m.confirmReset = false
		m.resetTyped = ""
		m.err = ""
		return m, nil
	case tea.KeyEnter:
		if strings.TrimSpace(m.resetTyped) != "RESET" {
			m.err = "请输入 RESET 确认（区分大小写）"
			return m, nil
		}
		warning, err := m.store.ResetVault()
		m.info = m.store.VaultInfo()
		m.confirmReset = false
		m.resetTyped = ""
		if err != nil {
			m.err = err.Error()
			return m, nil
		}
		m.err = ""
		m.canBack = true
		m.input.SetValue(m.info.KeyPath)
		m.notice = "密码库已重置，旧密码无法恢复。按 Enter 进入列表。"
		if warning != "" {
			m.notice += "\n注意: " + warning
		}
		return m, nil
	case tea.KeyBackspace:
		if m.resetTyped != "" {
			r := []rune(m.resetTyped)
			m.resetTyped = string(r[:len(r)-1])
		}
		return m, nil
	default:
		if k.Text != "" && len([]rune(m.resetTyped)) < 10 {
			m.resetTyped += k.Text
		}
		return m, nil
	}
}

func (m *vaultModel) View() tea.View {
	var b strings.Builder
	b.WriteString(styleTitle.Render("密码库") + "\n\n")

	info := m.info
	b.WriteString(styleFormLabel.Render("密码本") + styleDim.Render(info.VaultPath) + "\n")

	status := "已解锁"
	statusStyle := styleOK
	if !info.Ready {
		status = "未解锁"
		if info.Problem != "" {
			status += ": " + info.Problem
		}
		statusStyle = styleError
	}
	b.WriteString(styleFormLabel.Render("状态") + statusStyle.Render(status) + "\n")

	if info.KeyPermWarning != "" {
		b.WriteString(styleError.Render(info.KeyPermWarning) + "\n")
	}
	if info.Ready && info.KeyInConfigDir {
		b.WriteString(styleInfo.Render("密钥与密码本同目录，整目录备份/同步会同时包含两者；建议移到 U 盘或独立目录。") + "\n")
	}
	b.WriteString("\n")

	if m.confirmReset {
		b.WriteString(m.resetConfirmView())
		return tea.NewView(lipgloss.NewStyle().Padding(1, 2).Render(b.String()))
	}

	b.WriteString(styleFormLabel.Render("密钥") + m.input.View() + "\n")
	b.WriteString(styleHint.Render("  默认: "+info.DefaultKeyPath) + "\n\n")

	if m.notice != "" {
		b.WriteString(styleInfo.Render(m.notice) + "\n\n")
	}
	if m.err != "" {
		b.WriteString(styleError.Render(m.err) + "\n\n")
	}
	if !info.Ready {
		b.WriteString(styleHint.Render("密钥丢失时密码本无法恢复，只能重置后重新输入服务器密码。") + "\n\n")
	}
	b.WriteString(styleFooter.Render(styleDim.Render("Enter 保存并验证  Ctrl+R 重置密码库  Esc 返回/退出")) + "\n")
	return tea.NewView(lipgloss.NewStyle().Padding(1, 2).Render(b.String()))
}

func (m *vaultModel) resetConfirmView() string {
	var b strings.Builder
	b.WriteString(styleError.Render("确认重置密码库？此操作不可恢复。") + "\n\n")
	b.WriteString(styleDim.Render("将删除（不匹配/无效的自定义密钥会保留）：") + "\n")
	b.WriteString("  " + m.info.VaultPath + "\n")
	b.WriteString("  " + m.info.VaultPath + ".bak\n")
	if m.info.KeyPath != "" {
		b.WriteString("  " + m.info.KeyPath + "\n")
	}
	b.WriteString("\n" + styleDim.Render("然后生成新的密钥对和空密码本：") + "\n")
	b.WriteString("  新密钥: " + m.info.DefaultKeyPath + "\n\n")
	b.WriteString(styleDim.Render("旧版 secrets.json / 旧 keyring 条目不属于新版密码本；如需清理请手动处理。") + "\n")
	b.WriteString(styleInfo.Render("主机列表保留；旧密码不会、也无法恢复，需要重新输入。") + "\n\n")
	b.WriteString(styleDim.Render("输入 RESET 确认: ") + m.resetTyped + styleCursor.Render("▌") + "\n\n")
	if m.err != "" {
		b.WriteString(styleError.Render(m.err) + "\n\n")
	}
	b.WriteString(styleFooter.Render(styleDim.Render("Enter 确认重置  Esc 取消")) + "\n")
	return b.String()
}

// vaultStatusLines 渲染列表页的密码库/密钥路径状态（含独立保管提示）。
func vaultStatusLines(info store.VaultInfo, width int) []string {
	if !info.Ready {
		return nil
	}
	avail := width - 4 // 外层 Padding(1,2) 的左右各 2 列
	if avail <= 0 {
		return []string{
			styleDim.Render("密码库 " + contractHomePath(info.VaultPath)),
			styleDim.Render("密钥   " + contractHomePath(info.KeyPath) + independentHint(info)),
		}
	}

	vaultLine := styleDim.Render(truncateDisplay("密码库 "+contractHomePath(info.VaultPath), avail))

	// 优先保证“建议独立保管”完整显示：先按提示宽度预留，再截断密钥路径。
	// 终端极窄、同一行放不下路径时改为分行，绝不把提示截掉。
	const prefix = "密钥   "
	hint := independentHint(info)
	keyPath := contractHomePath(info.KeyPath)
	prefixW := runewidth.StringWidth(prefix)
	hintW := runewidth.StringWidth(hint)

	if pathW := avail - prefixW - hintW; hint == "" || pathW >= 8 {
		if pathW < 0 {
			pathW = 0
		}
		keyLine := styleDim.Render(prefix+truncateDisplay(keyPath, pathW)) + styleInfo.Render(hint)
		return []string{vaultLine, keyLine}
	}

	pathW := avail - prefixW
	if pathW < 0 {
		pathW = 0
	}
	return []string{
		vaultLine,
		styleDim.Render(prefix + truncateDisplay(keyPath, pathW)),
		styleInfo.Render(hint),
	}
}

func independentHint(info store.VaultInfo) string {
	if info.KeyInConfigDir {
		return "  [建议独立保管]"
	}
	return ""
}

func contractHomePath(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	home, p = filepath.Clean(home), filepath.Clean(p)
	if p == home {
		return "~"
	}
	sep := string(filepath.Separator)
	if strings.HasPrefix(p, home+sep) {
		return "~" + p[len(home):]
	}
	return p
}

func truncateDisplay(s string, width int) string {
	if width <= 0 {
		return ""
	}
	return runewidth.Truncate(s, width, "…")
}
