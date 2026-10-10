// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package cmd

import (
	"os/exec"
	"path/filepath"

	"gitea.dev/modules/log"
	"gitea.dev/modules/setting"

	"golang.org/x/sys/windows"
)

// hookScriptExecCommand runs the hook script through the configured shell (Git for Windows bash),
// extensionless shell scripts cannot be executed directly by CreateProcess
func hookScriptExecCommand(scriptPath string, scriptArgs []string) *exec.Cmd {
	return exec.Command(hookScriptShellPath(), append([]string{scriptPath}, scriptArgs...)...)
}

func hookScriptShellPath() string {
	if _, err := exec.LookPath(setting.ScriptType); err == nil {
		return setting.ScriptType
	}
	if gitPath := setting.Git.Path; gitPath != "" {
		// Git for Windows layout: git lives in <Git>/cmd/git.exe and bash in <Git>/bin/bash.exe
		if _, err := exec.LookPath(gitPath); err == nil {
			candidate := filepath.Join(filepath.Dir(filepath.Dir(gitPath)), "bin", "bash.exe")
			if _, err := exec.LookPath(candidate); err == nil {
				return candidate
			}
		}
	}
	return setting.ScriptType
}

// hookScriptProcessKiller terminates the whole shell process tree.
// MSYS bash does not keep Windows parent-child links, so a Job Object is needed
// to make sure child processes (e.g. "sleep") are terminated as well.
type hookScriptProcessKiller struct {
	job windows.Handle
}

func (k *hookScriptProcessKiller) prepare(_ *exec.Cmd) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		log.Error("hookScriptProcessKiller.prepare: unable to create job object: %v", err)
		return
	}
	k.job = job
}

func (k *hookScriptProcessKiller) attach(cmd *exec.Cmd) {
	if k.job == 0 || cmd.Process == nil {
		return
	}
	handle, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		log.Error("hookScriptProcessKiller.attach: %v", err)
		return
	}
	defer func() { _ = windows.CloseHandle(handle) }()
	if err := windows.AssignProcessToJobObject(k.job, handle); err != nil {
		log.Error("hookScriptProcessKiller.attach: %v", err)
	}
}

func (k *hookScriptProcessKiller) kill(cmd *exec.Cmd) {
	if k.job != 0 {
		_ = windows.TerminateJobObject(k.job, 1)
		_ = windows.CloseHandle(k.job)
		k.job = 0
		return
	}
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

func (k *hookScriptProcessKiller) close() {
	if k.job != 0 {
		_ = windows.CloseHandle(k.job)
		k.job = 0
	}
}
