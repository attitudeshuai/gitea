// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package setting

import (
	"testing"

	"gitea.dev/modules/log"
	"gitea.dev/modules/test"

	"github.com/stretchr/testify/assert"
)

func TestGitHooksConfig(t *testing.T) {
	defer test.MockVariableValue(&Git)

	// missing section means no limits (the historical behavior)
	cfg, err := NewConfigProviderFromData(``)
	assert.NoError(t, err)
	loadGitFrom(cfg)
	assert.Equal(t, GitHooksSettings{}, Git.Hooks)

	// explicit zeros are accepted
	cfg, err = NewConfigProviderFromData(`
[git.hooks]
SCRIPT_TIMEOUT = 0
TOTAL_TIMEOUT = 0
MAX_OUTPUT_SIZE = 0
`)
	assert.NoError(t, err)
	loadGitFrom(cfg)
	assert.Equal(t, GitHooksSettings{}, Git.Hooks)

	// positive values are mapped
	cfg, err = NewConfigProviderFromData(`
[git.hooks]
SCRIPT_TIMEOUT = 10
TOTAL_TIMEOUT = 30
MAX_OUTPUT_SIZE = 1048576
`)
	assert.NoError(t, err)
	loadGitFrom(cfg)
	assert.Equal(t, GitHooksSettings{ScriptTimeout: 10, TotalTimeout: 30, MaxOutputSize: 1048576}, Git.Hooks)
}

func TestGitHooksConfigInvalid(t *testing.T) {
	defer test.MockVariableValue(&Git)

	for _, iniData := range []string{
		"[git.hooks]\nSCRIPT_TIMEOUT = -1\n",
		"[git.hooks]\nTOTAL_TIMEOUT = -2\n",
		"[git.hooks]\nMAX_OUTPUT_SIZE = -1024\n",
	} {
		var exitCode int
		exitCalled := false
		defer test.MockVariableValue(&log.OsExiter, func(code int) {
			exitCode = code
			exitCalled = true
		})()

		cfg, err := NewConfigProviderFromData(iniData)
		assert.NoError(t, err)
		loadGitFrom(cfg)
		assert.True(t, exitCalled, "expected loadGitFrom to fail for %q", iniData)
		assert.Equal(t, 1, exitCode)
	}
}

func TestGitHooksSettingsValidate(t *testing.T) {
	assert.NoError(t, GitHooksSettings{}.Validate())
	assert.NoError(t, GitHooksSettings{ScriptTimeout: 1, TotalTimeout: 2, MaxOutputSize: 3}.Validate())
	assert.ErrorContains(t, GitHooksSettings{ScriptTimeout: -1}.Validate(), "SCRIPT_TIMEOUT")
	assert.ErrorContains(t, GitHooksSettings{TotalTimeout: -1}.Validate(), "TOTAL_TIMEOUT")
	assert.ErrorContains(t, GitHooksSettings{MaxOutputSize: -1}.Validate(), "MAX_OUTPUT_SIZE")
}
