// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package repository

import (
	"testing"

	repo_model "gitea.dev/models/repo"
	"gitea.dev/modules/setting"
	"gitea.dev/modules/test"

	"github.com/stretchr/testify/assert"
)

func TestEffectiveHookScriptSettings(t *testing.T) {
	defer test.MockVariableValue(&setting.Git)
	setting.Git.Hooks = setting.GitHooksSettings{ScriptTimeout: 5, TotalTimeout: 10, MaxOutputSize: 2048}

	// all NULL means inheriting the global settings
	assert.Equal(t, HookScriptSettings{ScriptTimeout: 5, TotalTimeout: 10, MaxOutputSize: 2048},
		EffectiveHookScriptSettings(&repo_model.Repository{}))

	// each field can be overridden independently
	repo := &repo_model.Repository{
		HookScriptTimeout: new(2),
		HookMaxOutputSize: new(int64(512)),
	}
	assert.Equal(t, HookScriptSettings{ScriptTimeout: 2, TotalTimeout: 10, MaxOutputSize: 512},
		EffectiveHookScriptSettings(repo))
}

func TestValidateHookScriptOverrides(t *testing.T) {
	defer test.MockVariableValue(&setting.Git)

	// global limits disabled: nil, zero and positive values are accepted
	setting.Git.Hooks = setting.GitHooksSettings{}
	assert.NoError(t, ValidateHookScriptOverrides(nil, nil, nil))
	assert.NoError(t, ValidateHookScriptOverrides(new(0), new(0), new(int64(0))))
	assert.NoError(t, ValidateHookScriptOverrides(new(9999), new(9999), new(int64(999999))))

	// negative values are always invalid
	assert.ErrorContains(t, ValidateHookScriptOverrides(new(-1), nil, nil), "SCRIPT_TIMEOUT")
	assert.ErrorContains(t, ValidateHookScriptOverrides(nil, new(-1), nil), "TOTAL_TIMEOUT")
	assert.ErrorContains(t, ValidateHookScriptOverrides(nil, nil, new(int64(-1))), "MAX_OUTPUT_SIZE")

	// global limits set: zero and greater-than-global are rejected, the boundary is accepted
	setting.Git.Hooks = setting.GitHooksSettings{ScriptTimeout: 30, TotalTimeout: 60, MaxOutputSize: 4096}
	assert.NoError(t, ValidateHookScriptOverrides(new(1), new(60), new(int64(4096))))
	assert.ErrorContains(t, ValidateHookScriptOverrides(new(0), nil, nil), "SCRIPT_TIMEOUT")
	assert.ErrorContains(t, ValidateHookScriptOverrides(new(31), nil, nil), "SCRIPT_TIMEOUT")
	assert.ErrorContains(t, ValidateHookScriptOverrides(nil, new(0), nil), "TOTAL_TIMEOUT")
	assert.ErrorContains(t, ValidateHookScriptOverrides(nil, new(61), nil), "TOTAL_TIMEOUT")
	assert.ErrorContains(t, ValidateHookScriptOverrides(nil, nil, new(int64(0))), "MAX_OUTPUT_SIZE")
	assert.ErrorContains(t, ValidateHookScriptOverrides(nil, nil, new(int64(4097))), "MAX_OUTPUT_SIZE")
}
