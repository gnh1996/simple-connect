package tui

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/lipgloss"
	"golang.org/x/crypto/ssh"

	"simple-connect/internal/model"
	sftpc "simple-connect/internal/sftp"
	sshc "simple-connect/internal/ssh"
	"simple-connect/internal/store"
)

// SFTP 页面模式
type sftpMode int

const (
	modeBrowse sftpMode = iota
	modeUpload
	modeNewDir
)

// SFTP 页面消息
type sftpConnMsg struct {
	conn *sftpc.Conn
	err  error
}
type sftpListMsg struct {
	path    string
	entries []fs.FileInfo
	err     error
	notice  string // 可选：列表刷新时显示的提示
}
type sftpProgressMsg struct{}
type sftpMsgText struct {
	text string
	ok   bool
}

// sftpModel 远程文件浏览/传输模型
type sftpModel struct {
	store *store.Store
	host  *model.Host
	conn  *sftpc.Conn

	hostKeyCallback ssh.HostKeyCallback // 测试可注入

	cwd      string
	entries  []fs.FileInfo
	cursor   int
	localCwd string

	mode      sftpMode
	uploadIn  *textinput.Model
	newDirIn  *textinput.Model
	confirmID int // 待删除条目下标，-1 表示无

	fromSession bool // 会话中热键唤起：q 返回时请求重连会话

	transfer *sftpc.Transfer
	status   string
	err      string
	busy     bool
}

func newSFTPModel(s *store.Store, h *model.Host) *sftpModel {
	up := textInput("", "本地文件路径，可直接拖拽文件到终端")
	nw := textInput("", "新目录名")
	m := &sftpModel{
		store: s, host: h,
		uploadIn: &up, newDirIn: &nw,
		confirmID: -1,
	}
	if h.LocalDir != "" {
		m.localCwd = h.LocalDir
	} else if home, err := os.UserHomeDir(); err == nil {
		m.localCwd = home
	} else {
		m.localCwd = "."
	}
	return m
}

func (m *sftpModel) close() {
	if m.conn != nil {
		m.conn.Close()
		m.conn = nil
	}
}

func (m *sftpModel) Init() tea.Cmd {
	return tea.Cmd(func() tea.Msg {
		pass, _ := m.store.Password(m.host)
		opts := []sshc.Option{}
		if m.hostKeyCallback != nil {
			opts = append(opts, sshc.WithHostKeyCallback(m.hostKeyCallback))
		}
		conn, err := sftpc.Dial(m.host, pass, opts...)
		if err != nil {
			return sftpConnMsg{err: err}
		}
		return sftpConnMsg{conn: conn}
	})
}

