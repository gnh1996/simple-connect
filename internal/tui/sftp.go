package tui

import (
	"context"
	"errors"
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
	// modePath：p 输入路径传输（方向按焦点栏）
	modePath
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

// sftpGotoCompleteMsg Tab 路径补全结果（input 为计算时的输入快照；
// target 区分 g 跳转与 p 路径传输两处补全，缺省 modeGoto 兼容旧调用）
type sftpGotoCompleteMsg struct {
	target sftpMode
	input  string
	cands  []string
	err    error
}

// sftpGotoJumpMsg 远程路径跳转结果
type sftpGotoJumpMsg struct {
	path    string
	entries []fs.FileInfo
	err     error
}

// sftpOverwriteCheckMsg 覆盖检测结果
type sftpOverwriteCheckMsg struct {
	conflicts []string
	total     int64 // 扫描得到的传输总字节数
	pending   *pendingTransfer
	err       error
}

// pendingTransfer 待执行的传输（覆盖确认后执行）
type pendingTransfer struct {
	up    bool
	src   string
	dst   string
	items []sftpc.BatchItem
	seq   int   // 发起时的检测代数号，收到结果时比对，不匹配则丢弃
	total int64 // 预扫描总量，执行时直接预设，避免再走一遍目录树
}

// sftpModel 双栏（本地 | 远程）文件浏览/传输模型。
// 远程栏：cwd/entries/cursor/confirmID（字段名向后兼容测试）；
// 本地栏：localCwd/localEntries/localCursor/localConfirmID。
type sftpModel struct {
	store *store.Store
	host  *model.Host
	conn  *sftpc.Conn

	// sshClient 会话复用连接（Ctrl+X f 挂起会话唤起场景，非 nil 时 SFTP 复用同一
	// SSH 连接，免重新认证；列表页进入为 nil，走独立 Dial）。
	sshClient *sshc.Client

	hostKeyCallback ssh.HostKeyCallback // 测试可注入

	// trustHostKey 指纹确认后信任 known_hosts（测试可注入，避免触碰真实 ~/.ssh）
	trustHostKey func(*sshc.UnknownHostKeyError) error

	// pendingKey 首次连接待确认的主机指纹（非 nil 时页面进入确认态：
	// 展示指纹，y 信任并重连，Esc/n/q 取消）
	pendingKey *sshc.UnknownHostKeyError

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
	pathIn   *textinput.Model
	newDirIn *textinput.Model
	gotoIn   *textinput.Model

	// 路径传输（p）Tab 补全状态
	pathCandidates []string // 候选完整路径（当前输入快照下的匹配项）
	pathLastSet    string   // 最近一次写入输入框的值（用于 Tab 循环切换判断）
	pathSel        int

	// 路径跳转（g）Tab 补全状态
	gotoCandidates []string // 候选完整路径（当前输入快照下的匹配项）
	gotoLastSet    string   // 最近一次写入输入框的值（用于 Tab 循环切换判断）
	gotoSel        int

	fromSession bool // 会话中热键唤起：q 返回时请求重连会话

	transfer *sftpc.Transfer
	status   string
	err      string
	busy     bool

	// 传输中退出确认（q / Ctrl+C）：confirmExit 显示确认提示；
	// exiting 表示已确认退出、正在等待取消完成（此后屏蔽输入，done 后回列表）。
	confirmExit bool
	exiting     bool
	exitTicks   int // exiting 期间轮询次数，超阈值强制离开（兜底阻塞的远端读取）

	// 覆盖确认
	confirmOverwrite   bool
	overwriteConflicts []string
	pendingOverwrite   *pendingTransfer
	checkingOverwrite  bool

	// overwriteSeq 覆盖检测代数号：取消或发起新检测时自增，
	// 迟到的旧检测结果（seq 不匹配）直接丢弃，防止「Esc 取消后仍触发传输」。
	overwriteSeq int
	// overwriteCancel 取消在途覆盖扫描：Esc 取消时立即中断远程/本地 Walk，
	// 避免大目录下取消后仍走完整棵树（结果到达后释放）。
	overwriteCancel context.CancelFunc

	width  int // 终端尺寸（WindowSizeMsg）
	height int
}

func newSFTPModel(s *store.Store, h *model.Host, remoteCwd string, sshCl *sshc.Client) *sftpModel {
	pt := textInput("", "路径（Tab 补全）")
	nw := textInput("", "新目录名")
	gt := textInput("", "路径（Tab 补全）")
	m := &sftpModel{
		store: s, host: h,
		sshClient: sshCl,
		pathIn:    &pt, newDirIn: &nw, gotoIn: &gt,
		confirmID: -1, localConfirmID: -1,
		selLocal:     map[int]struct{}{},
		selRemote:    map[int]struct{}{},
		focus:        paneRemote,
		remoteCwd:    remoteCwd,
		trustHostKey: sshc.TrustHostKey,
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
	// 拨号期间给出明确反馈并置 busy；失败与指纹确认分支负责复位，
	// 成功后由首条列表消息清除（Init 与 busy 复位必须同一处管理，见 docs/ux-perf-review.md 3.1）。
	m.busy = true
	if m.sshClient != nil {
		m.status = "正在建立 SFTP 通道…"
	} else {
		m.status = "正在连接…"
	}
	return tea.Cmd(func() tea.Msg {
		var conn *sftpc.Conn
		var err error
		if m.sshClient != nil {
			// 会话挂起唤起：复用同一 SSH 连接（免重新认证，SFTP 通道独立）
			conn, err = sftpc.NewConnFromSSH(m.sshClient)
		} else {
			pass, _ := m.store.Password(m.host)
			opts := []sshc.Option{}
			if m.hostKeyCallback != nil {
				opts = append(opts, sshc.WithHostKeyCallback(m.hostKeyCallback))
			}
			conn, err = sftpc.Dial(m.host, pass, opts...)
		}
		if err != nil {
			return sftpConnMsg{err: err}
		}
		return sftpConnMsg{conn: conn}
	})
}

// redial 指纹确认信任后重新建立连接（列表页独立连接场景）
func (m *sftpModel) redial() tea.Cmd {
	cmd := m.Init()
	m.status = "已信任主机指纹，正在重连…" // Init 会先置"正在连接…"，此处覆盖为更具体的文案
	return cmd
}

func (m *sftpModel) Update(msg tea.Msg) (*sftpModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case sftpConnMsg:
		if msg.err != nil {
			m.busy = false // 拨号失败：复位 busy，否则 t/p/x/r 全被挡住
			var uk *sshc.UnknownHostKeyError
			if errors.As(msg.err, &uk) {
				// 首次连接：不静默信任，页面进入确认态展示指纹（对齐 OpenSSH ask）
				m.pendingKey = uk
				m.status = fmt.Sprintf("首次连接 %s，指纹 %s 未确认", uk.Hostname, uk.Fingerprint)
				return m, nil
			}
			if m.sshClient == nil {
				// 列表页 f 进入走 ConnectRaw：不合并 ~/.ssh/config（有意为之），给跳板用户可操作的提示
				m.err = fmt.Sprintf("连接失败: %v（提示：列表 SFTP 不合并 ~/.ssh/config，跳板/别名主机请先 Enter 连接，再按 Ctrl+X f）", msg.err)
				return m, nil
			}
			m.err = fmt.Sprintf("连接失败: %v", msg.err)
			return m, nil
		}
		m.conn = msg.conn
		m.status = "" // 清除 Init 的"正在连接…"（下方会话目录提示会按需覆盖）
		if cwd, err := m.conn.Client.Getwd(); err == nil {
			m.cwd = cwd
		} else {
			m.cwd = "/"
		}
		// 会话热键唤起：优先定位到会话内跟踪到的目录
		if m.remoteCwd != "" {
			m.cwd = m.remoteCwd
		} else if m.fromSession {
			// 未能从会话跟踪到目录（PROMPT_COMMAND 注入未生效，如非 bash/zsh 或
			// 服务器环境特殊），落到默认路径并提示，便于定位。
			m.status = "未获取会话目录（shell 钩子未生效？目录可能不准确，可用 g 跳转）"
		}
		m.busy = true
		return m, tea.Batch(m.loadList(), m.loadLocal())

	case sftpListMsg:
		m.busy = false
		if msg.err != nil {
			m.err = fmt.Sprintf("读取目录失败: %v", msg.err)
			return m, nil
		}
		// 成功刷新后清除旧错误提示（避免常驻遮挡），状态提示保留
		m.err = ""
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
			m.err = ""
		} else {
			m.err = msg.text
		}
		return m, nil

	case sftpGotoCompleteMsg:
		return m.handleComplete(msg)

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

	case sftpOverwriteCheckMsg:
		if msg.pending == nil || msg.pending.seq != m.overwriteSeq {
			// 过期结果（用户已取消或发起了新的检测）：丢弃，不执行任何传输，
			// 也不得触碰当前状态（可能有新一轮检测仍在途，误清 busy/checkingOverwrite
			// 会解除输入屏蔽并丢失"正在检测"提示）
			return m, nil
		}
		m.releaseOverwriteCancel()
		m.checkingOverwrite = false
		m.busy = false
		m.status = ""
		if msg.err != nil {
			m.err = fmt.Sprintf("检测覆盖失败: %v", msg.err)
			return m, nil
		}
		msg.pending.total = msg.total // 预扫描总量随结果带回，执行时免重复统计
		if len(msg.conflicts) == 0 {
			// 无冲突，直接执行
			return m, m.executePendingTransfer(msg.pending)
		}
		// 有冲突，进入确认态
		m.confirmOverwrite = true
		m.overwriteConflicts = msg.conflicts
		m.pendingOverwrite = msg.pending
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

// readLocalInfos 读取本地目录并转为已排序的 FileInfo 列表。
// Info() 失败（如断链符号链接）时回退 Lstat，避免条目从列表中静默消失。
func readLocalInfos(dir string) ([]fs.FileInfo, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	infos := make([]fs.FileInfo, 0, len(entries))
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			// DirEntry.Info 对符号链接取目标信息，断链时失败；退回 Lstat 展示链接本身
			if li, lerr := os.Lstat(filepath.Join(dir, e.Name())); lerr == nil {
				info = li
			} else {
				continue
			}
		}
		infos = append(infos, info)
	}
	sftpc.SortEntries(infos)
	return infos, nil
}

