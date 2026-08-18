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
	"github.com/mattn/go-runewidth"
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

// 栏焦点
const (
	paneLocal = iota // 本地栏
	paneRemote       // 远程栏
)

// SFTP 页面消息
type sftpConnMsg struct {
	conn *sftpc.Conn
	err  error
}

// sftpListMsg 目录列表结果（kind 区分本地/远程栏）
type sftpListMsg struct {
	kind    int // paneLocal / paneRemote
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

// sftpModel 双栏（本地 | 远程）文件浏览/传输模型。
// 远程栏：cwd/entries/cursor/confirmID（字段名向后兼容测试）；
// 本地栏：localCwd/localEntries/localCursor/localConfirmID。
type sftpModel struct {
	store *store.Store
	host  *model.Host
	conn  *sftpc.Conn

	hostKeyCallback ssh.HostKeyCallback // 测试可注入

	// 远程栏
	cwd      string
	entries  []fs.FileInfo
	cursor   int
	remoteTop int // 滚动窗口顶部
	confirmID int // 待删除条目下标，-1 表示无

	// 本地栏
	localCwd        string
	localEntries    []fs.FileInfo
	localCursor     int
	localTop        int
	localConfirmID  int // 待删除条目下标，-1 表示无

	focus int // 当前焦点栏（paneLocal / paneRemote）

	remoteCwd string // 会话内跟踪到的远程工作目录（热键唤起定位用，空串=默认）

	mode     sftpMode
	uploadIn *textinput.Model
	newDirIn *textinput.Model

	fromSession bool // 会话中热键唤起：q 返回时请求重连会话

	transfer *sftpc.Transfer
	status   string
	err      string
	busy     bool

	width  int // 终端尺寸（WindowSizeMsg）
	height int
}

func newSFTPModel(s *store.Store, h *model.Host, remoteCwd string) *sftpModel {
	up := textInput("", "本地文件路径，可直接拖拽文件到终端")
	nw := textInput("", "新目录名")
	m := &sftpModel{
		store: s, host: h,
		uploadIn: &up, newDirIn: &nw,
		confirmID: -1, localConfirmID: -1,
		focus: paneRemote,
		remoteCwd: remoteCwd,
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
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
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
		// 会话热键唤起：优先定位到会话内跟踪到的目录
		if m.remoteCwd != "" {
			m.cwd = m.remoteCwd
		}
		m.busy = true
		return m, m.loadList()

	case sftpListMsg:
		m.busy = false
		if msg.err != nil {
			m.err = fmt.Sprintf("读取目录失败: %v", msg.err)
			return m, nil
		}
		if msg.kind == paneRemote {
			if msg.path == m.cwd {
				m.entries = msg.entries
				m.clampCursor()
			}
		} else {
			if msg.path == m.localCwd {
				m.localEntries = msg.entries
				m.clampLocalCursor()
			}
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

// ---- 列表加载 ----

// loadList 刷新远程栏
func (m *sftpModel) loadList() tea.Cmd {
	cl, p := m.conn.Client, m.cwd
	return func() tea.Msg {
		entries, err := sftpc.List(cl, p)
		return sftpListMsg{kind: paneRemote, path: p, entries: entries, err: err}
	}
}

// loadLocal 刷新本地栏
func (m *sftpModel) loadLocal() tea.Cmd {
	p := m.localCwd
	return func() tea.Msg {
		entries, err := os.ReadDir(p)
		if err != nil {
			return sftpListMsg{kind: paneLocal, path: p, err: err}
		}
		infos := make([]fs.FileInfo, 0, len(entries))
		for _, e := range entries {
			if info, err := e.Info(); err == nil {
				infos = append(infos, info)
			}
		}
		sftpc.SortEntries(infos)
		return sftpListMsg{kind: paneLocal, path: p, entries: infos}
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
	m.ensureVisible(&m.remoteTop, m.cursor)
}

func (m *sftpModel) clampLocalCursor() {
	if len(m.localEntries) == 0 {
		m.localCursor = 0
		return
	}
	if m.localCursor >= len(m.localEntries) {
		m.localCursor = len(m.localEntries) - 1
	}
	m.ensureVisible(&m.localTop, m.localCursor)
}

// ensureVisible 滚动窗口跟随光标
func (m *sftpModel) ensureVisible(top *int, cursor int) {
	body := m.bodyHeight()
	if body <= 0 {
		return
	}
	if cursor < *top {
		*top = cursor
	}
	if cursor >= *top+body {
		*top = cursor - body + 1
	}
}

// bodyHeight 列表可视行数（终端高度 - 标题/表头/底部固定行）
func (m *sftpModel) bodyHeight() int {
	if m.height <= 0 {
		return 0 // 未知尺寸：显示全部
	}
	h := m.height - 5 // 标题1 + 表头1 + 状态区3
	if h < 1 {
		h = 1
	}
	return h
}

// ---- 导航 ----

func (m *sftpModel) pane() int { return m.focus }

func (m *sftpModel) entryAt(idx int) fs.FileInfo {
	if m.focus == paneLocal {
		if idx < 0 || idx >= len(m.localEntries) {
			return nil
		}
		return m.localEntries[idx]
	}
	if idx < 0 || idx >= len(m.entries) {
		return nil
	}
	return m.entries[idx]
}

func (m *sftpModel) currentEntry() fs.FileInfo {
	if m.focus == paneLocal {
		return m.entryAt(m.localCursor)
	}
	return m.entryAt(m.cursor)
}

// enterDir 进入当前条目（目录）或触发传输（文件：本地→上传 / 远程→下载）
func (m *sftpModel) enterCurrent() (*sftpModel, tea.Cmd) {
	e := m.currentEntry()
	if e == nil {
		return m, nil
	}
	if e.IsDir() {
		if m.focus == paneLocal {
			m.localCwd = path.Join(m.localCwd, e.Name())
			m.localCursor = 0
			m.localEntries = nil
			m.busy = true
			return m, m.loadLocal()
		}
		m.cwd = path.Join(m.cwd, e.Name())
		m.cursor = 0
		m.entries = nil
		m.busy = true
		return m, m.loadList()
	}
	// 文件：本地栏 Enter=上传，远程栏 Enter=下载
	if m.focus == paneLocal {
		return m, m.uploadEntry(filepath.Join(m.localCwd, e.Name()))
	}
	return m.downloadEntry(e)
}

func (m *sftpModel) goUp() (*sftpModel, tea.Cmd) {
	if m.focus == paneLocal {
		parent := path.Dir(m.localCwd)
		if parent == m.localCwd {
			return m, nil
		}
		m.localCwd = parent
		m.localCursor = 0
		m.localEntries = nil
		m.busy = true
		return m, m.loadLocal()
	}
	parent := path.Dir(m.cwd)
	if parent == m.cwd {
		return m, nil
	}
	m.cwd = parent
	m.cursor = 0
	m.entries = nil
	m.busy = true
	return m, m.loadList()
}

// ---- 文件操作 ----

func (m *sftpModel) mkdir(name string) tea.Cmd {
	if m.focus == paneLocal {
		dir := path.Join(m.localCwd, name)
		p := m.localCwd
		return func() tea.Msg {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return sftpMsgText{text: fmt.Sprintf("创建目录失败: %v", err)}
			}
			entries, err := os.ReadDir(p)
			if err != nil {
				return sftpMsgText{text: fmt.Sprintf("读取目录失败: %v", err)}
			}
			infos := make([]fs.FileInfo, 0, len(entries))
			for _, e := range entries {
				if info, err := e.Info(); err == nil {
					infos = append(infos, info)
				}
			}
			sftpc.SortEntries(infos)
			return sftpListMsg{kind: paneLocal, path: p, entries: infos,
				notice: fmt.Sprintf("已创建 %s", dir)}
		}
	}
	dir := path.Join(m.cwd, name)
	cl := m.conn.Client
	p := m.cwd
	return func() tea.Msg {
		if err := cl.Mkdir(dir); err != nil {
			return sftpMsgText{text: fmt.Sprintf("创建目录失败: %v", err)}
		}
		entries, err := sftpc.List(cl, p)
		return sftpListMsg{kind: paneRemote, path: p, entries: entries, err: err,
			notice: fmt.Sprintf("已创建 %s", dir)}
	}
}

// uploadEntry 上传本地文件到远程栏当前目录
func (m *sftpModel) uploadEntry(localPath string) tea.Cmd {
	remote := path.Join(m.cwd, filepath.Base(localPath))
	return m.startTransfer(localPath, remote, true)
}

// downloadEntry 下载远程文件到本地栏当前目录
func (m *sftpModel) downloadEntry(e fs.FileInfo) (*sftpModel, tea.Cmd) {
	if m.busy {
		return m, nil
	}
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
		// 传输后刷新两侧列表（远程大小/时间可能变化；本地目录不变刷新无害）
		return m, tea.Batch(m.loadList(), m.loadLocal())
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
	if m.focus == paneLocal {
		idx := m.localConfirmID
		m.localConfirmID = -1
		if idx < 0 || idx >= len(m.localEntries) {
			return m, nil
		}
		e := m.localEntries[idx]
		local := filepath.Join(m.localCwd, e.Name())
		p := m.localCwd
		return m, tea.Cmd(func() tea.Msg {
			if err := os.RemoveAll(local); err != nil {
				return sftpMsgText{text: fmt.Sprintf("删除失败: %v", err)}
			}
			entries, err := os.ReadDir(p)
			if err != nil {
				return sftpMsgText{text: fmt.Sprintf("读取目录失败: %v", err)}
			}
			infos := make([]fs.FileInfo, 0, len(entries))
			for _, e := range entries {
				if info, err := e.Info(); err == nil {
					infos = append(infos, info)
				}
			}
			sftpc.SortEntries(infos)
			return sftpListMsg{kind: paneLocal, path: p, entries: infos,
				notice: fmt.Sprintf("已删除 %s", local)}
		})
	}
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
		return sftpListMsg{kind: paneRemote, path: p, entries: entries, err: err,
			notice: fmt.Sprintf("已删除 %s", remote)}
	})
}

// ---- 按键 ----

func (m *sftpModel) handleKey(msg tea.KeyPressMsg) (*sftpModel, tea.Cmd) {
	k := msg.Key()

	// 确认删除（当前焦点栏）
	if (m.focus == paneLocal && m.localConfirmID >= 0) || (m.focus == paneRemote && m.confirmID >= 0) {
		switch {
		case k.Code == tea.KeyEnter:
			fallthrough
		case k.Text == "y" || k.Text == "Y":
			return m.doDelete()
		case k.Code == tea.KeyEsc || k.Text == "n" || k.Text == "N" || k.Text == "q":
			if m.focus == paneLocal {
				m.localConfirmID = -1
			} else {
				m.confirmID = -1
			}
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
	case tea.KeyTab:
		m.focus = 1 - m.focus
		return m, nil
	case tea.KeyUp:
		m.moveCursor(-1)
	case tea.KeyDown:
		m.moveCursor(1)
	case tea.KeyPgUp:
		m.moveCursor(-10)
	case tea.KeyPgDown:
		m.moveCursor(10)
	case tea.KeyHome:
		m.setCursor(0)
	case tea.KeyEnd:
		if m.focus == paneLocal {
			m.setCursor(len(m.localEntries) - 1)
		} else {
			m.setCursor(len(m.entries) - 1)
		}
	case tea.KeyEnter:
		return m.enterCurrent()
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
				if m.focus == paneRemote {
					if e := m.currentEntry(); e != nil {
						return m.downloadEntry(e)
					}
				}
			case "x":
				if m.busy {
					return m, nil
				}
				if m.focus == paneLocal {
					if len(m.localEntries) > 0 {
						m.localConfirmID = m.localCursor
					}
				} else if len(m.entries) > 0 {
					m.confirmID = m.cursor
				}
			case "r":
				if m.busy {
					return m, nil
				}
				m.busy = true
				if m.focus == paneLocal {
					return m, m.loadLocal()
				}
				return m, m.loadList()
			case "q":
				return m, tea.Cmd(func() tea.Msg { return backToListMsg{} })
			}
		}
	}
	return m, nil
}

func (m *sftpModel) moveCursor(delta int) {
	if m.focus == paneLocal {
		n := len(m.localEntries)
		if n == 0 {
			return
		}
		m.localCursor = m.localCursor + delta
		if m.localCursor < 0 {
			m.localCursor = 0
		}
		if m.localCursor >= n {
			m.localCursor = n - 1
		}
		m.ensureVisible(&m.localTop, m.localCursor)
		return
	}
	n := len(m.entries)
	if n == 0 {
		return
	}
	m.cursor = m.cursor + delta
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.cursor >= n {
		m.cursor = n - 1
	}
	m.ensureVisible(&m.remoteTop, m.cursor)
}

func (m *sftpModel) setCursor(idx int) {
	if m.focus == paneLocal {
		if idx < 0 || idx >= len(m.localEntries) {
			return
		}
		m.localCursor = idx
		m.ensureVisible(&m.localTop, m.localCursor)
		return
	}
	if idx < 0 || idx >= len(m.entries) {
		return
	}
	m.cursor = idx
	m.ensureVisible(&m.remoteTop, m.cursor)
}

// ---- 渲染 ----

func (m *sftpModel) View() tea.View {
	var b strings.Builder

	paneW := 40
	if m.width > 0 {
		paneW = (m.width - 3) / 2
		if paneW < 20 {
			paneW = 20
		}
	}

	// 标题行：焦点栏高亮
	localTitle := title("本地", m.localCwd, m.focus == paneLocal)
	remoteTitle := title("远程", m.cwd, m.focus == paneRemote)
	if m.width > 0 {
		localTitle = runewidth.Truncate(localTitle, paneW, "…")
		remoteTitle = runewidth.Truncate(remoteTitle, paneW, "…")
	}
	b.WriteString(lipgloss.JoinHorizontal(lipgloss.Top,
		localTitle, styleDim.Render(" │ "), remoteTitle) + "\n\n")

	// 两栏列表
	b.WriteString(m.paneView(paneLocal, paneW))
	b.WriteString("\n")
	b.WriteString(m.paneView(paneRemote, paneW))

	// 删除确认
	if m.focus == paneLocal && m.localConfirmID >= 0 && m.localConfirmID < len(m.localEntries) {
		b.WriteString("\n" + styleInfo.Render(fmt.Sprintf("确认删除 %q？ (y/N)", m.localEntries[m.localConfirmID].Name())) + "\n")
	} else if m.confirmID >= 0 && m.confirmID < len(m.entries) {
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

func title(label, p string, focused bool) string {
	s := styleDim.Render(label + ": " + p)
	if focused {
		s = styleCursor.Render("▸ " + label + ": " + p)
	}
	return s
}

// paneView 渲染单栏列表（滚动窗口 top 起 bodyHeight 行）
func (m *sftpModel) paneView(kind, width int) string {
	var entries []fs.FileInfo
	var cursor, top int
	if kind == paneLocal {
		entries, cursor, top = m.localEntries, m.localCursor, m.localTop
	} else {
		entries, cursor, top = m.entries, m.cursor, m.remoteTop
	}
	focused := m.focus == kind

	var b strings.Builder
	header := styleHeader.Render(padRight("名称", width-22)) +
		styleDim.Render(padRight("大小", 9) + padRight("时间", 12))
	row := header
	if focused {
		row = styleSelected.Render(padRight("名称", width-22)) + styleDim.Render(padRight("大小", 9)+padRight("时间", 12))
	}
	b.WriteString(row + "\n")
	b.WriteString(styleDim.Render(strings.Repeat("─", width)) + "\n")

	body := m.bodyHeight()
	start, end := 0, len(entries)
	if body > 0 && end > body {
		start, end = top, top+body
		if end > len(entries) {
			end = len(entries)
		}
	}
	for i := start; i < end; i++ {
		b.WriteString(m.renderEntry(entries[i], i, cursor, width) + "\n")
	}
	return lipgloss.NewStyle().Width(width).Render(b.String())
}

func (m *sftpModel) renderEntry(e fs.FileInfo, idx, cursor, width int) string {
	nameStr := e.Name()
	if e.IsDir() {
		nameStr += "/"
	}
	nameW := width - 22
	if nameW < 8 {
		nameW = 8
	}
	name := padRight(runewidth.Truncate(nameStr, nameW, "…"), nameW)
	size := padRight(sftpc.FormatSize(e.Size()), 9)
	if e.IsDir() {
		size = padRight("-", 9)
	}
	line := lipgloss.JoinHorizontal(lipgloss.Top,
		name,
		styleDim.Render(size),
		styleDim.Render(e.ModTime().Format("01-02 15:04")),
	)
	if idx == cursor {
		return styleCursor.Render("▸ ") + line
	}
	return "  " + line
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
	return "Tab 切换栏  Enter 进入/传输(本地↑/远程↓)  ↑/↓ 移动  Backspace 上级  u 路径上传  n 新建目录  d 下载  x 删除  r 刷新  q 返回"
}