func (m *sftpModel) Update(msg tea.Msg) (*sftpModel, tea.Cmd) {
	switch msg := msg.(type) {
	case sftpConnMsg:
		if msg.err != nil {
			m.err = fmt.Sprintf("连接失败: %v", msg.err)
			return m, nil
		}
		m.conn = msg.conn
		if cwd, err := m.conn.Client.Getwd(); err == nil {
			m.cwd = cwd
		} else {
			m.cwd = "/"
		}
		m.busy = true
		return m, m.loadList()

	case sftpListMsg:
		m.busy = false
		if msg.err != nil {
			m.err = fmt.Sprintf("读取目录失败: %v", msg.err)
			return m, nil
		}
		if msg.path == m.cwd {
			m.entries = msg.entries
			m.clampCursor()
		}
		if msg.notice != "" {
			m.status = msg.notice
		}
		return m, nil

	case sftpProgressMsg:
		return m.handleProgress()

	case sftpMsgText:
		if msg.ok {
			m.status = msg.text
		} else {
			m.err = msg.text
		}
		return m, nil

	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m *sftpModel) handleKey(msg tea.KeyPressMsg) (*sftpModel, tea.Cmd) {
	k := msg.Key()

	// 确认删除
	if m.confirmID >= 0 {
		switch {
		case k.Code == tea.KeyEnter:
			fallthrough
		case k.Text == "y" || k.Text == "Y":
			return m.doDelete()
		case k.Code == tea.KeyEsc || k.Text == "n" || k.Text == "N" || k.Text == "q":
			m.confirmID = -1
		}
		return m, nil
	}

	// 上传路径输入
	if m.mode == modeUpload {
		switch k.Code {
		case tea.KeyEsc:
			m.mode = modeBrowse
			return m, nil
		case tea.KeyEnter:
			p := strings.TrimSpace(m.uploadIn.Value())
			if p == "" {
				m.mode = modeBrowse
				return m, nil
			}
			if st, err := os.Stat(sshc.ExpandPath(p)); err != nil || st.IsDir() {
				m.err = "本地文件不存在"
				return m, nil
			}
			m.mode = modeBrowse
			return m, m.startUpload(sshc.ExpandPath(p))
		}
		in, cmd := m.uploadIn.Update(msg)
		*m.uploadIn = in
		return m, cmd
	}

	// 新建目录输入
	if m.mode == modeNewDir {
		switch k.Code {
		case tea.KeyEsc:
			m.mode = modeBrowse
			return m, nil
		case tea.KeyEnter:
			name := strings.TrimSpace(m.newDirIn.Value())
			m.mode = modeBrowse
			if name == "" {
				return m, nil
			}
			return m, m.mkdir(name)
		}
		in, cmd := m.newDirIn.Update(msg)
		*m.newDirIn = in
		return m, cmd
	}

	// 浏览模式
	switch k.Code {
	case tea.KeyUp:
		if len(m.entries) > 0 {
			m.cursor = (m.cursor - 1 + len(m.entries)) % len(m.entries)
		}
	case tea.KeyDown:
		if len(m.entries) > 0 {
			m.cursor = (m.cursor + 1) % len(m.entries)
		}
	case tea.KeyTab:
		if k.Mod.Contains(tea.ModShift) {
			if len(m.entries) > 0 {
				m.cursor = (m.cursor - 1 + len(m.entries)) % len(m.entries)
			}
		} else if len(m.entries) > 0 {
			m.cursor = (m.cursor + 1) % len(m.entries)
		}
	case tea.KeyPgUp:
		m.cursor -= 10
		if m.cursor < 0 {
			m.cursor = 0
		}
	case tea.KeyPgDown:
		if m.cursor+10 >= len(m.entries) {
			m.cursor = len(m.entries) - 1
		} else {
			m.cursor += 10
		}
	case tea.KeyEnter:
		return m.enterDir()
	case tea.KeyBackspace:
		return m.goUp()
	default:
		if k.Text != "" {
			switch k.Text {
			case "u":
				if m.busy {
					return m, nil
				}
				m.mode = modeUpload
				m.uploadIn.Focus()
				return m, nil
			case "n":
				if m.busy {
					return m, nil
				}
				m.mode = modeNewDir
				m.newDirIn.Focus()
				return m, nil
			case "d":
				return m.download()
			case "x":
				if len(m.entries) > 0 && !m.busy {
					m.confirmID = m.cursor
				}
			case "r":
				if m.busy {
					return m, nil
				}
				m.busy = true
				return m, m.loadList()
			case "q":
				return m, tea.Cmd(func() tea.Msg { return backToListMsg{} })
			}
		}
	}
	return m, nil
}

func (m *sftpModel) loadList() tea.Cmd {
	cl, p := m.conn.Client, m.cwd
	return func() tea.Msg {
		entries, err := sftpc.List(cl, p)
		return sftpListMsg{path: p, entries: entries, err: err}
	}
}

func (m *sftpModel) clampCursor() {
	if len(m.entries) == 0 {
		m.cursor = 0
		return
	}
	if m.cursor >= len(m.entries) {
		m.cursor = len(m.entries) - 1
	}
}

func (m *sftpModel) enterDir() (*sftpModel, tea.Cmd) {
	if len(m.entries) == 0 {
		return m, nil
	}
	e := m.entries[m.cursor]
	if !e.IsDir() {
		m.status = e.Name()
		return m, nil
	}
	m.cwd = path.Join(m.cwd, e.Name())
	m.cursor = 0
	m.busy = true
	return m, m.loadList()
}

func (m *sftpModel) goUp() (*sftpModel, tea.Cmd) {
	parent := path.Dir(m.cwd)
	if parent == m.cwd {
		return m, nil
	}
	m.cwd = parent
	m.cursor = 0
	m.busy = true
	m.entries = nil
	return m, m.loadList()
}

func (m *sftpModel) mkdir(name string) tea.Cmd {
	dir := path.Join(m.cwd, name)
	cl := m.conn.Client
	p := m.cwd
	return func() tea.Msg {
		if err := cl.Mkdir(dir); err != nil {
			return sftpMsgText{text: fmt.Sprintf("创建目录失败: %v", err)}
		}
		entries, err := sftpc.List(cl, p)
		return sftpListMsg{path: p, entries: entries, err: err,
			notice: fmt.Sprintf("已创建 %s", dir)}
	}
}

func (m *sftpModel) download() (*sftpModel, tea.Cmd) {
	if len(m.entries) == 0 || m.busy {
		return m, nil
	}
	e := m.entries[m.cursor]
	if e.IsDir() {
		m.status = "暂不支持目录递归下载"
		return m, nil
	}
	if err := os.MkdirAll(m.localCwd, 0o755); err != nil {
		m.err = err.Error()
		return m, nil
	}
	remote := path.Join(m.cwd, e.Name())
	local := filepath.Join(m.localCwd, e.Name())
	return m, m.startTransfer(remote, local, false)
}

func (m *sftpModel) startUpload(localPath string) tea.Cmd {
	remote := path.Join(m.cwd, filepath.Base(localPath))
	return m.startTransfer(localPath, remote, true)
}

// startTransfer 异步执行传输并返回进度命令
func (m *sftpModel) startTransfer(src, dst string, up bool) tea.Cmd {
	t := sftpc.NewTransfer(filepath.Base(dst), up)
	m.transfer = t
	m.busy = true
	m.status = ""

	cl := m.conn.Client
	if up {
		go sftpc.Upload(cl, t, src, dst)
	} else {
		go sftpc.Download(cl, t, src, dst)
	}
	return tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg {
		return sftpProgressMsg{}
	})
}

