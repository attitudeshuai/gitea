// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package integration

import (
	"bytes"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gitea.dev/models/db"
	git_model "gitea.dev/models/git"
	repo_model "gitea.dev/models/repo"
	"gitea.dev/models/unittest"
	user_model "gitea.dev/models/user"
	"gitea.dev/modules/git"
	"gitea.dev/modules/setting"
	"gitea.dev/modules/test"
	repo_service "gitea.dev/services/repository"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const hookGuardrailsScriptName = "guard-hook"

func TestGitHookGuardrails(t *testing.T) {
	defer test.MockVariableValue(&setting.DisableGitHooks, false)()
	onGiteaRun(t, testGitHookGuardrails)
}

func testGitHookGuardrails(t *testing.T, u *url.URL) {
	t.Run("DefaultLimitsKeepBehavior", func(t *testing.T) {
		defer test.MockVariableValue(&setting.Git.Hooks, setting.GitHooksSettings{})()

		env := newHookGuardrailsEnv(t, u, "hook-guard-default")
		env.writeHookFile(t, "pre-receive", hookGuardrailsScriptName, "#!/bin/bash\ncat >/dev/null\necho guardrail-default\nexit 0\n")
		out, err := env.commitAndPush(t, "file1.txt", "x")
		require.NoError(t, err, out)
		assert.Contains(t, out, "guardrail-default")

		runs := env.findRuns(t, "pre-receive")
		require.Len(t, runs, 1)
		require.NotNil(t, runs[0].ExitCode)
		assert.Equal(t, 0, *runs[0].ExitCode)
		assert.False(t, runs[0].IsTimeout)
		assert.False(t, runs[0].IsTruncated)
		assert.False(t, runs[0].IsSkipped)
	})

	t.Run("PreReceiveTimeoutRejectsPush", func(t *testing.T) {
		defer test.MockVariableValue(&setting.Git.Hooks, setting.GitHooksSettings{ScriptTimeout: 1})()

		env := newHookGuardrailsEnv(t, u, "hook-guard-pre-timeout")
		env.writeHookFile(t, "pre-receive", hookGuardrailsScriptName, "#!/bin/bash\nsleep 30\n")
		out, err := env.commitAndPush(t, "file1.txt", "x")
		require.Error(t, err)
		assert.Contains(t, out, fmt.Sprintf(`hook script %q timed out after 1 second`, hookGuardrailsScriptName))

		runs := env.findRuns(t, "pre-receive")
		require.Len(t, runs, 1)
		assert.True(t, runs[0].IsTimeout)
		require.NotNil(t, runs[0].ExitCode)
		assert.Equal(t, 124, *runs[0].ExitCode)
	})

	t.Run("UpdateTimeoutRejectsPush", func(t *testing.T) {
		defer test.MockVariableValue(&setting.Git.Hooks, setting.GitHooksSettings{ScriptTimeout: 1})()

		env := newHookGuardrailsEnv(t, u, "hook-guard-update-timeout")
		env.writeHookFile(t, "update", hookGuardrailsScriptName, "#!/bin/bash\nsleep 30\n")
		out, err := env.runGitPushCapture(t, "origin", "master:update-timeout")
		require.Error(t, err)
		assert.Contains(t, out, fmt.Sprintf(`hook script %q timed out after 1 second`, hookGuardrailsScriptName))
		assert.Len(t, env.findRuns(t, "update"), 1)
	})

	t.Run("PostReceiveTimeoutKeepsRefUpdate", func(t *testing.T) {
		defer test.MockVariableValue(&setting.Git.Hooks, setting.GitHooksSettings{ScriptTimeout: 1})()

		env := newHookGuardrailsEnv(t, u, "hook-guard-post-timeout")
		env.writeHookFile(t, "post-receive", hookGuardrailsScriptName, "#!/bin/bash\nsleep 30\n")
		_, _ = env.commitAndPush(t, "file1.txt", "x")

		runs := env.findRuns(t, "post-receive")
		require.Len(t, runs, 1)
		assert.True(t, runs[0].IsTimeout)

		// the reference update is already committed and visible to Gitea despite the timed out post hook
		exist, err := git_model.IsBranchExist(t.Context(), env.repo.ID, "master")
		require.NoError(t, err)
		assert.True(t, exist)
	})

	t.Run("OutputTruncated", func(t *testing.T) {
		defer test.MockVariableValue(&setting.Git.Hooks, setting.GitHooksSettings{MaxOutputSize: 1024})()

		env := newHookGuardrailsEnv(t, u, "hook-guard-truncate")
		env.writeHookFile(t, "pre-receive", hookGuardrailsScriptName, "#!/bin/bash\ni=0\nwhile [ $i -lt 10000 ]; do echo \"0123456789abcdefghij\"; i=$((i+1)); done\nexit 0\n")
		out, err := env.commitAndPush(t, "file1.txt", "x")
		require.NoError(t, err, out)
		assert.Contains(t, out, "exceeded 1024 bytes")
		// each script line is 21 bytes ("0123456789abcdefghij\n"), so at most 48 lines can be forwarded
		assert.LessOrEqual(t, strings.Count(out, "0123456789abcdefghij"), 48)

		runs := env.findRuns(t, "pre-receive")
		require.Len(t, runs, 1)
		assert.True(t, runs[0].IsTruncated)
		require.NotNil(t, runs[0].ExitCode)
		assert.Equal(t, 0, *runs[0].ExitCode)
	})

	t.Run("CumulativeBudgetSkipsRemainingScripts", func(t *testing.T) {
		defer test.MockVariableValue(&setting.Git.Hooks, setting.GitHooksSettings{TotalTimeout: 3})()

		env := newHookGuardrailsEnv(t, u, "hook-guard-total")
		env.writeHookFile(t, "pre-receive", "a-quick", "#!/bin/bash\nexit 7\n")
		env.writeHookFile(t, "pre-receive", "b-slow", "#!/bin/bash\nsleep 30\n")
		env.writeHookFile(t, "pre-receive", "c-slow", "#!/bin/bash\nsleep 30\n")
		out, err := env.commitAndPush(t, "file1.txt", "x")
		require.Error(t, err)
		assert.Contains(t, out, `hook script "b-slow" timed out`)

		runs := env.findRuns(t, "pre-receive")
		byName := make(map[string]*git_model.HookRun, len(runs))
		for _, run := range runs {
			byName[run.HookName] = run
		}
		require.Contains(t, byName, "a-quick")
		require.Contains(t, byName, "b-slow")
		require.Contains(t, byName, "c-slow")
		require.NotNil(t, byName["a-quick"].ExitCode)
		assert.Equal(t, 7, *byName["a-quick"].ExitCode)
		assert.True(t, byName["b-slow"].IsTimeout)
		assert.True(t, byName["c-slow"].IsSkipped)
		assert.Nil(t, byName["c-slow"].ExitCode)
	})

	t.Run("RepoOverrideAndSettingsPage", func(t *testing.T) {
		env := newHookGuardrailsEnv(t, u, "hook-guard-override")
		env.user.AllowGitHook = true
		require.NoError(t, user_model.UpdateUserCols(t.Context(), env.user, "allow_git_hook"))
		session := loginUser(t, env.user.LowerName)
		settingsURL := fmt.Sprintf("/%s/settings/hooks/git", env.repo.FullName())

		// inherited state is shown on the settings page
		resp := session.MakeRequest(t, NewRequest(t, "GET", settingsURL), http.StatusOK)
		body := resp.Body.String()
		assert.Contains(t, body, "Hook Script Execution Limits")
		assert.Contains(t, body, "Inherited")

		// save a per-repository override of 1 second
		session.MakeRequest(t, NewRequestWithValues(t, "POST", settingsURL+"/settings", map[string]string{
			"hook_script_timeout":  "1",
			"hook_total_timeout":   "",
			"hook_max_output_size": "",
		}), http.StatusSeeOther)

		repoFromDB := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: env.repo.ID})
		require.NotNil(t, repoFromDB.HookScriptTimeout)
		assert.Equal(t, 1, *repoFromDB.HookScriptTimeout)
		resp = session.MakeRequest(t, NewRequest(t, "GET", settingsURL), http.StatusOK)
		assert.Contains(t, resp.Body.String(), "Repository override")

		// the override applies to the very next push without regenerating the wrapper scripts
		env.writeHookFile(t, "pre-receive", hookGuardrailsScriptName, "#!/bin/bash\nsleep 30\n")
		out, err := env.commitAndPush(t, "file1.txt", "x")
		require.Error(t, err)
		assert.Contains(t, out, fmt.Sprintf(`hook script %q timed out after 1 second`, hookGuardrailsScriptName))

		// an override above the global cap is rejected and not persisted
		defer test.MockVariableValue(&setting.Git.Hooks, setting.GitHooksSettings{ScriptTimeout: 5})()
		session.MakeRequest(t, NewRequestWithValues(t, "POST", settingsURL+"/settings", map[string]string{
			"hook_script_timeout": "999",
		}), http.StatusSeeOther)
		flash := session.GetCookieFlashMessage()
		require.NotNil(t, flash)
		assert.Contains(t, flash.ErrorMsg, "SCRIPT_TIMEOUT")
		repoFromDB = unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: env.repo.ID})
		require.NotNil(t, repoFromDB.HookScriptTimeout)
		assert.Equal(t, 1, *repoFromDB.HookScriptTimeout)

		// the records list is reachable and can be filtered by hook script name
		session.MakeRequest(t, NewRequest(t, "GET", settingsURL+"?hook_name="+hookGuardrailsScriptName), http.StatusOK)
	})
}

