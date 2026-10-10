// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"gitea.dev/modules/git"
	"gitea.dev/modules/json"
	"gitea.dev/modules/log"
	"gitea.dev/modules/private"
	repo_module "gitea.dev/modules/repository"

	"github.com/urfave/cli/v3"
)

func newHookRunScriptCommand() *cli.Command {
	return &cli.Command{
		Name:        "run-script",
		Usage:       "(internal) Execute a custom Git hook script with timeout and output limits",
		Description: "This command should only be called by the generated Git hook wrapper",
		Hidden:      true,
		Action:      runHookScript,
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "phase", Required: true},
			&cli.StringFlag{Name: "state", Required: true},
		},
	}
}

// hookScriptAPITimeout bounds the local internal API calls so a hanging Gitea cannot stall a push
const hookScriptAPITimeout = 5 * time.Second

// hookScriptState is cached per wrapper invocation in a temporary state file
type hookScriptState struct {
	ScriptTimeout int   `json:"script_timeout"`
	TotalTimeout  int   `json:"total_timeout"`
	MaxOutputSize int64 `json:"max_output_size"`
	DeadlineUnix  int64 `json:"deadline_unix"` // absolute unix seconds when the cumulative budget ends, 0 = no limit
}

// hookScriptBudget returns the effective timeout for the next script and whether it must be skipped.
// Time values are floored to whole seconds, so the final script may run up to roughly one second past
// the configured total timeout. A zero timeout means no limit.
func hookScriptBudget(state *hookScriptState, now time.Time) (timeout time.Duration, skip bool) {
	var perScript, remaining time.Duration
	if state.ScriptTimeout > 0 {
		perScript = time.Duration(state.ScriptTimeout) * time.Second
	}
	if state.DeadlineUnix > 0 {
		remainSeconds := state.DeadlineUnix - now.Unix()
		if remainSeconds <= 0 {
			return 0, true
		}
		remaining = time.Duration(remainSeconds) * time.Second
	}
	switch {
	case perScript == 0:
		return remaining, false
	case remaining == 0:
		return perScript, false
	default:
		return min(perScript, remaining), false
	}
}

func loadHookScriptState(ctx context.Context, statePath, ownerName, repoName string) *hookScriptState {
	state := new(hookScriptState)
	if data, err := os.ReadFile(statePath); err == nil {
		if len(data) == 0 {
			// the wrapper creates the empty file with mktemp, it is filled in below
		} else if err := json.Unmarshal(data, state); err == nil {
			return state
		} else {
			log.Error("loadHookScriptState: ignoring unreadable state file %q: %v", statePath, err)
		}
	}

	// the settings fetch must not block the push: on failure fall back to unlimited execution
	requestCtx, cancel := context.WithTimeout(ctx, hookScriptAPITimeout)
	defer cancel()
	limits, extra := private.HookGetScriptSettings(requestCtx, ownerName, repoName)
	if extra.HasError() {
		log.Error("loadHookScriptState: unable to get hook script settings: %v", extra.Error)
		return state
	}
	state.ScriptTimeout = limits.ScriptTimeout
	state.TotalTimeout = limits.TotalTimeout
	state.MaxOutputSize = limits.MaxOutputSize
	if state.TotalTimeout > 0 {
		state.DeadlineUnix = time.Now().Unix() + int64(state.TotalTimeout)
	}
	if data, err := json.Marshal(state); err == nil {
		if err := os.WriteFile(statePath, data, 0o600); err != nil {
			log.Error("loadHookScriptState: unable to write state file %q: %v", statePath, err)
		}
	}
	return state
}

// hookOutputLimit counts the combined forwarded stdout/stderr bytes of one script
type hookOutputLimit struct {
	maxBytes  int64 // 0 = unlimited
	written   int64
	truncated bool
	mu        sync.Mutex
}

type hookLimitedWriter struct {
	limit  *hookOutputLimit
	out    io.Writer
	failed bool // the pusher side stopped reading; keep draining but discard the rest
}

func (w *hookLimitedWriter) Write(p []byte) (int, error) {
	w.limit.mu.Lock()
	defer w.limit.mu.Unlock()

	total := len(p)
	if w.failed {
		return total, nil // excess bytes are drained but discarded
	}
	if w.limit.maxBytes != 0 && int64(len(p)) > w.limit.maxBytes-w.limit.written {
		p = p[:max(0, w.limit.maxBytes-w.limit.written)]
		w.limit.truncated = true
	}
	if len(p) > 0 {
		n, err := w.out.Write(p)
		w.limit.written += int64(n)
		if err != nil {
			// a broken pusher pipe must never block draining the script's output
			w.failed = true
		}
	}
	return total, nil
}

