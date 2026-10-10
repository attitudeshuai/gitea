// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

//go:build !windows

package cmd

import (
	"os/exec"
	"syscall"
)

// hookScriptExecCommand runs the hook script directly, the kernel honors its shebang
func hookScriptExecCommand(scriptPath string, scriptArgs []string) *exec.Cmd {
	return exec.Command(scriptPath, scriptArgs...)
}

// hookScriptProcessKiller terminates the script's own process group
type hookScriptProcessKiller struct{}

func (*hookScriptProcessKiller) prepare(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func (*hookScriptProcessKiller) attach(_ *exec.Cmd) {}

func (*hookScriptProcessKiller) close() {}

func (*hookScriptProcessKiller) kill(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		_ = cmd.Process.Kill()
	}
}
