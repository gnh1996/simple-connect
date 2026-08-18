package session

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/charmbracelet/x/term"
	"golang.org/x/crypto/ssh"

	sshc "simple-connect/internal/ssh"
)

// ErrDetach 表示用户在会话中按热键请求切换到 SFTP。
// 与"中断会话"不同：detach 只挂起透传，SSH 连接与远程 shell 保持存活，
// 可通过 Handle.Resume 恢复同一会话（不重连、不修改远程目录/进程）。
var ErrDetach = errors.New("用户请求切换到 SFTP")

// oscPromptCommand 让远程 bash 在每次提示符前输出当前目录（OSC 133;cwd，iTerm2 shell
// integration 兼容）。仅通过会话 env 注入，不修改远程任何文件；
// 若远程 .bashrc 覆盖 PROMPT_COMMAND（powerline 等），自动失跟踪并安全降级。
const oscPromptCommand = `printf '\033]133;cwd=%s\007' "$PWD"`

// cwdHookCommand 首次进入会话时注入远程 shell 的 cwd 追踪钩子（经 stdin 发送，绕开
// sshd 默认 AcceptEnv 仅接受 LANG/LC_* 而拒绝 PROMPT_COMMAND 的限制）：
//   - bash：定义 _sc_cwd 上报函数并追加 PROMPT_COMMAND（保留用户已有钩子）；
//   - zsh：precmd_functions 追加（不覆盖，兼容 oh-my-zsh 等）；zsh 专属语法经 eval
//     包裹，避免 POSIX sh 解析时报语法错误；
//   - sh/dash：两个条件均短路，静默降级（POSIX 无提示符前钩子机制）。
//
// 上报经 OSC 133;cwd；远程 tmux 内用 passthrough 序列（\ePtmux;...）穿透。
// **本命令不含任何终端控制序列**（清行/清屏会干扰 bash readline，导致光标错位、
// 输入不可见）；明文回显由 Handle.run 用 `stty -echo` 包裹隐藏（ECHO 关闭期间
// 发送，命令不回显，随后恢复 ECHO），仅残留一行 `stty -echo`。
// 仅影响提示符前的一次 OSC 输出，不改目录、不执行远程可执行负载。
const cwdHookCommand = `[ -n "$BASH_VERSION" ] && { _sc_cwd(){ if [ -n "$TMUX" ]; then printf '\ePtmux;\e\e]133;cwd=%s\007\e\\' "$PWD"; else printf '\e]133;cwd=%s\007' "$PWD"; fi; }; export PROMPT_COMMAND="_sc_cwd; ${PROMPT_COMMAND}"; }; [ -n "$ZSH_VERSION" ] && { eval '_sc_cwd(){ if [ -n "$TMUX" ]; then printf '\''\ePtmux;\e\e]133;cwd=%s\007\e\\'\'' "$PWD"; else printf '\''\e]133;cwd=%s\007'\'' "$PWD"; fi; }; precmd_functions+=(_sc_cwd);'; }`

// localeEnvKeys 透传到远程会话的环境变量（对齐 OpenSSH 客户端默认 SendEnv LANG LC_*）。
// 不传时远程落到 C/POSIX locale：readline 按字节处理输入，中文/非 ASCII 输入后
// 命令行重绘光标错位（表现为跳行首），CJK 宽度对齐也全乱。
var localeEnvKeys = []string{
	"LANG", "LC_ALL", "LC_CTYPE", "LC_MESSAGES", "LC_COLLATE",
	"LC_TIME", "LC_NUMERIC", "LC_MONETARY", "LC_PAPER", "LC_NAME",
	"LC_ADDRESS", "LC_TELEPHONE", "LC_MEASUREMENT", "LC_IDENTIFICATION",
}

// sessionEnv 收集本机需透传到远程会话的环境变量（仅取非空值）
func sessionEnv() map[string]string {
	out := map[string]string{}
	for _, k := range localeEnvKeys {
		if v := os.Getenv(k); v != "" {
			out[k] = v
		}
	}
	return out
}

