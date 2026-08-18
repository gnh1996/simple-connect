package main

import (
	"errors"
	"fmt"
	"os"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/lipgloss"

	"simple-connect/internal/exec"
	"simple-connect/internal/store"
	"simple-connect/internal/tui"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Render("错误: "+err.Error()))
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
			fmt.Print("\x1b[2J\x1b[H") // 清理屏幕
			if err := exec.RunSSH(h); err != nil {
				fmt.Fprintln(os.Stderr, "连接失败: "+err.Error())
			}
			fmt.Print("\n按 Enter 返回管理界面...")
			_, _ = fmt.Scanln()
		default:
			return nil
		}
	}
}