type hookGuardrailsEnv struct {
	repo   *repo_model.Repository
	user   *user_model.User
	gitDir string
}

func newHookGuardrailsEnv(t *testing.T, u *url.URL, repoName string) *hookGuardrailsEnv {
	t.Helper()
	user := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	repo, err := repo_service.CreateRepository(t.Context(), user, user, repo_service.CreateRepoOptions{
		Name:          repoName,
		Description:   "hook guardrails test",
		AutoInit:      false,
		DefaultBranch: "master",
		IsPrivate:     false,
	})
	require.NoError(t, err)
	require.NotEmpty(t, repo)

	gitDir := t.TempDir()
	doGitInitTestRepository(gitDir)(t)

	remoteURL := *u
	remoteURL.Path = repo.FullName() + ".git"
	remoteURL.User = url.UserPassword(user.LowerName, userPassword)
	doGitAddRemote(gitDir, "origin", &remoteURL)(t)

	env := &hookGuardrailsEnv{repo: repo, user: user, gitDir: gitDir}
	out, err := env.runGitPushCapture(t, "origin", "master")
	require.NoError(t, err, out)
	return env
}

func (env *hookGuardrailsEnv) writeHookFile(t *testing.T, phase, name, content string) {
	t.Helper()
	dir := filepath.Join(setting.RepoRootPath, env.repo.OwnerName, env.repo.Name+".git", "hooks", phase+".d")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o755))
}

