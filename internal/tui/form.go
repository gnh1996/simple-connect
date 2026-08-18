package tui

import (
	"fmt"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/lipgloss"

	"simple-connect/internal/model"
	sshc "simple-connect/internal/ssh"
	"simple-connect/internal/store"
)

// formField 单个表单字段
type formField struct {
	label  string
	input  *textinput.Model
	choice *choiceField
}

// choiceField 单选字段
type choiceField struct {
	options []string
	index   int
}

// formModel 添加/编辑连接表单
type formModel struct {
	store    *store.Store
	fields   []*formField
	cursor   int
	editing  *model.Host // nil 表示新增
	password string
	err      string
}

func newFormModel(s *store.Store, editing *model.Host) *formModel {
	m := &formModel{store: s, editing: editing}

	nameInput := textInput("", "server-01")
	hostInput := textInput("", "192.168.1.100")
	portInput := textInput("22", "22")
	userInput := textInput("", "root")
	passInput := textInput("", "密码")
	passInput.EchoMode = textinput.EchoPassword
	keyInput := textInput("", "~/.ssh/id_ed25519")
	localInput := textInput("", "~")

	authField := &choiceField{options: []string{"密码", "私钥"}}

	if editing != nil {
		nameInput.SetValue(editing.Name)
		hostInput.SetValue(editing.Host)
		portInput.SetValue(strconv.Itoa(editing.Port))
		userInput.SetValue(editing.User)
		localInput.SetValue(editing.LocalDir)
		if editing.Auth == model.AuthKey {
			authField.index = 1
			keyInput.SetValue(editing.KeyPath)
		} else {
			authField.index = 0
		}
		if editing.HasPassword {
			passInput.Placeholder = "已保存（留空保持不变）"
		}
	}

	m.fields = []*formField{
		{label: "名称", input: &nameInput},
		{label: "主机", input: &hostInput},
		{label: "端口", input: &portInput},
		{label: "用户名", input: &userInput},
		{label: "认证方式", choice: authField},
		{label: "密码", input: &passInput},
		{label: "私钥路径", input: &keyInput},
		{label: "本地目录", input: &localInput},
	}
	return m
}

func textInput(value, placeholder string) textinput.Model {
	ti := textinput.New()
	ti.Placeholder = placeholder
	ti.Prompt = ""
	ti.CharLimit = 256
	// 必须显式设置宽度：v2 的 textinput 在 width=0 时 placeholder 只渲染首字符
	// （占位提示几乎不可见），且长输入不滚动截断会溢出折行。
	ti.SetWidth(40)
	st := textinput.DefaultStyles(false)
	st.Cursor.Blink = false
	ti.SetStyles(st)
	if value != "" {
		ti.SetValue(value)
	}
	return ti
}

func (m *formModel) Init() tea.Cmd {
	m.focus(0)
	return nil
}

func (m *formModel) focus(idx int) {
	for i, f := range m.fields {
		if f.input == nil {
			continue
		}
		if i == idx {
			f.input.Focus()
		} else {
			f.input.Blur()
		}
	}
}

func (m *formModel) Update(msg tea.Msg) (*formModel, tea.Cmd) {
	if m.cursor >= len(m.fields) {
		m.cursor = len(m.fields) - 1
	}
	f := m.fields[m.cursor]

	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		k := msg.Key()
		switch k.Code {
		case tea.KeyEsc:
			return m, tea.Cmd(func() tea.Msg { return backToListMsg{} })
		case tea.KeyTab:
			if k.Mod == tea.ModShift {
				return m.prev(), nil
			}
			if m.cursor == len(m.fields)-1 {
				return m.save()
			}
			return m.next(), nil
		case tea.KeyDown, tea.KeyEnter:
			if m.cursor == len(m.fields)-1 {
				return m.save()
			}
			return m.next(), nil
		case tea.KeyUp:
			return m.prev(), nil
		case tea.KeyLeft, tea.KeyRight:
			if f.choice != nil && (k.Code == tea.KeyLeft || k.Code == tea.KeyRight) {
				n := len(f.choice.options)
				if k.Code == tea.KeyLeft {
					f.choice.index = (f.choice.index - 1 + n) % n
				} else {
					f.choice.index = (f.choice.index + 1) % n
				}
				return m, nil
			}
		}
	}

	// 转发给当前聚焦的输入框
	if f.input != nil {
		in, cmd := f.input.Update(msg)
		*f.input = in
		return m, cmd
	}
	return m, nil
}

