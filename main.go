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
			cwd, err := startSSH(s, h)
			if err != nil {
				if errors.Is(err, session.ErrDetach) {
					// 会话中按 Ctrl+X f 唤起 SFTP：SFTP 页结束后自动重连会话
					if rerr := resumeLoop(s, h, cwd); rerr != nil {
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
			}
		default:
			return nil
		}
	}
}

// resumeLoop 会话 ⇄ SFTP 循环：热键唤起 SFTP 页，退出后重连会话，再次唤起则继续循环。
// cwd 为会话内跟踪到的远程工作目录（空串=未跟踪到，SFTP 用默认路径）。
func resumeLoop(s *store.Store, h *model.Host, cwd string) error {
	for {
		root := tui.NewSFTPRoot(s, h, cwd)
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
		var serr error
		cwd, serr = startSSH(s, h)
		if serr != nil {
			if errors.Is(serr, session.ErrDetach) {
				continue // 再次唤起 SFTP（cwd 已更新为本次会话跟踪值）
			}
			fmt.Fprintln(os.Stderr, styleError("重连会话失败: "+serr.Error()))
			return nil
		}
		return nil
	}
}

// startSSH 使用保存的凭据建立自建交互会话。
// 返回 (会话内跟踪到的远程工作目录, error)；detach 时 cwd 供 SFTP 页定位。
func startSSH(s *store.Store, h *model.Host) (string, error) {
	pass, _ := s.Password(h)
	cl, err := sshc.Connect(h, pass)
	if err != nil {
		return "", err
	}
	defer cl.Close()
	fmt.Print("\x1b[2J\x1b[H") // 清理屏幕
	return session.StartInteractive(cl)
}

func styleError(msg string) string {
	return lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Render(msg)
}