func runHookScript(ctx context.Context, c *cli.Command) error {
	setup(ctx, false)

	phase := c.String("phase")
	scriptArgs := c.Args().Slice()
	if !git.IsValidHookName(phase) || len(scriptArgs) == 0 {
		return fmt.Errorf("runHookScript: invalid arguments (phase=%q args=%v)", phase, scriptArgs)
	}
	scriptPath := scriptArgs[0]
	hookName := filepath.Base(scriptPath)

	ownerName := os.Getenv(repo_module.EnvRepoUsername)
	repoName := strings.TrimSuffix(os.Getenv(repo_module.EnvRepoName), ".wiki")
	isWiki, _ := strconv.ParseBool(os.Getenv(repo_module.EnvRepoIsWiki))

	state := loadHookScriptState(ctx, c.String("state"), ownerName, repoName)

	now := time.Now()
	timeout, skipped := hookScriptBudget(state, now)
	if skipped {
		recordHookScriptRun(ctx, ownerName, repoName, isWiki, phase, hookName, now, 0, nil, false, false, true, 0)
		return nil
	}

	record := runHookScriptProcessWithIO(scriptPath, scriptArgs[1:], state.MaxOutputSize, timeout, os.Stdin, os.Stdout, os.Stderr)
	recordHookScriptRun(ctx, ownerName, repoName, isWiki, phase, hookName, record.start, record.duration, record.exitCode, record.timedOut, record.truncated, false, state.MaxOutputSize)

	if record.timedOut {
		fmt.Fprintf(os.Stderr, "\nGitea: hook script %q timed out after %d second(s) and was terminated\n", hookName, (record.duration+500*time.Millisecond)/time.Second)
		return cli.Exit("", 124)
	}
	if record.startErr != nil {
		fmt.Fprintf(os.Stderr, "\nGitea: unable to execute hook script %q: %v\n", hookName, record.startErr)
		return cli.Exit("", 1)
	}
	if record.exitCode != nil && *record.exitCode != 0 {
		return cli.Exit("", *record.exitCode)
	}
	return nil
}

type hookScriptProcessResult struct {
	start     time.Time
	duration  time.Duration
	exitCode  *int
	timedOut  bool
	truncated bool
	startErr  error
}

func runHookScriptProcessWithIO(scriptPath string, scriptArgs []string, maxOutputSize int64, timeout time.Duration, stdin io.Reader, stdout, stderr io.Writer) *hookScriptProcessResult {
	result := new(hookScriptProcessResult)
	result.start = time.Now()
	defer func() {
		result.duration = time.Since(result.start)
	}()

	cmd := hookScriptExecCommand(scriptPath, scriptArgs)
	killer := &hookScriptProcessKiller{}
	killer.prepare(cmd)
	defer killer.close()
	cmd.Stdin = stdin

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		result.startErr = err
		code := 1
		result.exitCode = &code
		return result
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		result.startErr = err
		code := 1
		result.exitCode = &code
		return result
	}

	limit := &hookOutputLimit{maxBytes: maxOutputSize}
	done := make(chan error, 1)
	if err := cmd.Start(); err != nil {
		result.startErr = err
		code := 1
		result.exitCode = &code
		return result
	}
	killer.attach(cmd)

	go func() {
		var copyWG sync.WaitGroup
		copyWG.Add(2)
		go func() {
			defer copyWG.Done()
			_, _ = io.Copy(&hookLimitedWriter{limit: limit, out: stdout}, stdoutPipe)
		}()
		go func() {
			defer copyWG.Done()
			_, _ = io.Copy(&hookLimitedWriter{limit: limit, out: stderr}, stderrPipe)
		}()
		copyWG.Wait()
		done <- cmd.Wait()
	}()

	var waitErr error
	if timeout > 0 {
		timer := time.NewTimer(timeout)
		select {
		case waitErr = <-done:
			timer.Stop()
		case <-timer.C:
			result.timedOut = true
			killer.kill(cmd)
			waitErr = <-done
		}
	} else {
		waitErr = <-done
	}
	result.truncated = limit.truncated

	if result.timedOut {
		code := 124
		result.exitCode = &code
		return result
	}
	if waitErr == nil {
		code := 0
		result.exitCode = &code
		return result
	}
	if exitErr, ok := errors.AsType[*exec.ExitError](waitErr); ok {
		code := exitErr.ExitCode()
		result.exitCode = &code
		return result
	}
	// the script started but wait failed for an unexpected reason, keep the failure visible
	result.startErr = waitErr
	code := 1
	result.exitCode = &code
	return result
}

func recordHookScriptRun(ctx context.Context, ownerName, repoName string, isWiki bool, phase, hookName string, started time.Time, duration time.Duration, exitCode *int, timedOut, truncated, skipped bool, maxOutputSize int64) {
	record := private.HookScriptRunRecord{
		IsWiki:      isWiki,
		Phase:       phase,
		HookName:    hookName,
		StartedUnix: started.Unix(),
		DurationMs:  duration.Milliseconds(),
		ExitCode:    exitCode,
		IsTimeout:   timedOut,
		IsTruncated: truncated,
		IsSkipped:   skipped,
	}
	requestCtx, cancel := context.WithTimeout(ctx, hookScriptAPITimeout)
	defer cancel()
	if extra := private.HookRecordScriptRun(requestCtx, ownerName, repoName, record); extra.HasError() {
		log.Error("recordHookScriptRun: %v", extra.Error)
	}
	if truncated {
		fmt.Fprintf(os.Stderr, "\nGitea: output of hook script %q exceeded %d bytes, remaining output was truncated\n", hookName, maxOutputSize)
	}
}