func (env *hookGuardrailsEnv) findRuns(t *testing.T, phase string) []*git_model.HookRun {
	t.Helper()
	runs, _, err := git_model.FindHookRuns(t.Context(), git_model.FindHookRunsOptions{
		ListOptions: db.ListOptionsAll,
		RepoID:      env.repo.ID,
		Phase:       phase,
	})
	require.NoError(t, err)
	return runs
}

func (env *hookGuardrailsEnv) commitAndPush(t *testing.T, file, content string) (string, error) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(env.gitDir, file), []byte(content), 0o644))
	require.NoError(t, gitAddChangesDeprecated(t.Context(), env.gitDir, true))
	signature := git.Signature{Email: "test@test.test", Name: "test"}
	require.NoError(t, gitCommitChangesDeprecated(t.Context(), env.gitDir, gitCommitChangesOptions{
		Committer: &signature,
		Author:    &signature,
		Message:   "commit " + file,
	}))
	return env.runGitPushCapture(t, "origin", "master")
}

func (env *hookGuardrailsEnv) runGitPushCapture(t *testing.T, args ...string) (string, error) {
	t.Helper()
	buf := &bytes.Buffer{}
	cmd := exec.Command("git", append([]string{"push"}, args...)...)
	cmd.Dir = env.gitDir
	cmd.Stdout = buf
	cmd.Stderr = buf
	err := cmd.Run()
	return buf.String(), err
}
