package tui

import (
	tea "charm.land/bubbletea/v2"

	"simple-connect/internal/model"
	"simple-connect/internal/session"
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
	// ActionResumeSSH 从 SFTP 页返回后请求重连会话（会话中热键唤起场景）
	ActionResumeSSH
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

// NewSFTPRoot 创建直接进入 SFTP 页的根模型（会话中热键唤起用）。
// sess 为挂起的会话 Handle（复用其 SSH 连接，并用跟踪到的远程 cwd 定位）；
// nil 表示列表页独立进入（SFTP 用默认路径 + 独立连接）。
func NewSFTPRoot(s *store.Store, h *model.Host, sess *session.Handle) *Root {
	var sshCl *sshc.Client
	remoteCwd := ""
	if sess != nil {
		sshCl = sess.SSHClient()
		remoteCwd = sess.Cwd()
	}
	sm := newSFTPModel(s, h, remoteCwd, sshCl)
	sm.fromSession = sess != nil
	return &Root{
		Store: s,
		page:  pageSFTP,
		sftp:  sm,
	}
}

func (m *Root) Init() tea.Cmd {
	switch m.page {
	case pageSFTP:
		if m.sftp != nil {
			return m.sftp.Init()
		}
	case pageForm:
		if m.form != nil {
			return m.form.Init()
		}
	}
	if m.list != nil {
		return m.list.Init()
	}
	return nil
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
		m.sftp = newSFTPModel(m.Store, msg.host, "", nil) // 列表页唤起无会话 cwd，用默认路径
		m.page = pageSFTP
		return m, m.sftp.Init()
	case backToListMsg:
		if m.sftp != nil && m.sftp.fromSession {
			m.sftp.close()
			m.sftp = nil
			m.Action = ActionResumeSSH
			return m, tea.Cmd(func() tea.Msg { return tea.Quit() })
		}
		return m.returnToList()
	case navListMsg, formSavedMsg:
		return m.returnToList()
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

func (m *Root) returnToList() (tea.Model, tea.Cmd) {
	if m.sftp != nil {
		m.sftp.close()
		m.sftp = nil
	}
	m.page = pageList
	_ = m.Store.Reload() // 回到列表时同步最新配置（多实例并发编辑可见）
	m.list.reload(m.Store.Hosts())
	return m, m.list.Init()
}

func (m *Root) View() tea.View {
	var v tea.View
	switch m.page {
	case pageForm:
		if m.form != nil {
			v = m.form.View()
		}
	case pageSFTP:
		if m.sftp != nil {
			v = m.sftp.View()
		}
	default:
		if m.list != nil {
			v = m.list.View()
		}
	}
	v.AltScreen = true
	return v
}