func (m *sftpModel) handleProgress() (*sftpModel, tea.Cmd) {
	if m.transfer == nil {
		return m, nil
	}
	_, _, finished, err := m.transfer.Snapshot()
	if finished {
		up := m.transfer.Up
		done, _, _, _ := m.transfer.Snapshot()
		m.transfer = nil
		m.busy = false
		if err != nil {
			m.err = fmt.Sprintf("%s失败: %v", transferName(up), err)
			return m, nil
		}
		m.status = fmt.Sprintf("%s完成 %s", transferName(up), sftpc.FormatSize(done))
		m.cursor = 0
		return m, m.loadList()
	}
	// 继续轮询
	return m, tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg {
		return sftpProgressMsg{}
	})
}

func transferName(up bool) string {
	if up {
		return "上传"
	}
	return "下载"
}

func (m *sftpModel) doDelete() (*sftpModel, tea.Cmd) {
	idx := m.confirmID
	m.confirmID = -1
	if idx < 0 || idx >= len(m.entries) {
		return m, nil
	}
	e := m.entries[idx]
	remote := path.Join(m.cwd, e.Name())
	p := m.cwd
	cl := m.conn.Client
	return m, tea.Cmd(func() tea.Msg {
		if err := sftpc.Remove(cl, remote, e.IsDir()); err != nil {
			return sftpMsgText{text: fmt.Sprintf("删除失败: %v", err)}
		}
		entries, err := sftpc.List(cl, p)
		return sftpListMsg{path: p, entries: entries, err: err,
			notice: fmt.Sprintf("已删除 %s", remote)}
	})
}

