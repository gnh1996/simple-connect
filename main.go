package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/lipgloss"

	"simple-connect/internal/exec"
	"simple-connect/internal/model"
	"simple-connect/internal/session"
	sshc "simple-connect/internal/ssh"
	"simple-connect/internal/store"
	"simple-connect/internal/tui"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, styleError("错误: "+err.Error()))
		os.Exit(1)
	}
}

func run() error {
	s, err := store.Load()
	if err != nil {
		return err
	}

	for {
		root := tui.NewRoot(s)
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
			sess, err := startSSH(s, h)
			if err == nil {
				continue // 会话正常结束，回列表
			}
			if errors.Is(err, session.ErrDetach) {
				// 会话中按 Ctrl+X f 挂起会话唤起 SFTP（SSH 连接保持，目录/进程不变），
				// SFTP 页结束后自动恢复同一会话
				if rerr := sftpLoop(s, h, sess); rerr != nil {
					fmt.Fprintln(os.Stderr, styleError("SFTP 页面异常: "+rerr.Error()))
				}
				continue
			}
			fmt.Fprintln(os.Stderr, styleError("连接失败: "+err.Error()))
			fmt.Print("是否降级使用系统 ssh 连接？(y/N): ")
			var ans string
			_, _ = fmt.Scanln(&ans)
			if strings.EqualFold(strings.TrimSpace(ans), "y") {
				if err := exec.RunSSH(h); err != nil {
					fmt.Fprintln(os.Stderr, styleError("系统 ssh 失败: "+err.Error()))
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
			fmt.Fprintln(os.Stderr, styleError("恢复会话失败: "+err.Error()))
			return nil
		}
		return nil // 会话正常结束
	}
}

// startSSH 使用保存的凭据建立自建交互会话并透传。
// detach（Ctrl+X f）时返回挂起的 *session.Handle 与 ErrDetach；会话正常结束返回 nil。
func startSSH(s *store.Store, h *model.Host) (*session.Handle, error) {
	pass, _ := s.Password(h)
	cl, err := sshc.Connect(h, pass)
	if err != nil {
		return nil, err
	}
	return session.StartInteractive(cl)
}

func styleError(msg string) string {
	return lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Render(msg)
}
