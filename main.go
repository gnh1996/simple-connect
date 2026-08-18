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
			if err := startSSH(s, h); err != nil {
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

// startSSH 使用保存的凭据建立自建交互会话
func startSSH(s *store.Store, h *model.Host) error {
	pass, _ := s.Password(h)
	cl, err := sshc.Connect(h, pass)
	if err != nil {
		return err
	}
	defer cl.Close()
	fmt.Print("\x1b[2J\x1b[H") // 清理屏幕
	return session.StartInteractive(cl)
}

func styleError(msg string) string {
	return lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Render(msg)
}
