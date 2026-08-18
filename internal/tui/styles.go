package tui

import (
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
)

// padRight 按显示宽度右填充（正确处理 CJK 双宽字符）
func padRight(s string, width int) string {
	return runewidth.FillRight(s, width)
}

// 全局样式
var (
	styleTitle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("39")) // 亮蓝

	styleHeader = lipgloss.NewStyle().
			Foreground(lipgloss.Color("250")).
			Bold(true)

	styleCursor = lipgloss.NewStyle().
			Foreground(lipgloss.Color("39")).
			Bold(true)

	styleSelected = lipgloss.NewStyle().
			Foreground(lipgloss.Color("0")).
			Background(lipgloss.Color("39"))

	styleOnline  = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	styleOffline = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	styleUnknown = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))

	styleDim   = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	styleError = lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Bold(true)
	styleInfo  = lipgloss.NewStyle().Foreground(lipgloss.Color("220"))
	styleOK    = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))

	styleFooter = lipgloss.NewStyle().
			Foreground(lipgloss.Color("250")).
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("240")).
			Padding(0, 1)

	styleFormLabel = lipgloss.NewStyle().
			Foreground(lipgloss.Color("250")).
			Width(8)

	styleHint = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
)