// oscTracker 从输出字节流中提取 shell integration 的 cwd 标记：
// `ESC ] 133;cwd=<path> BEL`。路径来自远程输出，仅用于 UI 展示/定位，
// 绝不拼入本机 shell 执行。解析出的 OSC 序列从透传输出中剔除（对齐 Tabby），
// 不污染终端显示。
type oscTracker struct {
	mu    sync.Mutex
	cwd   string
	buf   []byte // 跨 Write 边界残留（含序列头部但未收尾的部分）
	out   io.Writer
	quiet bool // 挂起期间静音：丢弃输出但继续消费（防止远端阻塞），OSC 扫描照常
}

func newOSCTracker(out io.Writer) *oscTracker {
	return &oscTracker{out: out, buf: make([]byte, 0, 256)}
}

// setQuiet 切换静音模式。detach 挂起期间置 true（避免远程输出污染 SFTP 页），
// 恢复透传时置 false。
func (t *oscTracker) setQuiet(q bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.quiet = q
}

func (t *oscTracker) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	// 关键：合并残留 + 新数据时必须**拷贝**到独立切片，不能 append(t.buf, p...) 直接
	// 复用 t.buf 的底层数组。否则 data 与 t.buf 共享同一数组，scan 在遍历 data 时
	// 又会 t.buf = append(t.buf, tail...) 写入同一数组，导致 data 未处理部分被覆盖
	// → 输出重复/损坏（实测：登录横幅被重复多份）。
	buf := make([]byte, 0, len(t.buf)+len(p))
	buf = append(buf, t.buf...)
	buf = append(buf, p...)
	cleaned := t.scan(buf)
	if !t.quiet {
		_, _ = t.out.Write(cleaned)
	}
	return len(p), nil
}

// scan 扫描数据，提取 OSC 133;cwd 序列并从输出中剔除；返回应透传的字节。
// 跨 Write 边界未收尾的序列头部保留在 t.buf，与下次数据合并。
// 注意：不能以「第一个 ESC 到第一个 BEL」为界——cwd OSC 之前可能已有其他 ESC
// 序列（如 \x1b[?2004h 括号粘贴、光标移动），首个 ESC 并非 OSC 起点。
// 必须精确定位 `\x1b]133;cwd=` 前缀。
func (t *oscTracker) scan(data []byte) []byte {
	const oscMaxKeep = 4096
	const prefix = "]133;cwd="
	var out []byte
	t.buf = t.buf[:0]
	for len(data) > 0 {
		i := bytes.Index(data, []byte{0x1b, ']'})
		if i < 0 {
			// 本段无任何 ESC 序列起点：全部直接透传。无需在 t.buf 保留任何字节
			//（否则会把已透传内容在下次 Write 时重复输出——历史 bug：登录横幅被
			// 重复多份）。跨 Write 边界的序列起点会在 i>=0 且 j<0 分支保留。
			out = append(out, data...)
			return out
		}
		out = append(out, data[:i]...)
		// 从该 ESC 之后找 BEL 作为 OSC 终止符
		j := bytes.IndexByte(data[i:], 0x07)
		if j < 0 {
			// 序列未收尾：保留尾部（含序列头部）跨边界续扫
			tail := data[i:]
			if len(tail) > oscMaxKeep {
				tail = tail[len(tail)-oscMaxKeep:]
			}
			t.buf = append(t.buf, tail...)
			return out
		}
		seq := data[i : i+j]
		if bytes.HasPrefix(seq[1:], []byte(prefix)) {
			t.cwd = string(seq[1+len(prefix):]) // 剔除该序列
		} else {
			out = append(out, seq...) // 其他序列原样透传
			out = append(out, 0x07)
		}
		data = data[i+j+1:]
	}
	return out
}

// Cwd 返回最近一次跟踪到的远程工作目录（未跟踪到返回空串）
func (t *oscTracker) Cwd() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.cwd
}

// 会话中唤起 SFTP 的热键序列：Ctrl+X 后按 f
const (
	detachCtrlX = 0x18
	detachFKey  = 'f'
)