// loadLocal 刷新本地栏
func (m *sftpModel) loadLocal() tea.Cmd {
	p := m.localCwd
	return func() tea.Msg {
		infos, err := readLocalInfos(p)
		return sftpListMsg{kind: paneLocal, path: p, entries: infos, err: err}
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

// bodyHeight 列表可视条目行数。总行数 = 标题1 + 表头1 + 分隔线1 + body + 动态行
// + footer1 + 上下 padding2，必须恰为终端高度，footer 才能固定在最后一行。
// 动态行（确认/输入/状态等）存在时列表区相应压缩。
func (m *sftpModel) bodyHeight() int {
	return m.bodyHeightWith(len(m.dynamicLines()))
}

func (m *sftpModel) bodyHeightWith(dynLen int) int {
	if m.height <= 0 {
		return 0 // 未知尺寸：显示全部
	}
	body := m.height - 6 - dynLen
	if body < 1 {
		body = 1
	}
	return body
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
			m.localCwd = filepath.Join(m.localCwd, e.Name()) // 本地路径用 filepath（Windows 盘符/反斜杠）
			m.localCursor = 0
			m.localEntries = nil
			m.clearSel()
			m.busy = true
			return m, m.loadLocal()
		}
		m.cwd = path.Join(m.cwd, e.Name()) // 远程路径保持 POSIX
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

// localParent 本地栏上级目录：filepath.Dir（Windows 盘符/反斜杠语义），
// 与远程栏的 path.Dir（POSIX）区分。
func localParent(p string) string { return filepath.Dir(p) }

func (m *sftpModel) goUp() (*sftpModel, tea.Cmd) {
	if m.focus == paneLocal {
		parent := localParent(m.localCwd)
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
		dir := filepath.Join(m.localCwd, name) // 本地路径用 filepath（见 enterCurrent 注释）
		p := m.localCwd
		return func() tea.Msg {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return sftpMsgText{text: fmt.Sprintf("创建目录失败: %v", err)}
			}
			infos, err := readLocalInfos(p)
			if err != nil {
				return sftpMsgText{text: fmt.Sprintf("读取目录失败: %v", err)}
			}
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

// uploadEntry 上传本地文件到远程栏当前目录（带覆盖检测）
func (m *sftpModel) uploadEntry(localPath string) tea.Cmd {
	remote := path.Join(m.cwd, filepath.Base(localPath))
	return m.requestTransfer(localPath, remote, true)
}

// downloadEntry 下载远程条目到本地栏当前目录（目录递归，带覆盖检测）
func (m *sftpModel) downloadEntry(e fs.FileInfo) (*sftpModel, tea.Cmd) {
	if m.busy || m.checkingOverwrite {
		return m, nil
	}
	if err := os.MkdirAll(m.localCwd, 0o755); err != nil {
		m.err = err.Error()
		return m, nil
	}
	remote := path.Join(m.cwd, e.Name())
	local := filepath.Join(m.localCwd, e.Name())
	return m, m.requestTransfer(remote, local, false)
}

// startUpload / startUploadPath / startBatch 已移除：
// 上传/下载统一经 requestTransfer/requestBatch 做覆盖检测后由 executePendingTransfer 执行。

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
			infos, err := readLocalInfos(p)
			if err != nil {
				return sftpMsgText{text: fmt.Sprintf("读取目录失败: %v", err)}
			}
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

// startPathTransfer / startTransfer 已移除：传输统一走 requestTransfer（覆盖检测）
// → executePendingTransfer 路径，避免绕过检测的旧入口。

func (m *sftpModel) handleProgress() (*sftpModel, tea.Cmd) {
	if m.transfer == nil {
		return m, nil
	}
	_, _, finished, err := m.transfer.Snapshot()
	if finished {
		up := m.transfer.Up
		done, _, _, _ := m.transfer.Snapshot()
		canceled := m.transfer.Canceled()
		m.transfer = nil
		m.busy = false
		m.confirmExit = false // 传输已结束：撤销可能仍显示的退出确认
		// 已确认退出：取消完成（临时文件已清理）后离开页面，不再提示完成/错误
		if m.exiting {
			m.exiting = false
			return m, tea.Cmd(func() tea.Msg { return backToListMsg{} })
		}
		if canceled {
			m.status = transferName(up) + "已取消"
			return m, tea.Batch(m.loadList(), m.loadLocal())
		}
		if err != nil {
			m.err = fmt.Sprintf("%s失败: %v", transferName(up), err)
			return m, nil
		}
		m.status = fmt.Sprintf("%s完成 %s", transferName(up), sftpc.FormatSize(done))
		// 保留光标位置（不跳回顶部），刷新的列表由 clampCursor 兜底；两侧列表均刷新
		return m, tea.Batch(m.loadList(), m.loadLocal())
	}
	// 已确认退出但传输迟迟未结束（如远端读取阻塞）：等待约 5 秒后强制离开。
	// 此时由上层关闭连接中断底层 I/O，可能残留 .part 临时文件，属兜底路径。
	if m.exiting {
		m.exitTicks++
		if m.exitTicks >= 50 {
			m.exiting = false
			return m, tea.Cmd(func() tea.Msg { return backToListMsg{} })
		}
	}
	// 继续轮询
	return m, tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg {
		return sftpProgressMsg{}
	})
}

// requestExit 处理离开页面请求（q / Ctrl+C）：
// 传输中先进入确认态，避免用户无提示地中止传输；否则直接返回列表。
func (m *sftpModel) requestExit() (*sftpModel, tea.Cmd) {
	if m.transfer != nil {
		m.confirmExit = true
		return m, nil
	}
	return m, tea.Cmd(func() tea.Msg { return backToListMsg{} })
}

// confirmExitNow 用户确认退出：请求取消传输并继续轮询，待取消完成（临时文件已清理）
// 后再返回列表，避免在传输仍持有连接时关闭连接导致竞态与残留文件。
func (m *sftpModel) confirmExitNow() (*sftpModel, tea.Cmd) {
	m.confirmExit = false
	if m.transfer == nil {
		return m, tea.Cmd(func() tea.Msg { return backToListMsg{} })
	}
	m.exiting = true
	m.exitTicks = 0
	m.err = ""
	m.status = "正在取消传输…"
	m.transfer.Cancel()
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

// ---- 覆盖检测与确认 ----

func (m *sftpModel) checkOverwriteCmd(ctx context.Context, pending *pendingTransfer) tea.Cmd {
	cl := m.conn.Client
	// 无连接时直接报错（理论上不会触发）
	if cl == nil {
		return func() tea.Msg {
			return sftpOverwriteCheckMsg{pending: pending, err: fmt.Errorf("未连接")}
		}
	}
	return func() tea.Msg {
		var res sftpc.ScanResult
		var err error
		if len(pending.items) > 0 {
			res, err = sftpc.BatchScan(ctx, cl, pending.up, pending.items)
		} else if pending.up {
			res, err = sftpc.ScanUpload(ctx, cl, pending.src, pending.dst)
		} else {
			res, err = sftpc.ScanDownload(ctx, cl, pending.src, pending.dst)
		}
		return sftpOverwriteCheckMsg{conflicts: res.Conflicts, total: res.Total, pending: pending, err: err}
	}
}

// startOverwriteCheck 发起新一轮覆盖扫描：自增代数号、创建可取消上下文，
// 并返回扫描命令。Esc 取消时经 releaseOverwriteCancel 中断在途 Walk。
func (m *sftpModel) startOverwriteCheck(pending *pendingTransfer) tea.Cmd {
	if m.overwriteCancel != nil {
		m.overwriteCancel() // 防御：不应有在途扫描，取消是幂等的
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.overwriteCancel = cancel
	m.overwriteSeq++
	pending.seq = m.overwriteSeq
	return m.checkOverwriteCmd(ctx, pending)
}

// releaseOverwriteCancel 释放覆盖扫描的取消函数（结果到达或取消后调用，幂等）。
func (m *sftpModel) releaseOverwriteCancel() {
	if m.overwriteCancel != nil {
		m.overwriteCancel()
		m.overwriteCancel = nil
	}
}

func (m *sftpModel) executePendingTransfer(pending *pendingTransfer) tea.Cmd {
	if pending == nil {
		return nil
	}
	if len(pending.items) > 0 {
		t := sftpc.NewTransfer(fmt.Sprintf("%d 项", len(pending.items)), pending.up)
		t.SetTotal(pending.total) // 预扫描总量：批内不再逐项统计
		m.transfer = t
		m.busy = true
		m.status = ""
		m.confirmBatch = false
		m.clearSel()
		cl := m.conn.Client
		go sftpc.BatchTransfer(cl, t, pending.up, pending.items)
		return tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg {
			return sftpProgressMsg{}
		})
	}
	// 单个路径：统一走 Path 变体（兼容文件与目录，自动判断 IsDir）
	t := sftpc.NewTransfer(filepath.Base(pending.src), pending.up)
	t.SetTotal(pending.total)
	m.transfer = t
	m.busy = true
	m.status = ""
	cl := m.conn.Client
	if pending.up {
		go sftpc.UploadPath(cl, t, pending.src, pending.dst)
	} else {
		go sftpc.DownloadPath(cl, t, pending.src, pending.dst)
	}
	return tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg {
		return sftpProgressMsg{}
	})
}

func (m *sftpModel) requestTransfer(src, dst string, up bool) tea.Cmd {
	if m.busy || m.checkingOverwrite {
		return nil
	}
	m.checkingOverwrite = true
	m.busy = true
	m.status = ""
	m.err = ""
	pending := &pendingTransfer{up: up, src: src, dst: dst}
	return m.startOverwriteCheck(pending)
}

func (m *sftpModel) requestBatch(up bool) (*sftpModel, tea.Cmd) {
	if m.busy || m.checkingOverwrite {
		return m, nil
	}
	items := m.collectBatchItems(up)
	if len(items) == 0 {
		return m, nil
	}
	m.checkingOverwrite = true
	m.busy = true
	m.status = ""
	m.err = ""
	pending := &pendingTransfer{up: up, items: items}
	return m, m.startOverwriteCheck(pending)
}

func (m *sftpModel) overwritePrompt() string {
	n := len(m.overwriteConflicts)
	if n == 0 {
		return ""
	}
	// 展示前 3 个冲突路径，其余用"等"概括
	show := n
	if show > 3 {
		show = 3
	}
	names := make([]string, 0, show)
	for i := 0; i < show; i++ {
		names = append(names, conflictBase(m.overwriteConflicts[i], m.pendingOverwrite != nil && m.pendingOverwrite.up))
	}
	list := strings.Join(names, "、")
	if n > 3 {
		list += fmt.Sprintf(" 等 %d 项", n)
	}
	action := "下载"
	if m.pendingOverwrite != nil && m.pendingOverwrite.up {
		action = "上传"
	}
	return fmt.Sprintf("目标已存在 %s，%s将覆盖 %s，是否继续？ (y/N)", list, action, list)
}

// conflictBase 按传输方向取冲突路径的文件名：
// 上传的冲突是远程路径（/ 分隔用 path.Base），下载的冲突是本地路径（平台分隔符用 filepath.Base）。
func conflictBase(p string, up bool) string {
	if up {
		return path.Base(p)
	}
	return filepath.Base(p)
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
			infos, err := readLocalInfos(p)
			if err != nil {
				return sftpMsgText{text: fmt.Sprintf("读取目录失败: %v", err)}
			}
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

	// 传输中退出确认态：仅响应确认/取消，其余按键忽略，防止误触
	if m.confirmExit {
		switch {
		case k.Code == tea.KeyEnter:
			fallthrough
		case k.Text == "y" || k.Text == "Y":
			return m.confirmExitNow()
		case k.Code == tea.KeyEsc || k.Text == "n" || k.Text == "N" || k.Text == "q",
			k.Mod.Contains(tea.ModCtrl) && k.Code == 'c':
			m.confirmExit = false
			return m, nil
		}
		return m, nil
	}

	// 已确认退出：等待取消完成期间屏蔽全部输入（避免再次触发传输/删除等）
	if m.exiting {
		return m, nil
	}

	// Ctrl+C 与 q 同义：传输中进入退出确认，否则离开页面
	if k.Mod.Contains(tea.ModCtrl) && k.Code == 'c' {
		return m.requestExit()
	}

	// 首次连接指纹确认态：y/Enter 信任并重连，Esc/n/q 取消
	if m.pendingKey != nil {
		switch {
		case k.Code == tea.KeyEnter:
			fallthrough
		case k.Text == "y" || k.Text == "Y":
			uk := m.pendingKey
			m.pendingKey = nil
			if terr := m.trustHostKey(uk); terr != nil {
				m.err = fmt.Sprintf("写入 known_hosts 失败: %v", terr)
				return m, nil
			}
			m.status = "已信任主机指纹，正在重连…"
			return m, m.redial()
		case k.Code == tea.KeyEsc || k.Text == "n" || k.Text == "N" || k.Text == "q":
			m.pendingKey = nil
			m.err = "已取消连接（未信任主机指纹）"
			return m, nil
		}
		return m, nil
	}

	// 覆盖确认（上传/下载目标已存在）
	if m.confirmOverwrite {
		switch {
		case k.Code == tea.KeyEnter:
			fallthrough
		case k.Text == "y" || k.Text == "Y":
			pending := m.pendingOverwrite
			m.confirmOverwrite = false
			m.overwriteConflicts = nil
			m.pendingOverwrite = nil
			m.busy = false
			m.checkingOverwrite = false
			return m, m.executePendingTransfer(pending)
		case k.Code == tea.KeyEsc || k.Text == "n" || k.Text == "N" || k.Text == "q":
			m.confirmOverwrite = false
			m.overwriteConflicts = nil
			m.pendingOverwrite = nil
			m.busy = false
			m.checkingOverwrite = false
			m.status = "已取消"
			return m, nil
		}
		return m, nil
	}
	if m.checkingOverwrite {
		// 检测期间忽略除 Esc 取消外的输入
		if k.Code == tea.KeyEsc {
			m.overwriteSeq++           // 使在途检测结果作废（迟到消息将被丢弃）
			m.releaseOverwriteCancel() // 中断扫描：不等待整棵目录树走完
			m.checkingOverwrite = false
			m.busy = false
			m.status = "已取消"
			return m, nil
		}
		return m, nil
	}

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

	// 路径传输输入（p）：方向按焦点栏——本地=上传、远程=下载
	if m.mode == modePath {
		switch k.Code {
		case tea.KeyEsc:
			m.mode = modeBrowse
			m.clearPathCandidates()
			return m, nil
		case tea.KeyEnter:
			return m.pathJump()
		case tea.KeyTab:
			return m.pathComplete()
		}
		in, cmd := m.pathIn.Update(msg)
		if in.Value() != m.pathLastSet {
			m.clearPathCandidates()
		}
		*m.pathIn = in
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
			return m.requestBatch(m.focus == paneLocal)
		}
		return m.enterCurrent()
	case tea.KeyBackspace:
		return m.goUp()
	default:
		if k.Text != "" {
			switch k.Text {
			case "t":
				if m.busy {
					return m, nil
				}
				if m.hasSel() {
					return m.requestBatch(m.focus == paneLocal)
				}
				e := m.currentEntry()
				if e == nil {
					return m, nil
				}
				if m.focus == paneLocal {
					return m, m.uploadEntry(filepath.Join(m.localCwd, e.Name()))
				}
				return m.downloadEntry(e)
			case "p":
				if m.busy {
					return m, nil
				}
				return m.openPath()
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
				return m.requestExit()
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
	clearComplete(&m.gotoCandidates, &m.gotoLastSet, &m.gotoSel)
}

func (m *sftpModel) clearPathCandidates() {
	clearComplete(&m.pathCandidates, &m.pathLastSet, &m.pathSel)
}

// clearComplete 重置一组补全状态（候选 / 最近写入值 / 当前下标）
func clearComplete(cands *[]string, lastSet *string, sel *int) {
	*cands = nil
	*lastSet = ""
	*sel = 0
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
	m.gotoIn.CursorEnd() // SetValue 复用时不会重置光标，显式落到末尾便于追加/修改
	return m, nil
}

// openPath 打开路径传输输入（p）。方向按焦点栏：本地栏=上传，远程栏=下载。
// 不预填路径，避免直接 Enter 误传整个当前目录；Tab 补全基准取焦点栏 cwd。
func (m *sftpModel) openPath() (*sftpModel, tea.Cmd) {
	m.mode = modePath
	m.clearPathCandidates()
	m.pathIn.SetValue("")
	if m.focus == paneLocal {
		m.pathIn.Placeholder = "本地路径，Enter 上传"
	} else {
		m.pathIn.Placeholder = "远程路径，Enter 下载"
	}
	m.pathIn.Focus()
	return m, nil
}

// splitPathInput 按本地/远程规则拆分输入。本地兼容 / 与 \ 分隔符，远程按 / 拆分；
// 无分隔符时 dir 返回空串（补全时回落到当前栏 cwd）。
func splitPathInput(v string, local bool) (dir, base string) {
	if local {
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

// pathJump 校验并执行路径传输（p）：本地栏上传本地路径，远程栏下载远程路径。
// 远程路径的存在性由覆盖扫描的 ScanDownload 异步校验。
func (m *sftpModel) pathJump() (*sftpModel, tea.Cmd) {
	v := strings.TrimSpace(m.pathIn.Value())
	m.clearPathCandidates()
	if v == "" {
		m.mode = modeBrowse
		return m, nil
	}
	if m.focus == paneLocal {
		p := sshc.ExpandPath(v)
		if _, err := os.Stat(p); err != nil {
			m.err = "本地路径不存在"
			return m, nil // 保留输入态便于修正
		}
		m.mode = modeBrowse
		remote := path.Join(m.cwd, filepath.Base(p))
		return m, m.requestTransfer(p, remote, true)
	}
	if m.conn == nil {
		m.err = "未连接"
		return m, nil
	}
	if err := os.MkdirAll(m.localCwd, 0o755); err != nil {
		m.err = err.Error()
		return m, nil
	}
	m.mode = modeBrowse
	local := filepath.Join(m.localCwd, path.Base(v))
	return m, m.requestTransfer(v, local, false)
}

// gotoComplete / pathComplete Tab 补全入口，共用 completeCycle。
func (m *sftpModel) gotoComplete() (*sftpModel, tea.Cmd) {
	return m.completeCycle(m.gotoIn, &m.gotoCandidates, &m.gotoLastSet, &m.gotoSel, modeGoto)
}

func (m *sftpModel) pathComplete() (*sftpModel, tea.Cmd) {
	return m.completeCycle(m.pathIn, &m.pathCandidates, &m.pathLastSet, &m.pathSel, modePath)
}

// completeCycle Tab 补全通用处理：有新鲜候选时循环切换，否则异步读取目录计算候选。
func (m *sftpModel) completeCycle(in *textinput.Model, cands *[]string, lastSet *string, sel *int, target sftpMode) (*sftpModel, tea.Cmd) {
	v := in.Value()
	if len(*cands) > 0 && v == *lastSet {
		*sel = (*sel + 1) % len(*cands)
		next := (*cands)[*sel]
		in.SetValue(next)
		in.CursorEnd() // 循环切换后光标落到末尾便于继续输入
		*lastSet = next
		return m, nil
	}
	clearComplete(cands, lastSet, sel)
	return m, m.completeCandidates(v, target)
}

// completeCandidates 异步计算补全候选：按焦点栏决定本地/远程，空基准取当前栏 cwd。
func (m *sftpModel) completeCandidates(v string, target sftpMode) tea.Cmd {
	local := m.focus == paneLocal
	dir, base := splitPathInput(v, local)
	if dir == "" {
		if local {
			dir = m.localCwd
		} else {
			dir = m.cwd
		}
	} else if local {
		dir = sshc.ExpandPath(dir)
	}
	var cl *sftp.Client
	if !local && m.conn != nil {
		cl = m.conn.Client
	}
	return tea.Cmd(func() tea.Msg {
		var names []string
		if local {
			entries, err := os.ReadDir(dir)
			if err != nil {
				return sftpGotoCompleteMsg{target: target, input: v, err: err}
			}
			for _, e := range entries {
				names = append(names, e.Name())
			}
		} else {
			entries, err := sftpc.List(cl, dir)
			if err != nil {
				return sftpGotoCompleteMsg{target: target, input: v, err: err}
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
				cands = append(cands, joinGoto(dir, n, local))
			}
		}
		return sftpGotoCompleteMsg{target: target, input: v, cands: cands}
	})
}

// handleComplete 应用补全结果：按 target 写入 g/p 对应的补全状态。
func (m *sftpModel) handleComplete(msg sftpGotoCompleteMsg) (*sftpModel, tea.Cmd) {
	in, cands, lastSet, sel := m.completeState(msg.target)
	if in == nil || msg.input != in.Value() {
		return m, nil // 输入已变化，丢弃陈旧结果
	}
	if msg.err != nil {
		m.err = fmt.Sprintf("补全失败: %v", msg.err)
		clearComplete(cands, lastSet, sel)
		return m, nil
	}
	if len(msg.cands) == 0 {
		clearComplete(cands, lastSet, sel)
		m.status = "无匹配项"
		return m, nil
	}
	*cands = msg.cands
	*sel = 0
	next := msg.cands[0]
	in.SetValue(next)
	in.CursorEnd() // 首次补全后光标落到末尾便于继续输入
	*lastSet = next
	return m, nil
}

// completeState 返回指定来源（g/p）的输入框与补全状态指针。
func (m *sftpModel) completeState(target sftpMode) (*textinput.Model, *[]string, *string, *int) {
	if target == modePath {
		return m.pathIn, &m.pathCandidates, &m.pathLastSet, &m.pathSel
	}
	return m.gotoIn, &m.gotoCandidates, &m.gotoLastSet, &m.gotoSel
}

func joinGoto(dir, name string, local bool) string {
	if local {
		return filepath.Join(dir, name)
	}
	return path.Join(dir, name)
}

// ---- 渲染 ----

func (m *sftpModel) View() tea.View {
	// 栏宽：基于外层 Padding(1,2) 之外的实际内容宽（m.width-4）计算。双栏并排时
	// 中间分隔线 " │ " 占 3 宽，每栏右侧各预留 1 宽滚动箭头位 → 共 5 宽。
	paneW := 40
	if m.width > 0 {
		contentW := m.width - 4
		paneW = (contentW - 5) / 2
		if paneW < 8 {
			paneW = 8
		}
	}

	dyn := m.dynamicLines()
	body := m.bodyHeightWith(len(dyn))

	// 标题行：焦点栏高亮（对纯文本截断后再上色，避免 runewidth 破坏 ANSI 序列）。
	// 宽度用 paneW+1 与列表行（含右侧滚动箭头位）对齐。
	titleLine := lipgloss.JoinHorizontal(lipgloss.Top,
		title("本地", m.localCwd, m.focus == paneLocal, paneW+1),
		styleDim.Render(" │ "),
		title("远程", m.cwd, m.focus == paneRemote, paneW+1))

	// 两栏列表：左右并排，各自滚动；行数补平（含箭头位 paneW+1）保证左右逐行对齐
	sep := styleDim.Render(" │ ")
	left := m.paneRowsWithBody(paneLocal, paneW, body)
	right := m.paneRowsWithBody(paneRemote, paneW, body)
	n := 2 + body
	if len(left) > n {
		n = len(left)
	}
	if len(right) > n {
		n = len(right)
	}
	for len(left) < n {
		left = append(left, strings.Repeat(" ", paneW+1))
	}
	for len(right) < n {
		right = append(right, strings.Repeat(" ", paneW+1))
	}

	var b strings.Builder
	b.WriteString(titleLine + "\n")
	for i := 0; i < n; i++ {
		b.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, left[i], sep, right[i]) + "\n")
	}
	for _, l := range dyn {
		b.WriteString(l + "\n")
	}
	// 单行 footer（不用带边框的 styleFooter：边框渲染 3 行会打乱行数计算）
	b.WriteString(styleDim.Render(renderSFTPFooter()))
	v := tea.NewView(lipgloss.NewStyle().Padding(1, 2).Render(b.String()))
	// View 不再直接 mutate 模型：错误行通过 dynamicLines 展示一次后，
	// 由调用方在下一轮 Update 中通过 sftpMsgText/status 覆盖或显式清除。
	// 保留 m.err 供下一次 View 仍能与 bodyHeight 计算一致，避免高度抖动。
	return v
}

// dynamicLines 生成列表区与 footer 之间的动态区块行（删除确认/输入模式/多选/状态/错误等），
// 渲染与高度计算共用，保证 footer 固定在终端最后一行。
func (m *sftpModel) dynamicLines() []string {
	var lines []string

	// 首次连接指纹确认
	if m.pendingKey != nil {
		lines = append(lines, styleInfo.Render(fmt.Sprintf(
			"首次连接 %s，指纹 %s 未确认，信任并继续？ (y/N)",
			m.pendingKey.Hostname, m.pendingKey.Fingerprint)))
	}

	// 传输中退出确认
	if m.confirmExit {
		lines = append(lines, styleError.Render(
			"传输进行中，退出将中止传输并删除未完成的临时文件，确定退出？ (y/N)"))
	} else if m.exiting {
		lines = append(lines, styleInfo.Render("正在取消传输…"))
	}

	// 覆盖确认（上传/下载目标已存在）
	if m.confirmOverwrite {
		lines = append(lines, styleInfo.Render(m.overwritePrompt()))
	} else if m.checkingOverwrite {
		lines = append(lines, styleInfo.Render("正在检测是否覆盖..."))
	}

	// 删除确认（单个或批量）
	if m.confirmBatch {
		lines = append(lines, styleInfo.Render(fmt.Sprintf("确认删除 %d 项？ (y/N)", m.selCount())))
	} else if m.focus == paneLocal && m.localConfirmID >= 0 && m.localConfirmID < len(m.localEntries) {
		lines = append(lines, styleInfo.Render(fmt.Sprintf("确认删除 %q？ (y/N)", m.localEntries[m.localConfirmID].Name())))
	} else if m.confirmID >= 0 && m.confirmID < len(m.entries) {
		lines = append(lines, styleInfo.Render(fmt.Sprintf("确认删除 %q？ (y/N)", m.entries[m.confirmID].Name())))
	}

	// 输入模式
	switch m.mode {
	case modeBrowse:
		// 浏览模式无输入行
	case modePath:
		if m.focus == paneLocal {
			lines = append(lines, styleCursor.Render("上传 ")+"本地路径(Tab 补全): "+m.pathIn.View())
		} else {
			lines = append(lines, styleCursor.Render("下载 ")+"远程路径(Tab 补全): "+m.pathIn.View())
		}
		lines = append(lines, completionLines(m.pathCandidates, m.pathSel)...)
		lines = append(lines, styleHint.Render("Tab 补全  Enter 传输  Esc 取消"))
	case modeNewDir:
		lines = append(lines, styleCursor.Render("新建目录: ")+m.newDirIn.View())
	case modeGoto:
		lines = append(lines, styleCursor.Render("跳转路径: ")+m.gotoIn.View())
		lines = append(lines, completionLines(m.gotoCandidates, m.gotoSel)...)
		lines = append(lines, styleHint.Render("Tab 补全  Enter 跳转  Esc 取消"))
	}

	// 多选状态提示
	if m.hasSel() {
		lines = append(lines, styleHint.Render(fmt.Sprintf("已选中 %d 项（Enter 批量传输 / x 删除 / Esc 取消）", m.selCount())))
	}

	// 状态/进度
	if m.transfer != nil {
		done, total, _, _ := m.transfer.Snapshot()
		lines = append(lines, styleInfo.Render(renderProgress(m.transfer.Name, done, total)))
	} else if m.status != "" {
		lines = append(lines, styleOK.Render(m.status))
	}
	if m.err != "" {
		lines = append(lines, styleError.Render(m.err))
	}
	return lines
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
// 每行末尾追加 1 宽滚动箭头位：表头行在可向上滚动（top>0）时显示 ▴，
// 最后可见行在可向下滚动（top+body<len(entries)）时显示 ▾。
func (m *sftpModel) paneRows(kind, width int) []string {
	return m.paneRowsWithBody(kind, width, m.bodyHeight())
}

func (m *sftpModel) paneRowsWithBody(kind, width, body int) []string {
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
	rows = append(rows, header+scrollArrow(top > 0, "▴"))
	rows = append(rows, styleDim.Render(strings.Repeat("─", width))+" ")

	start, end := 0, len(entries)
	if body > 0 && end > body {
		start, end = top, top+body
		if end > len(entries) {
			end = len(entries)
		}
	}
	for i := start; i < end; i++ {
		down := false
		if i == end-1 {
			down = body > 0 && top+body < len(entries)
		}
		rows = append(rows, m.renderEntry(entries[i], i, cursor, lay, kind)+scrollArrow(down, "▾"))
	}
	return rows
}

// scrollArrow 渲染 1 宽滚动箭头位（show 时显示指示字符，否则空白占位保持对齐）
func scrollArrow(show bool, ch string) string {
	if show {
		return styleDim.Render(ch)
	}
	return " "
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

// completionLines 渲染补全候选（最多 6 个，高亮当前项），供 g/p 输入模式共用。
func completionLines(cands []string, sel int) []string {
	limit := len(cands)
	if limit > 6 {
		limit = 6
	}
	var lines []string
	for i := 0; i < limit; i++ {
		mark := "  "
		if i == sel {
			mark = "▸ "
		}
		line := mark + cands[i]
		if i == sel {
			line = styleSelected.Render(line)
		}
		lines = append(lines, line)
	}
	return lines
}

func renderProgress(name string, done, total int64) string {
	if total <= 0 {
		return fmt.Sprintf("正在传输 %s ... %s", name, sftpc.FormatSize(done))
	}
	pct := float64(done) / float64(total) * 100
	// done 可能超过 total（传输期间源文件变大、目录型遍历跟随符号链接等），必须钳位：
	// 否则 filled > width 会让 strings.Repeat 传入负长度 panic。
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	const width = 20
	filled := int(pct / 100 * width)
	if filled < 0 {
		filled = 0
	}
	if filled > width {
		filled = width
	}
	bar := strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
	return fmt.Sprintf("正在传输 %s  %3.0f%% %s %s/%s", name, pct, bar, sftpc.FormatSize(done), sftpc.FormatSize(total))
}

func renderSFTPFooter() string {
	return "Tab 切换栏  Enter 进入/传输文件  t 传输(本地↑/远程↓)  p 路径传输  Space 多选  g 跳转  ↑/↓ 移动  Backspace 上级  n 新建目录  x 删除  r 刷新  q 返回"
}
