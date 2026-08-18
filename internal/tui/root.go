package tui

import (
	tea "charm.land/bubbletea/v2"

	"simple-connect/internal/model"
	sshc "simple-connect/internal/ssh"
	"simple-connect/internal/store"
)

// 页面枚举
type page int

const (
	pageList page = iota
	pageForm
	pageSFTP
)

// 退出后主程序要执行的动作
type Action int

const (
	ActionNone Action = iota
	ActionQuit
	ActionSSH
)

// 页面间消息
type navListMsg struct{}
type navFormMsg struct{ host *model.Host } // host 为 nil 表示新增
type navSFTPMsg struct{ host *model.Host }
type backToListMsg struct{}
type formSavedMsg struct{}

// 连接与退出
type connectMsg struct{ host *model.Host }
type quitMsg struct{}

// 状态检测
type statusResultMsg struct{ results map[string]sshc.Status }
type statusTickMsg struct{}

// Root 主模型：持有各页面并负责路由
type Root struct {
	Store *store.Store

	page   page
	list   *listModel
	form   *formModel
	sftp   *sftpModel
	Action Action
	HostID string
}

// NewRoot 创建根模型
func NewRoot(s *store.Store) *Root {
	return &Root{
		Store: s,
		page:  pageList,
		list:  newListModel(s),
	}
}

func (m *Root) Init() tea.Cmd {
	return m.list.Init()
}

func (m *Root) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case connectMsg:
		m.Action = ActionSSH
		m.HostID = msg.host.ID
		return m, tea.Cmd(func() tea.Msg { return tea.Quit() })
	case quitMsg:
		m.Action = ActionQuit
		return m, tea.Cmd(func() tea.Msg { return tea.Quit() })
	case navFormMsg:
		m.form = newFormModel(m.Store, msg.host)
		m.page = pageForm
		return m, m.form.Init()
	case navSFTPMsg:
		m.sftp = newSFTPModel(m.Store, msg.host)
		m.page = pageSFTP
		return m, m.sftp.Init()
	case navListMsg, backToListMsg, formSavedMsg:
		if m.sftp != nil {
			m.sftp.close()
			m.sftp = nil
		}
		m.page = pageList
		m.list.reload(m.Store.Hosts())
		return m, m.list.Init()
	}

	var cmd tea.Cmd
	switch m.page {
	case pageForm:
		fm, c := m.form.Update(msg)
		m.form = fm
		cmd = c
	case pageSFTP:
		sm, c := m.sftp.Update(msg)
		m.sftp = sm
		cmd = c
	default:
		lm, c := m.list.Update(msg)
		m.list = lm
		cmd = c
	}
	return m, cmd
}

func (m *Root) View() tea.View {
	var v tea.View
	switch m.page {
	case pageForm:
		v = m.form.View()
	case pageSFTP:
		v = m.sftp.View()
	default:
		v = m.list.View()
	}
	v.AltScreen = true
	return v
}