func (m *sftpModel) View() tea.View {
	var b strings.Builder

	head := styleTitle.Render("SFTP 文件浏览器") + "  " +
		styleDim.Render(m.host.Name+" ("+m.host.User+"@"+m.host.Host+")") + "\n"
	b.WriteString(head)
	b.WriteString(styleDim.Render("远程: "+m.cwd+"    本地: "+m.localCwd) + "\n\n")

	// 目录列表
	b.WriteString(renderSFTPHeader())
	for i, e := range m.entries {
		b.WriteString(m.renderEntry(e, i) + "\n")
	}

	// 删除确认
	if m.confirmID >= 0 && m.confirmID < len(m.entries) {
		b.WriteString("\n" + styleInfo.Render(fmt.Sprintf("确认删除 %q？ (y/N)", m.entries[m.confirmID].Name())) + "\n")
	}

	// 输入模式
	if m.mode == modeUpload {
		b.WriteString("\n" + styleCursor.Render("上传 ") + "本地路径(可拖拽文件): " + m.uploadIn.View() + "\n")
	} else if m.mode == modeNewDir {
		b.WriteString("\n" + styleCursor.Render("新建目录: ") + m.newDirIn.View() + "\n")
	}

	// 状态/进度
	if m.transfer != nil {
		done, total, _, _ := m.transfer.Snapshot()
		b.WriteString("\n" + styleInfo.Render(renderProgress(m.transfer.Name, done, total)) + "\n")
	} else if m.status != "" {
		b.WriteString("\n" + styleOK.Render(m.status) + "\n")
	}
	if m.err != "" {
		b.WriteString("\n" + styleError.Render(m.err) + "\n")
		m.err = ""
	}

	b.WriteString("\n" + styleFooter.Render(styleDim.Render(renderSFTPFooter())))
	return tea.NewView(lipgloss.NewStyle().Padding(1, 2).Render(b.String()))
}

func renderSFTPHeader() string {
	name := styleHeader.Render(padRight("名称", 28))
	size := styleHeader.Render(padRight("大小", 12))
	mtime := styleHeader.Render("修改时间")
	return lipgloss.JoinHorizontal(lipgloss.Top, name, size, mtime) + "\n" +
		styleDim.Render(strings.Repeat("─", 68)) + "\n"
}

func (m *sftpModel) renderEntry(e fs.FileInfo, idx int) string {
	nameStr := e.Name()
	if e.IsDir() {
		nameStr += "/"
	}
	mark := "  "
	if idx == m.cursor {
		mark = styleCursor.Render("▸ ")
	}
	name := padRight(nameStr, 28)
	size := padRight(sftpc.FormatSize(e.Size()), 12)
	if e.IsDir() {
		size = padRight("-", 12)
	}
	line := lipgloss.JoinHorizontal(lipgloss.Top,
		mark+name,
		styleDim.Render(size),
		styleDim.Render(e.ModTime().Format("01-02 15:04")),
	)
	return line
}

func renderProgress(name string, done, total int64) string {
	if total <= 0 {
		return fmt.Sprintf("正在传输 %s ... %s", name, sftpc.FormatSize(done))
	}
	pct := float64(done) / float64(total) * 100
	const width = 20
	filled := int(pct / 100 * width)
	bar := strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
	return fmt.Sprintf("正在传输 %s  %3.0f%% %s %s", name, pct, bar, sftpc.FormatSize(done))
}

func renderSFTPFooter() string {
	return "Enter 进入  Backspace 上级  u 上传  n 新建目录  d 下载  x 删除  r 刷新  q 返回"
}
