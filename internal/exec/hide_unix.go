//go:build !windows

package exec

import "os/exec"

func hideWindow(cmd *exec.Cmd) {}
