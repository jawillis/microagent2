//go:build windows

package mcp

import "os/exec"

func attachPlatformAttrs(cmd *exec.Cmd) {}

func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