// resolveTermSize 将 term.GetSize 返回的 (width, height) 换算为 PTY 请求所需的
// (rows, cols)。term.GetSize 的返回值语义是 (宽, 高)，而 RequestPty/WindowChange
// 的参数语义是 (行, 列)，二者顺序相反，必须在此交换；尺寸非法时回退 24x80。
func resolveTermSize(width, height int) (rows, cols int) {
	if width <= 0 || height <= 0 {
		return 24, 80
	}
	return height, width
}

// StartInteractive 建立自建交互式 SSH 会话并开始透传。
// 认证已由 sshc.Connect 使用保存的凭据完成；这里负责将本地终端置为 raw 模式，
// 与远程 PTY 做字节透传，结束后恢复终端。
//
// 会话中按 Ctrl+X f 会**挂起**透传并返回 ErrDetach（SSH 连接与远程 shell 保持
// 存活，目录/进程不受影响），返回的 *Handle 可经 Resume 恢复同一会话。
// 会话正常结束/异常时清理连接并返回 (nil, err)。
func StartInteractive(c *sshc.Client) (*Handle, error) {
	h := &Handle{cl: c}
	if err := h.start(); err != nil {
		return nil, err
	}
	if err := h.run(); errors.Is(err, ErrDetach) {
		return h, ErrDetach
	} else if err != nil {
		h.close()
		return nil, err
	}
	h.close()
	return nil, nil
}

// setupSession 建立 PTY 会话：请求 PTY、注入 locale/PROMPT_COMMAND、绑定输出、
// 启动远程 shell。返回 session、cwd 追踪器与输入管道。
func setupSession(cl *sshc.Client, out, errOut io.Writer, rows, cols int) (*ssh.Session, *oscTracker, io.WriteCloser, error) {
	termName := os.Getenv("TERM")
	if termName == "" {
		termName = "xterm-256color"
	}
	s, err := cl.NewTerminalSession(termName, rows, cols)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("请求远程 PTY 失败: %w", err)
	}
	// 透传 UTF-8 相关 locale（对齐 OpenSSH 客户端默认 SendEnv LANG LC_*）。
	// 不传时远程落到 C/POSIX locale：readline 按字节处理多字节输入，
	// 中文输入后光标位置计算错乱（表现为跳动/错位），ls/vim 中 CJK 宽度对齐也全乱。
	for k, v := range sessionEnv() {
		_ = s.Setenv(k, v)
	}
	// 注入 cwd 追踪钩子（OSC 133;cwd）：远程 bash 每次提示符前上报当前目录。
	// 被远程 .bashrc 覆盖时自动失跟踪（安全降级）。
	_ = s.Setenv("PROMPT_COMMAND", oscPromptCommand)

	tracker := newOSCTracker(out)
	s.Stdout = tracker
	s.Stderr = errOut
	inPipe, err := s.StdinPipe()
	if err != nil {
		_ = s.Close()
		return nil, nil, nil, fmt.Errorf("获取会话输入失败: %w", err)
	}
	if err := s.Shell(); err != nil {
		_ = inPipe.Close()
		_ = s.Close()
		return nil, nil, nil, fmt.Errorf("启动远程 shell 失败: %w", err)
	}
	return s, tracker, inPipe, nil
}

// detach 时挂起：不关闭会话与输入管道，输出静音，返回 trackedCwd。
func runOnce(s *ssh.Session, in io.Reader, tracker *oscTracker, inPipe io.WriteCloser, waitDone chan error) (string, bool, error) {
	stop := make(chan struct{})
	if sp, ok := in.(stoppable); ok {
		sp.setStop(stop)
	}
	detached := make(chan struct{})
	go pumpInput(inPipe, in, stop, detached)

	select {
	case err := <-waitDone:
		close(stop)
		// 远程进程正常退出（含 exit 0/非 0、信号）视为正常结束
		if err == nil {
			return "", false, nil
		}
		var exitErr *ssh.ExitError
		var missingErr *ssh.ExitMissingError
		if errors.As(err, &exitErr) || errors.As(err, &missingErr) {
			return "", false, nil
		}
		if errors.Is(err, io.EOF) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("会话异常结束: %w", err)
	case <-detached:
		close(stop)
		tracker.setQuiet(true) // 挂起期间丢弃远程输出（防污染 SFTP 页），连接保持
		return tracker.Cwd(), true, nil
	}
}

