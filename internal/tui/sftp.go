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
	"github.com/pkg/sftp"
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
	modeGoto
)

// 栏焦点
const (
	paneLocal  = iota // 本地栏
	paneRemote        // 远程栏
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

// sftpGotoCompleteMsg Tab 路径补全结果（input 为计算时的输入快照）
type sftpGotoCompleteMsg struct {
	input string
	cands []string
	err   error
}

// sftpGotoJumpMsg 远程路径跳转结果
type sftpGotoJumpMsg struct {
	path    string
	entries []fs.FileInfo
	err     error
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
	cwd       string
	entries   []fs.FileInfo
	cursor    int
	remoteTop int // 滚动窗口顶部
	confirmID int // 待删除条目下标，-1 表示无

	// 本地栏
	localCwd       string
	localEntries   []fs.FileInfo
	localCursor    int
	localTop       int
	localConfirmID int // 待删除条目下标，-1 表示无

	// 多选（按列表下标）
	selLocal  map[int]struct{}
	selRemote map[int]struct{}
	// confirmBatch 批量删除确认中（有选中项按 x 触发）
	confirmBatch bool

	focus int // 当前焦点栏（paneLocal / paneRemote）

	remoteCwd string // 会话内跟踪到的远程工作目录（热键唤起定位用，空串=默认）

	mode     sftpMode
	uploadIn *textinput.Model
	newDirIn *textinput.Model
	gotoIn   *textinput.Model

	// 路径跳转 Tab 补全状态
	gotoCandidates []string // 候选完整路径（当前输入快照下的匹配项）
	gotoLastSet    string   // 最近一次写入输入框的值（用于 Tab 循环切换判断）
	gotoSel        int

	fromSession bool // 会话中热键唤起：q 返回时请求重连会话

	transfer *sftpc.Transfer
	status   string
	err      string
	busy     bool

	width  int // 终端尺寸（WindowSizeMsg）
	height int
}

func newSFTPModel(s *store.Store, h *model.Host, remoteCwd string) *sftpModel {
	up := textInput("", "本地文件/目录路径，可直接拖拽文件到终端")
	nw := textInput("", "新目录名")
	gt := textInput("", "路径（Tab 补全）")
	m := &sftpModel{
		store: s, host: h,
		uploadIn: &up, newDirIn: &nw, gotoIn: &gt,
		confirmID: -1, localConfirmID: -1,
		selLocal:  map[int]struct{}{},
		selRemote: map[int]struct{}{},
		focus:     paneRemote,
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

	case sftpGotoCompleteMsg:
		return m.handleGotoComplete(msg)

	case sftpGotoJumpMsg:
		if msg.err != nil {
			m.err = msg.err.Error()
			m.busy = false
			m.mode = modeBrowse
			m.clearGotoCandidates()
			return m, nil
		}
		m.cwd = msg.path
		m.entries = msg.entries
		m.cursor = 0
		m.remoteTop = 0
		m.clearSel()
		m.busy = false
		m.mode = modeBrowse
		m.clearGotoCandidates()
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

// bodyHeight 列表可视行数（终端高度 - 标题/表头/分隔线/底部固定行）。
// 双栏左右并排后列表区只有一套表头与分隔线（原上下堆叠是两套）。
func (m *sftpModel) bodyHeight() int {
	if m.height <= 0 {
		return 0 // 未知尺寸：显示全部
	}
	h := m.height - 6 // 标题1 + 表头1 + 分隔线1 + 状态/底部3
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
			m.clearSel()
			m.busy = true
			return m, m.loadLocal()
		}
		m.cwd = path.Join(m.cwd, e.Name())
		m.cursor = 0
		m.entries = nil
		m.clearSel()
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
		m.clearSel()
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
	m.clearSel()
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

// downloadEntry 下载远程条目到本地栏当前目录（目录递归）
func (m *sftpModel) downloadEntry(e fs.FileInfo) (*sftpModel, tea.Cmd) {
	if m.busy {
		return m, nil
	}
	if err := os.MkdirAll(m.localCwd, 0o755); err != nil {
		m.err = err.Error()
		return m, nil
	}
	remote := path.Join(m.cwd, e.Name())
	local := filepath.Join(m.localCwd, e.Name())
	if e.IsDir() {
		return m, m.startPathTransfer(remote, local, false)
	}
	return m, m.startTransfer(remote, local, false)
}

func (m *sftpModel) startUpload(localPath string) tea.Cmd {
	remote := path.Join(m.cwd, filepath.Base(localPath))
	return m.startTransfer(localPath, remote, true)
}

// startUploadPath 上传本地文件或目录（目录递归）到远程栏当前目录
func (m *sftpModel) startUploadPath(localPath string) tea.Cmd {
	remote := path.Join(m.cwd, filepath.Base(localPath))
	return m.startPathTransfer(localPath, remote, true)
}

// startBatch 批量传输：有选中项时本地栏=上传、远程栏=下载（文件夹递归）
func (m *sftpModel) startBatch(up bool) (*sftpModel, tea.Cmd) {
	items := m.collectBatchItems(up)
	if len(items) == 0 {
		return m, nil
	}
	t := sftpc.NewTransfer(fmt.Sprintf("%d 项", len(items)), up)
	m.transfer = t
	m.busy = true
	m.status = ""
	m.confirmBatch = false
	m.clearSel()

	cl := m.conn.Client
	go sftpc.BatchTransfer(cl, t, up, items)
	return m, tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg {
		return sftpProgressMsg{}
	})
}

// collectBatchItems 收集焦点栏全部选中条目为批量传输项
func (m *sftpModel) collectBatchItems(up bool) []sftpc.BatchItem {
	sel := m.selectedMap()
	if len(sel) == 0 {
		return nil
	}
	entries := m.currentEntries()
	items := make([]sftpc.BatchItem, 0, len(sel))
	for i, e := range entries {
		if _, ok := sel[i]; !ok {
			continue
		}
		if up {
			items = append(items, sftpc.BatchItem{
				Src: filepath.Join(m.localCwd, e.Name()),
				Dst: path.Join(m.cwd, e.Name()),
			})
		} else {
			items = append(items, sftpc.BatchItem{
				Src: path.Join(m.cwd, e.Name()),
				Dst: filepath.Join(m.localCwd, e.Name()),
			})
		}
	}
	return items
}

// ---- 多选 ----

func (m *sftpModel) selectedMap() map[int]struct{} {
	if m.focus == paneLocal {
		return m.selLocal
	}
	return m.selRemote
}

func (m *sftpModel) currentEntries() []fs.FileInfo {
	if m.focus == paneLocal {
		return m.localEntries
	}
	return m.entries
}

func (m *sftpModel) currentCursor() int {
	if m.focus == paneLocal {
		return m.localCursor
	}
	return m.cursor
}

func (m *sftpModel) hasSel() bool {
	if m.focus == paneLocal {
		return len(m.selLocal) > 0
	}
	return len(m.selRemote) > 0
}

func (m *sftpModel) selCount() int {
	if m.focus == paneLocal {
		return len(m.selLocal)
	}
	return len(m.selRemote)
}

// clearSel 清空双栏选中
func (m *sftpModel) clearSel() {
	m.selLocal = map[int]struct{}{}
	m.selRemote = map[int]struct{}{}
	m.confirmBatch = false
}

func (m *sftpModel) toggleSel() {
	sel := m.selectedMap()
	idx := m.currentCursor()
	if _, ok := sel[idx]; ok {
		delete(sel, idx)
	} else {
		sel[idx] = struct{}{}
	}
}

// selectedNames 返回焦点栏选中条目的名字（有序，用于批量删除确认/渲染）
func (m *sftpModel) selectedEntries() []fs.FileInfo {
	sel := m.selectedMap()
	entries := m.currentEntries()
	var out []fs.FileInfo
	for i, e := range entries {
		if _, ok := sel[i]; ok {
			out = append(out, e)
		}
	}
	return out
}

// doBatchDelete 批量删除焦点栏全部选中条目（本地 RemoveAll / 远程递归）
func (m *sftpModel) doBatchDelete() (*sftpModel, tea.Cmd) {
	sel := m.selectedEntries()
	m.confirmBatch = false
	m.clearSel()
	if len(sel) == 0 {
		return m, nil
	}
	if m.focus == paneLocal {
		p := m.localCwd
		paths := make([]string, 0, len(sel))
		for _, e := range sel {
			paths = append(paths, filepath.Join(p, e.Name()))
		}
		return m, tea.Cmd(func() tea.Msg {
			for _, lp := range paths {
				if err := os.RemoveAll(lp); err != nil {
					return sftpMsgText{text: fmt.Sprintf("删除失败: %v", err)}
				}
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
				notice: fmt.Sprintf("已删除 %d 项", len(paths))}
		})
	}
	p := m.cwd
	cl := m.conn.Client
	type item struct {
		path  string
		isDir bool
	}
	items := make([]item, 0, len(sel))
	for _, e := range sel {
		items = append(items, item{path: path.Join(p, e.Name()), isDir: e.IsDir()})
	}
	return m, tea.Cmd(func() tea.Msg {
		for _, it := range items {
			if err := sftpc.Remove(cl, it.path, it.isDir); err != nil {
				return sftpMsgText{text: fmt.Sprintf("删除失败: %v", err)}
			}
		}
		entries, err := sftpc.List(cl, p)
		return sftpListMsg{kind: paneRemote, path: p, entries: entries, err: err,
			notice: fmt.Sprintf("已删除 %d 项", len(items))}
	})
}

// startPathTransfer 异步传输单个文件或目录（递归），进度走同一轮询
func (m *sftpModel) startPathTransfer(src, dst string, up bool) tea.Cmd {
	t := sftpc.NewTransfer(filepath.Base(src), up)
	m.transfer = t
	m.busy = true
	m.status = ""

	cl := m.conn.Client
	if up {
		go sftpc.UploadPath(cl, t, src, dst)
	} else {
		go sftpc.DownloadPath(cl, t, src, dst)
	}
	return tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg {
		return sftpProgressMsg{}
	})
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

	// 确认删除（当前焦点栏，单个或批量）
	singleConfirm := (m.focus == paneLocal && m.localConfirmID >= 0) ||
		(m.focus == paneRemote && m.confirmID >= 0)
	if singleConfirm || m.confirmBatch {
		switch {
		case k.Code == tea.KeyEnter:
			fallthrough
		case k.Text == "y" || k.Text == "Y":
			if m.confirmBatch {
				return m.doBatchDelete()
			}
			return m.doDelete()
		case k.Code == tea.KeyEsc || k.Text == "n" || k.Text == "N" || k.Text == "q":
			m.confirmBatch = false
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
			abs := sshc.ExpandPath(p)
			if st, err := os.Stat(abs); err != nil {
				m.err = "本地路径不存在"
				return m, nil
			} else if !st.IsDir() {
				m.mode = modeBrowse
				return m, m.startUpload(abs)
			} else {
				m.mode = modeBrowse
				return m, m.startUploadPath(abs)
			}
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

	// 路径跳转输入
	if m.mode == modeGoto {
		switch k.Code {
		case tea.KeyEsc:
			m.mode = modeBrowse
			m.clearGotoCandidates()
			return m, nil
		case tea.KeyEnter:
			return m.gotoJump()
		case tea.KeyTab:
			return m.gotoComplete()
		}
		in, cmd := m.gotoIn.Update(msg)
		if in.Value() != m.gotoLastSet {
			m.clearGotoCandidates()
		}
		*m.gotoIn = in
		return m, cmd
	}

	// 浏览模式
	switch k.Code {
	case tea.KeyTab:
		m.focus = 1 - m.focus
		m.confirmBatch = false
		return m, nil
	case tea.KeyEsc:
		if m.hasSel() {
			m.clearSel()
			return m, nil
		}
	case tea.KeySpace:
		m.toggleSel()
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
		if m.hasSel() {
			return m.startBatch(m.focus == paneLocal)
		}
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
			case "g":
				if m.busy {
					return m, nil
				}
				return m.openGoto()
			case "n":
				if m.busy {
					return m, nil
				}
				m.mode = modeNewDir
				m.newDirIn.Focus()
				return m, nil
			case "d":
				if m.focus == paneRemote && !m.hasSel() {
					if e := m.currentEntry(); e != nil {
						return m.downloadEntry(e)
					}
				}
			case "x":
				if m.busy {
					return m, nil
				}
				if m.hasSel() {
					m.confirmBatch = true
					m.confirmID = -1
					m.localConfirmID = -1
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

// ---- 路径跳转与 Tab 补全 ----

func (m *sftpModel) clearGotoCandidates() {
	m.gotoCandidates = nil
	m.gotoLastSet = ""
	m.gotoSel = 0
}

func (m *sftpModel) openGoto() (*sftpModel, tea.Cmd) {
	m.mode = modeGoto
	m.clearGotoCandidates()
	if m.focus == paneLocal {
		m.gotoIn.SetValue(m.localCwd)
	} else {
		m.gotoIn.SetValue(m.cwd)
	}
	m.gotoIn.Focus()
	return m, nil
}

// gotoSplit 拆分输入为「目录部分 + 基础名」；无分隔符时 dir 返回空串。
// 本地兼容 / 与 \ 分隔符，远程按 / 拆分。
func (m *sftpModel) gotoSplit(v string) (dir, base string) {
	if m.focus == paneLocal {
		if strings.HasSuffix(v, "/") || strings.HasSuffix(v, string(os.PathSeparator)) {
			return v, ""
		}
		i := strings.LastIndexAny(v, `/\`)
		if i < 0 {
			return "", v
		}
		return v[:i], v[i+1:]
	}
	if strings.HasSuffix(v, "/") {
		return v, ""
	}
	i := strings.LastIndex(v, "/")
	if i < 0 {
		return "", v
	}
	return v[:i], v[i+1:]
}

// gotoJump 校验并跳转到输入路径（本地同步校验，远程异步 List 校验）
func (m *sftpModel) gotoJump() (*sftpModel, tea.Cmd) {
	v := strings.TrimSpace(m.gotoIn.Value())
	m.clearGotoCandidates()
	if v == "" {
		m.mode = modeBrowse
		return m, nil
	}
	if m.focus == paneLocal {
		p := sshc.ExpandPath(v)
		st, err := os.Stat(p)
		if err != nil || !st.IsDir() {
			m.err = "本地路径不存在或不是目录"
			return m, nil
		}
		m.mode = modeBrowse
		m.localCwd = p
		m.localCursor = 0
		m.localEntries = nil
		m.localTop = 0
		m.clearSel()
		m.busy = true
		return m, m.loadLocal()
	}
	cl := m.conn.Client
	p := v
	m.busy = true
	return m, tea.Cmd(func() tea.Msg {
		entries, err := sftpc.List(cl, p)
		if err != nil {
			return sftpGotoJumpMsg{path: p, err: fmt.Errorf("目录不存在或不是目录: %w", err)}
		}
		return sftpGotoJumpMsg{path: p, entries: entries}
	})
}

// gotoComplete Tab 补全：有新鲜候选时循环切换，否则异步读取目录计算候选
func (m *sftpModel) gotoComplete() (*sftpModel, tea.Cmd) {
	v := m.gotoIn.Value()
	if len(m.gotoCandidates) > 0 && v == m.gotoLastSet {
		m.gotoSel = (m.gotoSel + 1) % len(m.gotoCandidates)
		next := m.gotoCandidates[m.gotoSel]
		m.gotoIn.SetValue(next)
		m.gotoLastSet = next
		return m, nil
	}
	m.clearGotoCandidates()
	dir, base := m.gotoSplit(v)
	if dir == "" {
		if m.focus == paneLocal {
			dir = m.localCwd
		} else {
			dir = m.cwd
		}
	} else if m.focus == paneLocal {
		dir = sshc.ExpandPath(dir)
	}
	kind := m.focus
	var cl *sftp.Client
	if kind == paneRemote && m.conn != nil {
		cl = m.conn.Client
	}
	return m, tea.Cmd(func() tea.Msg {
		var names []string
		if kind == paneLocal {
			entries, err := os.ReadDir(dir)
			if err != nil {
				return sftpGotoCompleteMsg{input: v, err: err}
			}
			for _, e := range entries {
				names = append(names, e.Name())
			}
		} else {
			entries, err := sftpc.List(cl, dir)
			if err != nil {
				return sftpGotoCompleteMsg{input: v, err: err}
			}
			for _, e := range entries {
				names = append(names, e.Name())
			}
		}
		var cands []string
		for _, n := range names {
			if n == "." || n == ".." {
				continue
			}
			if base == "" || strings.HasPrefix(strings.ToLower(n), strings.ToLower(base)) {
				cands = append(cands, joinGoto(dir, n, kind == paneLocal))
			}
		}
		return sftpGotoCompleteMsg{input: v, cands: cands}
	})
}

func (m *sftpModel) handleGotoComplete(msg sftpGotoCompleteMsg) (*sftpModel, tea.Cmd) {
	if msg.input != m.gotoIn.Value() {
		return m, nil // 输入已变化，丢弃陈旧结果
	}
	if msg.err != nil {
		m.err = fmt.Sprintf("补全失败: %v", msg.err)
		m.clearGotoCandidates()
		return m, nil
	}
	if len(msg.cands) == 0 {
		m.clearGotoCandidates()
		m.status = "无匹配项"
		return m, nil
	}
	m.gotoCandidates = msg.cands
	m.gotoSel = 0
	next := msg.cands[0]
	m.gotoIn.SetValue(next)
	m.gotoLastSet = next
	return m, nil
}

func joinGoto(dir, name string, local bool) string {
	if local {
		return filepath.Join(dir, name)
	}
	return path.Join(dir, name)
}

// ---- 渲染 ----

func (m *sftpModel) View() tea.View {
	var b strings.Builder

	// 栏宽：基于外层 Padding(1,2) 之外的实际内容宽（m.width-4）计算，扣除中间分隔线 " │ "（3 宽）
	paneW := 40
	if m.width > 0 {
		contentW := m.width - 4
		paneW = (contentW - 3) / 2
		if paneW < 8 {
			paneW = 8
		}
	}

	// 标题行：焦点栏高亮（对纯文本截断后再上色，避免 runewidth 破坏 ANSI 序列）
	b.WriteString(lipgloss.JoinHorizontal(lipgloss.Top,
		title("本地", m.localCwd, m.focus == paneLocal, paneW),
		styleDim.Render(" │ "),
		title("远程", m.cwd, m.focus == paneRemote, paneW),
	) + "\n")

	// 两栏列表：左右并排，各自滚动；行数补平保证右栏逐行对齐
	sep := styleDim.Render(" │ ")
	left := m.paneRows(paneLocal, paneW)
	right := m.paneRows(paneRemote, paneW)
	n := len(left)
	if len(right) > n {
		n = len(right)
	}
	for len(left) < n {
		left = append(left, strings.Repeat(" ", paneW))
	}
	for len(right) < n {
		right = append(right, strings.Repeat(" ", paneW))
	}
	for i := 0; i < n; i++ {
		b.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, left[i], sep, right[i]) + "\n")
	}

	// 删除确认
	if m.confirmBatch {
		b.WriteString("\n" + styleInfo.Render(fmt.Sprintf("确认删除 %d 项？ (y/N)", m.selCount())) + "\n")
	} else if m.focus == paneLocal && m.localConfirmID >= 0 && m.localConfirmID < len(m.localEntries) {
		b.WriteString("\n" + styleInfo.Render(fmt.Sprintf("确认删除 %q？ (y/N)", m.localEntries[m.localConfirmID].Name())) + "\n")
	} else if m.confirmID >= 0 && m.confirmID < len(m.entries) {
		b.WriteString("\n" + styleInfo.Render(fmt.Sprintf("确认删除 %q？ (y/N)", m.entries[m.confirmID].Name())) + "\n")
	}

	// 输入模式
	if m.mode == modeUpload {
		b.WriteString("\n" + styleCursor.Render("上传 ") + "本地路径(可拖拽文件/目录): " + m.uploadIn.View() + "\n")
	} else if m.mode == modeNewDir {
		b.WriteString("\n" + styleCursor.Render("新建目录: ") + m.newDirIn.View() + "\n")
	} else if m.mode == modeGoto {
		b.WriteString("\n" + styleCursor.Render("跳转路径: ") + m.gotoIn.View() + "\n")
		if len(m.gotoCandidates) > 0 {
			max := len(m.gotoCandidates)
			if max > 6 {
				max = 6
			}
			for i := 0; i < max; i++ {
				mark := "  "
				if i == m.gotoSel {
					mark = "▸ "
				}
				line := mark + m.gotoCandidates[i]
				if i == m.gotoSel {
					line = styleSelected.Render(line)
				}
				b.WriteString(line + "\n")
			}
		}
		b.WriteString("\n" + styleHint.Render("Tab 补全  Enter 跳转  Esc 取消") + "\n")
	}

	// 多选状态提示
	if m.hasSel() {
		b.WriteString("\n" + styleHint.Render(fmt.Sprintf("已选中 %d 项（Enter 批量传输 / x 删除 / Esc 取消）", m.selCount())) + "\n")
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

// title 渲染栏标题。path 为纯文本，先截断到可用宽度再上色，
// 避免对含 ANSI 的字符串做 runewidth 截断导致颜色序列被破坏/宽度错乱。
func title(label, p string, focused bool, width int) string {
	prefix := ""
	if focused {
		prefix = "▸ "
	}
	avail := width - runewidth.StringWidth(prefix+label+": ")
	if avail < 1 {
		avail = 1
	}
	text := prefix + label + ": " + runewidth.Truncate(p, avail, "…")
	if focused {
		return styleCursor.Render(text)
	}
	return styleDim.Render(text)
}

// pane 列布局：每行 = 前缀 2 + 名称 + 大小(9) + 时间(12)，总宽 = 栏宽。
// 窄屏时按序丢弃时间列、大小列，保证不折行。
const (
	panePrefixW = 2
	paneSizeW   = 9
	paneTimeW   = 12
)

type paneLayout struct {
	nameW, sizeW, timeW int
	showSize, showTime  bool
}

func computePaneLayout(width int) paneLayout {
	lay := paneLayout{sizeW: paneSizeW, timeW: paneTimeW, showSize: true, showTime: true}
	nameW := width - panePrefixW - paneSizeW - paneTimeW
	if nameW < 8 {
		lay.showTime = false
		nameW = width - panePrefixW - paneSizeW
		if nameW < 8 {
			lay.showSize = false
			nameW = width - panePrefixW
			if nameW < 8 {
				nameW = 8
			}
		}
	}
	lay.nameW = nameW
	return lay
}

// paneRows 渲染单栏各行（表头 + 分隔线 + 可见条目），不补行。
func (m *sftpModel) paneRows(kind, width int) []string {
	var entries []fs.FileInfo
	var cursor, top int
	if kind == paneLocal {
		entries, cursor, top = m.localEntries, m.localCursor, m.localTop
	} else {
		entries, cursor, top = m.entries, m.cursor, m.remoteTop
	}
	focused := m.focus == kind
	lay := computePaneLayout(width)

	var rows []string
	header := strings.Repeat(" ", panePrefixW)
	headerName := padRight("名称", lay.nameW)
	if focused {
		header += styleSelected.Render(headerName)
	} else {
		header += styleHeader.Render(headerName)
	}
	if lay.showSize {
		header += styleDim.Render(padRight("大小", lay.sizeW))
	}
	if lay.showTime {
		header += styleDim.Render(padRight("时间", lay.timeW))
	}
	rows = append(rows, header)
	rows = append(rows, styleDim.Render(strings.Repeat("─", width)))

	body := m.bodyHeight()
	start, end := 0, len(entries)
	if body > 0 && end > body {
		start, end = top, top+body
		if end > len(entries) {
			end = len(entries)
		}
	}
	for i := start; i < end; i++ {
		rows = append(rows, m.renderEntry(entries[i], i, cursor, lay, kind))
	}
	return rows
}

func (m *sftpModel) renderEntry(e fs.FileInfo, idx, cursor int, lay paneLayout, kind int) string {
	nameStr := e.Name()
	if e.IsDir() {
		nameStr += "/"
	}
	selMark := " "
	if kind == paneLocal {
		if _, ok := m.selLocal[idx]; ok {
			selMark = "●"
		}
	} else if _, ok := m.selRemote[idx]; ok {
		selMark = "●"
	}
	cursorMark := " "
	if idx == cursor {
		cursorMark = "▸"
	}
	prefix := cursorMark + selMark
	if idx == cursor {
		prefix = styleCursor.Render(prefix)
	}
	name := padRight(runewidth.Truncate(nameStr, lay.nameW, "…"), lay.nameW)
	var b strings.Builder
	b.WriteString(prefix)
	b.WriteString(name)
	if lay.showSize {
		size := sftpc.FormatSize(e.Size())
		if e.IsDir() {
			size = "-"
		}
		b.WriteString(styleDim.Render(padRight(size, lay.sizeW)))
	}
	if lay.showTime {
		b.WriteString(styleDim.Render(padRight(e.ModTime().Format("01-02 15:04"), lay.timeW)))
	}
	return b.String()
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
	return "Tab 切换栏  Enter 进入/传输(本地↑/远程↓)  Space 多选  g 跳转路径  ↑/↓ 移动  Backspace 上级  u 路径上传  n 新建目录  d 下载  x 删除  r 刷新  q 返回"
}