func (m *formModel) next() *formModel {
	m.cursor = (m.cursor + 1) % len(m.fields)
	m.focus(m.cursor)
	return m
}

func (m *formModel) prev() *formModel {
	m.cursor = (m.cursor - 1 + len(m.fields)) % len(m.fields)
	m.focus(m.cursor)
	return m
}

func (m *formModel) save() (*formModel, tea.Cmd) {
	name := strings.TrimSpace(m.fields[0].input.Value())
	host := strings.TrimSpace(m.fields[1].input.Value())
	portStr := strings.TrimSpace(m.fields[2].input.Value())
	user := strings.TrimSpace(m.fields[3].input.Value())
	pass := m.fields[5].input.Value()
	keyPath := strings.TrimSpace(m.fields[6].input.Value())
	localDir := strings.TrimSpace(m.fields[7].input.Value())

	if name == "" {
		m.err = "名称不能为空"
		return m, nil
	}
	if host == "" {
		m.err = "主机不能为空"
		return m, nil
	}
	if user == "" {
		m.err = "用户名不能为空"
		return m, nil
	}
	port := 22
	if portStr != "" {
		p, err := strconv.Atoi(portStr)
		if err != nil || p < 1 || p > 65535 {
			m.err = "端口无效"
			return m, nil
		}
		port = p
	}

	h := &model.Host{
		Name:     name,
		Host:     host,
		Port:     port,
		User:     user,
		LocalDir: sshc.ExpandPath(localDir),
	}

	if m.fields[4].choice.index == 1 {
		if keyPath == "" {
			m.err = "请填写私钥路径"
			return m, nil
		}
		h.Auth = model.AuthKey
		h.KeyPath = sshc.ExpandPath(keyPath)
	} else {
		h.Auth = model.AuthPassword
	}

	var err error
	if m.editing != nil {
		h.ID = m.editing.ID
		h.HasPassword = m.editing.HasPassword
		if pass != "" {
			err = m.store.SetPassword(h, pass)
		} else {
			err = m.store.Update(h)
		}
	} else {
		err = m.store.Add(h)
		if err == nil && pass != "" {
			err = m.store.SetPassword(h, pass)
		}
	}

	if err != nil {
		m.err = err.Error()
		return m, nil
	}
	return m, tea.Cmd(func() tea.Msg { return formSavedMsg{} })
}

func (m *formModel) View() tea.View {
	var b strings.Builder

	if m.editing == nil {
		b.WriteString(styleTitle.Render("添加连接") + "\n\n")
	} else {
		b.WriteString(styleTitle.Render(fmt.Sprintf("编辑连接: %s", m.editing.Name)) + "\n\n")
	}

	for i, f := range m.fields {
		cursorMark := "  "
		if i == m.cursor {
			cursorMark = styleCursor.Render("▸ ")
		}
		label := styleFormLabel.Render(f.label)
		if f.choice != nil {
			var opts []string
			for j, o := range f.choice.options {
				mark := "○"
				if j == f.choice.index {
					mark = "●"
				}
				opts = append(opts, fmt.Sprintf("%s %s", mark, o))
			}
			line := cursorMark + label + styleDim.Render(strings.Join(opts, "    "))
			if i == m.cursor {
				line += styleHint.Render("   ←/→ 切换")
			}
			b.WriteString(line + "\n")
		} else {
			b.WriteString(cursorMark + label + f.input.View() + "\n")
		}
	}

	if m.err != "" {
		b.WriteString("\n" + styleError.Render(m.err) + "\n")
		m.err = ""
	}

	b.WriteString("\n" + styleFooter.Render(styleDim.Render("Enter 保存  Tab/↓ 下一个  ↑ 上一个  Esc 取消")) + "\n")
	return tea.NewView(lipgloss.NewStyle().Padding(1, 2).Render(b.String()))
}
