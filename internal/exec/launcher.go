package exec

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"

	"simple-connect/internal/model"
)

// RunSSH 通过系统 ssh 命令建立交互式连接（需退出 TUI 后调用）
func RunSSH(h *model.Host) error {
	var args []string
	if h.Port > 0 && h.Port != 22 {
		args = append(args, "-p", strconv.Itoa(h.Port))
	}
	target := h.Host
	if h.User != "" {
		target = h.User + "@" + h.Host
	}
	args = append(args, target)
	return run("ssh", args, nil)
}

// RunSFTP 通过系统 sftp 命令建立文件传输会话（需退出 TUI 后调用）
func RunSFTP(h *model.Host) error {
	var args []string
	if h.Port > 0 && h.Port != 22 {
		args = append(args, "-P", strconv.Itoa(h.Port))
	}
	if h.KeyPath != "" {
		args = append(args, "-i", h.KeyPath)
	}
	target := h.Host
	if h.User != "" {
		target = h.User + "@" + h.Host
	}
	args = append(args, target)
	return run("sftp", args, nil)
}

// run 启动外部进程并接管终端
func run(name string, args []string, env []string) error {
	bin, err := exec.LookPath(name)
	if err != nil {
		return fmt.Errorf("未找到命令 %q，请确认已安装（Windows 请安装 OpenSSH 客户端）", name)
	}
	cmd := exec.Command(bin, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), env...)
	if runtime.GOOS == "windows" {
		// 隐藏 Windows 下可能弹出的控制台窗口
		hideWindow(cmd)
	}
	return cmd.Run()
}
