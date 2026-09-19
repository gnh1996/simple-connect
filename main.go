package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/lipgloss"

	"simple-connect/internal/applog"
	"simple-connect/internal/exec"
	"simple-connect/internal/model"
	"simple-connect/internal/session"
	sshc "simple-connect/internal/ssh"
	"simple-connect/internal/store"
	"simple-connect/internal/tui"
)

func main() {
	applog.Init()
	code := 0
	if err := run(); err != nil {
		reportErr("错误", err)
		code = 1
	}
	applog.Close()
	os.Exit(code)
}

// reportErr 统一错误出口：终端高亮提示 + 落盘日志（供事后诊断）。
func reportErr(prefix string, err error) {
	msg := prefix + ": " + err.Error()
	_, _ = fmt.Fprintln(os.Stderr, styleError(msg))
	applog.Errorf("%s", msg)
}

func run() error {
	s, err := store.Load()
	if err != nil {
		return err
	}

	// listState 跨 tea.Program 生命周期的列表页快照：SSH 会话必须退出 TUI 才能
	// 接管终端，重建 Root 时注入快照，过滤词/光标/在线状态不再全部重置。
	var listState *tui.ListState
	for {
		root := tui.NewRootWithListState(s, listState)
		p := tea.NewProgram(root)
		result, err := p.Run()
		if err != nil {
			if errors.Is(err, tea.ErrInterrupted) {
				return nil // Ctrl+C 优雅退出
			}
			return err
		}
		rm, ok := result.(*tui.Root)
		if !ok {
			return nil
		}
		switch rm.Action {
		case tui.ActionQuit:
			return nil
		case tui.ActionSSH:
			h := s.Find(rm.HostID)
			if h == nil {
				continue
			}
			listState = rm.ListState() // 进入会话前保存列表状态，会话/SFTP 往返后恢复
			fmt.Printf("正在连接 %s@%s ...（会话中 Ctrl+X f 可唤起 SFTP）\n", h.User, h.Addr())
			sess, err := startSSH(s, h)
			if err == nil {
				continue // 会话正常结束，回列表
			}
			if errors.Is(err, session.ErrDetach) {
				// 会话中按 Ctrl+X f 挂起会话唤起 SFTP（SSH 连接保持，目录/进程不变），
				// SFTP 页结束后自动恢复同一会话
				if rerr := sftpLoop(s, h, sess); rerr != nil {
					reportErr("SFTP 页面异常", rerr)
				}
				continue
			}
			reportErr("连接失败", err)
			fmt.Print("是否降级使用系统 ssh 连接？(y/N): ")
			var ans string
			_, _ = fmt.Scanln(&ans)
			if strings.EqualFold(strings.TrimSpace(ans), "y") {
				if err := exec.RunSSH(h); err != nil {
					reportErr("系统 ssh 失败", err)
				}
			}
		default:
			return nil
		}
	}
}

// sftpLoop 会话 ⇄ SFTP 循环：挂起的会话（Handle）在 Ctrl+X f 与 SFTP 页之间往返。
// SFTP 复用同一 SSH 连接（sess.SSHClient），q 后恢复同一会话透传；再次挂起则继续循环。
func sftpLoop(s *store.Store, h *model.Host, sess *session.Handle) error {
	defer sess.Close() // 兜底：任何退出路径都释放 SSH 连接（幂等，正常结束已关闭）
	for {
		root := tui.NewSFTPRoot(s, h, sess)
		p := tea.NewProgram(root)
		result, err := p.Run()
		if err != nil {
			if errors.Is(err, tea.ErrInterrupted) {
				return nil
			}
			return err
		}
		rm, ok := result.(*tui.Root)
		if !ok || rm.Action != tui.ActionResumeSSH {
			return nil // 正常返回列表
		}
		err = sess.Resume()
		if errors.Is(err, session.ErrDetach) {
			continue // 再次挂起唤起 SFTP
		}
		if err != nil {
			return fmt.Errorf("恢复会话失败: %w", err)
		}
		return nil // 会话正常结束
	}
}

// startSSH 使用保存的凭据建立自建交互会话并透传。
// detach（Ctrl+X f）时返回挂起的 *session.Handle 与 ErrDetach；会话正常结束返回 nil。
func startSSH(s *store.Store, h *model.Host) (*session.Handle, error) {
	pass, _ := s.Password(h)
	// 连接所有权在本函数内已显式管理（defer + transferred）：成功/挂起由会话接管，
	// 其余路径释放。静态检查无法跨函数推断 StartInteractive 的所有权转移，故抑制。
	//noinspection GoResourceLeak
	cl, err := connectWithFingerprintConfirm(h, pass)
	// 连接所有权：仅「会话正常结束」（StartInteractive 内部已关）与「挂起」
	// （ErrDetach，由 Handle 持有）算转移；其余任何返回路径都由 defer 兜底释放
	//（重复关闭幂等）。
	transferred := false
	defer func() {
		if !transferred && cl != nil {
			_ = cl.Close()
		}
	}()
	if err != nil {
		return nil, err
	}
	hnd, err := session.StartInteractive(cl)
	if err == nil || errors.Is(err, session.ErrDetach) {
		transferred = true
	}
	return hnd, err
}

// connectWithFingerprintConfirm 建立 SSH 连接；首次连接时展示指纹并征得确认、
// 写入 known_hosts 后重连（对齐 OpenSSH ask 模式）。成功时连接所有权交给调用方。
func connectWithFingerprintConfirm(h *model.Host, pass string) (*sshc.Client, error) {
	cl, err := sshc.Connect(h, pass)
	if err == nil {
		return cl, nil // 所有权转移给调用方
	}
	if cl != nil {
		_ = cl.Close() // 防御：Connect 契约是失败时不返回可用连接
	}
	var uk *sshc.UnknownHostKeyError
	if !errors.As(err, &uk) {
		return nil, err
	}
	// 首次连接：展示指纹并征得用户确认后信任，再重新连接
	if !confirmHostFingerprint(uk) {
		return nil, errors.New("已拒绝信任主机指纹，取消连接")
	}
	if terr := sshc.TrustHostKey(uk); terr != nil {
		return nil, terr
	}
	return sshc.Connect(h, pass)
}

// confirmHostFingerprint 在终端展示主机指纹并请求确认（TUI 已退出、终端恢复非 raw 模式）。
func confirmHostFingerprint(uk *sshc.UnknownHostKeyError) bool {
	fmt.Printf("无法确认主机 %q 的真实性。\n  指纹: %s\n是否信任并继续连接？(y/N): ",
		uk.Hostname, uk.Fingerprint)
	var ans string
	_, _ = fmt.Scanln(&ans)
	return strings.EqualFold(strings.TrimSpace(ans), "y")
}

func styleError(msg string) string {
	return lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Render(msg)
}