// runSession 单次透传（in/out 可注入以便测试；不负责 raw 终端状态）。
// 本地输入经热键检测后转发远程；检测到 Ctrl+X f 时**挂起**会话并返回 ErrDetach
// （不关闭连接，由调用方负责最终关闭）。返回 (跟踪到的远程工作目录, error)。
func runSession(c *sshc.Client, in io.Reader, out, errOut io.Writer, rows, cols int) (string, error) {
	s, tracker, inPipe, err := setupSession(c, out, errOut, rows, cols)
	if err != nil {
		return "", err
	}
	stopResize := startResize(s)
	defer stopResize()

	waitDone := make(chan error, 1)
	go func() { waitDone <- s.Wait() }()

	cwd, detached, rerr := runOnce(s, in, tracker, inPipe, waitDone)
	if detached {
		return cwd, ErrDetach // 挂起：保持会话，由调用方关闭连接
	}
	_ = inPipe.Close()
	_ = s.Close()
	return "", rerr
}

// Handle 持有一次 SSH 交互会话，支持 detach 挂起与 Resume 恢复（不重连）。
type Handle struct {
	cl       *sshc.Client
	s        *ssh.Session
	inPipe   io.WriteCloser
	tracker  *oscTracker
	waitDone chan error
	rows     int
	cols     int
	started  bool // 是否已进行首次透传（首次进入清屏 + 注入 cwd 钩子；恢复时原样不动）

	// 测试注入：非 nil 时绕过真实终端（raw/清屏/输入源），使用注入的 IO
	testIn              io.Reader
	testOut, testErrOut io.Writer
}

func (h *Handle) start() error {
	if h.testIn == nil {
		width, height, _ := term.GetSize(os.Stdout.Fd())
		h.rows, h.cols = resolveTermSize(width, height)
	}
	out := h.testOut
	if out == nil {
		out = os.Stdout
	}
	errOut := h.testErrOut
	if errOut == nil {
		errOut = os.Stderr
	}
	s, tracker, inPipe, err := setupSession(h.cl, out, errOut, h.rows, h.cols)
	if err != nil {
		return err
	}
	h.s, h.tracker, h.inPipe = s, tracker, inPipe
	h.waitDone = make(chan error, 1)
	go func() { h.waitDone <- s.Wait() }()
	return nil
}

// newTestHandle 测试构造：注入 IO 与终端尺寸，不操作真实终端（raw/清屏/输入源）。
func newTestHandle(cl *sshc.Client, in io.Reader, out io.Writer, rows, cols int) (*Handle, error) {
	h := &Handle{cl: cl, testIn: in, testOut: out, testErrOut: out, rows: rows, cols: cols}
	if err := h.start(); err != nil {
		return nil, err
	}
	return h, nil
}

