// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package repository

import (
	"fmt"

	repo_model "gitea.dev/models/repo"
	"gitea.dev/modules/setting"
)

// HookScriptSettings is the effective execution limits for a repository's custom Git hook scripts
type HookScriptSettings struct {
	ScriptTimeout int
	TotalTimeout  int
	MaxOutputSize int64
}

// EffectiveHookScriptSettings returns the effective limits, repository overrides take precedence over the global ones
func EffectiveHookScriptSettings(repo *repo_model.Repository) HookScriptSettings {
	limits := HookScriptSettings{
		ScriptTimeout: setting.Git.Hooks.ScriptTimeout,
		TotalTimeout:  setting.Git.Hooks.TotalTimeout,
		MaxOutputSize: setting.Git.Hooks.MaxOutputSize,
	}
	if repo.HookScriptTimeout != nil {
		limits.ScriptTimeout = *repo.HookScriptTimeout
	}
	if repo.HookTotalTimeout != nil {
		limits.TotalTimeout = *repo.HookTotalTimeout
	}
	if repo.HookMaxOutputSize != nil {
		limits.MaxOutputSize = *repo.HookMaxOutputSize
	}
	return limits
}

// ValidateHookScriptOverrides validates repository overrides against the global limits.
// nil means inheriting the global value. Zero means unlimited and is only accepted while the
// global limit is unlimited (zero) as well, otherwise the override would exceed the global cap.
func ValidateHookScriptOverrides(scriptTimeout, totalTimeout *int, maxOutputSize *int64) error {
	if err := validateHookScriptIntOverride("SCRIPT_TIMEOUT", scriptTimeout, setting.Git.Hooks.ScriptTimeout); err != nil {
		return err
	}
	if err := validateHookScriptIntOverride("TOTAL_TIMEOUT", totalTimeout, setting.Git.Hooks.TotalTimeout); err != nil {
		return err
	}
	if maxOutputSize == nil {
		return nil
	}
	if *maxOutputSize < 0 {
		return fmt.Errorf("ValidateHookScriptOverrides: MAX_OUTPUT_SIZE must not be negative: %d", *maxOutputSize)
	}
	if global := setting.Git.Hooks.MaxOutputSize; global > 0 && (*maxOutputSize == 0 || *maxOutputSize > global) {
		return fmt.Errorf("ValidateHookScriptOverrides: MAX_OUTPUT_SIZE must be between 1 and %d when the global limit is set, got %d", global, *maxOutputSize)
	}
	return nil
}

func validateHookScriptIntOverride(name string, value *int, global int) error {
	if value == nil {
		return nil
	}
	if *value < 0 {
		return fmt.Errorf("ValidateHookScriptOverrides: %s must not be negative: %d", name, *value)
	}
	if global > 0 && (*value == 0 || *value > global) {
		return fmt.Errorf("ValidateHookScriptOverrides: %s must be between 1 and %d when the global limit is set, got %d", name, global, *value)
	}
	return nil
}
