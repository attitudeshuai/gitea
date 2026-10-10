// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package git

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateDelegateHooksPermissions(t *testing.T) {
	hookDir := t.TempDir()
	existingHookDir := filepath.Join(hookDir, "post-receive.d")
	require.NoError(t, os.MkdirAll(existingHookDir, 0o777))
	require.NoError(t, os.Chmod(existingHookDir, 0o777))

	require.NoError(t, createDelegateHooks(hookDir))

	hookNames, _, _ := getHookTemplates()
	for _, hookName := range hookNames {
		for _, path := range []string{
			filepath.Join(hookDir, hookName),
			filepath.Join(hookDir, hookName+".d"),
			filepath.Join(hookDir, hookName+".d", "gitea"),
		} {
			info, err := os.Stat(path)
			require.NoError(t, err)
			require.Equal(t, os.FileMode(0o755), info.Mode().Perm(), path)
		}
	}
}

func TestDelegateHookTemplatesRunCustomScriptsViaRunner(t *testing.T) {
	hookNames, hookTpls, _ := getHookTemplates()
	for i, hookName := range hookNames {
		if hookName == "proc-receive" {
			continue
		}
		tpl := hookTpls[i]
		assert.Contains(t, tpl, "run-script", hookName)
		assert.Contains(t, tpl, `--phase="${hookname}"`, hookName)
		assert.Contains(t, tpl, "mktemp", hookName)
		assert.Contains(t, tpl, "trap", hookName)
		// the state file is created lazily, so repositories without custom scripts spawn no extra processes
		assert.Contains(t, tpl, `if [ -z "${hook_state}" ]; then`, hookName)
		// the Gitea delegate hook is still executed directly and keeps its name
		assert.Contains(t, tpl, `[ "${hook##*/}" = "gitea" ]`, hookName)
		if hookName == "update" {
			assert.Contains(t, tpl, `"${hook}" $1 $2 $3`, hookName)
			assert.Contains(t, tpl, `-- "${hook}" $1 $2 $3`, hookName)
		} else {
			assert.Contains(t, tpl, `echo "${data}" | "${hook}"`, hookName)
			assert.Contains(t, tpl, `echo "${data}" | "${gitea_bin}"`, hookName)
		}
	}
}

func TestCheckDelegateHooksOutOfDate(t *testing.T) {
	hookDir := t.TempDir()
	require.NoError(t, createDelegateHooks(hookDir))

	// freshly generated hooks are up to date
	results, err := checkDelegateHooks(hookDir)
	require.NoError(t, err)
	assert.Empty(t, results)

	// an old wrapper script is reported as out of date but does not error out
	require.NoError(t, os.WriteFile(filepath.Join(hookDir, "pre-receive"), []byte("# old wrapper template\n"), 0o755))
	results, err = checkDelegateHooks(hookDir)
	require.NoError(t, err)
	assert.Contains(t, results, "old hook file "+filepath.Join(hookDir, "pre-receive")+" is out of date")
}