// run 执行一次透传：置 raw、启动输入源与尺寸监听；首次进入时清屏并注入 cwd 钩子，
// 恢复（Resume）时原样恢复（不清屏、不发任何字节，画面保持 detach 时状态）。
// detach 时挂起返回 ErrDetach。
func (h *Handle) run() error {
	h.tracker.setQuiet(false)
	var in io.Reader
	if h.testIn != nil {
		in = h.testIn
	} else {
		fd := os.Stdin.Fd()
		old, err := term.MakeRaw(fd)
		if err == nil {
			defer func() { _ = term.Restore(fd, old) }()
		}
		var cleanup func()
		in, cleanup = newInteractiveInput()
		if cleanup != nil {
			defer cleanup()
		}
		stopResize := startResize(h.s)
		defer stopResize()

		// 刷新当前终端尺寸并同步远程（挂起期间尺寸可能变化；window-change 是尺寸
		// 通知，无语义副作用，等价于用户 resize 终端）
		if width, height, err := term.GetSize(os.Stdout.Fd()); err == nil {
			h.rows, h.cols = resolveTermSize(width, height)
		}
		_ = h.s.WindowChange(h.rows, h.cols)

		if !h.started {
			fmt.Print("\x1b[2J\x1b[H") // 仅首次进入清屏
		}
	}
	// 仅首次进入：注入 cwd 追踪钩子（shell 在提示符前自动上报目录）。
	// 用 `stty -echo` 包裹使注入命令不回显（随后恢复 ECHO），避免在命令内输出
	// 清行/清屏控制序列干扰 bash readline（光标错位/输入不可见）。
	// 从 SFTP 恢复时不发任何字节（原样恢复）。
	if !h.started {
		_, _ = h.inPipe.Write([]byte("stty -echo\r" + cwdHookCommand + "; stty echo\r"))
		h.started = true
	}

	_, detached, err := runOnce(h.s, in, h.tracker, h.inPipe, h.waitDone)
	if detached {
		return ErrDetach
	}
	return err
}

// Resume 恢复透传（detach 挂起后复用同一会话）；再次 detach 返回 ErrDetach。
func (h *Handle) Resume() error {
	err := h.run()
	if errors.Is(err, ErrDetach) {
		return ErrDetach
	}
	h.close()
	return err
}

// Close 关闭会话与连接（幂等）。会话透传结束时内部已调用；
// 供调用方在挂起状态下兜底释放连接（如 SFTP 页异常退出时）。
func (h *Handle) Close() {
	h.close()
}

// close 关闭会话与连接（仅在会话真正结束时调用；detach 挂起期间不得调用）。
func (h *Handle) close() {
	if h.s != nil {
		_ = h.s.Close()
		h.s = nil
	}
	if h.cl != nil {
		_ = h.cl.Close()
		h.cl = nil
	}
}

// SSHClient 返回会话所在 SSH 连接（SFTP 复用，不重新认证）。
func (h *Handle) SSHClient() *sshc.Client {
	if h == nil {
		return nil
	}
	return h.cl
}

// Cwd 返回最近跟踪到的远程工作目录（未跟踪到返回空串，仅供 SFTP 初始定位）。
func (h *Handle) Cwd() string {
	if h == nil || h.tracker == nil {
		return ""
	}
	return h.tracker.Cwd()
}

// stoppable 支持在 detach 时主动停止的输入源（unix 非阻塞轮询 stdin 实现）
type stoppable interface {
	setStop(chan struct{})
}

// pumpInput 读取本地输入，经热键检测后转发远程会话。
// 检测到 Ctrl+X f 时关闭 detached 并返回（**挂起**：不关闭输入管道与会话）；
// 输入流结束或 stop 关闭时退出，输入流结束时向远程发送 EOF。
func pumpInput(w io.WriteCloser, in io.Reader, stop chan struct{}, detached chan struct{}) {
	var sc detachScanner
	buf := make([]byte, 512)
	for {
		select {
		case <-stop:
			return
		default:
		}
		n, err := in.Read(buf)
		if err != nil {
			_ = w.Close() // 输入流结束 → 远程收到 EOF
			return
		}
		fwd, hit := sc.feed(buf[:n])
		if len(fwd) > 0 {
			if _, werr := w.Write(fwd); werr != nil {
				_ = w.Close()
				return
			}
		}
		if hit {
			close(detached)
			return // 挂起：保留会话与输入管道，供 Resume 恢复
		}
	}
}

// detachScanner 前缀状态机：检测 Ctrl+X f，输出应转发的字节
type detachScanner struct {
	pending bool
}

func (sc *detachScanner) feed(data []byte) (forward []byte, detach bool) {
	for _, b := range data {
		if sc.pending {
			sc.pending = false
			if b == detachFKey {
				return forward, true
			}
			forward = append(forward, detachCtrlX)
		}
		if b == detachCtrlX {
			sc.pending = true
		} else {
			forward = append(forward, b)
		}
	}
	return forward, false
}
