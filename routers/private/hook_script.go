// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package private

import (
	"net/http"

	git_model "gitea.dev/models/git"
	"gitea.dev/modules/private"
	"gitea.dev/modules/timeutil"
	"gitea.dev/modules/web"
	gitea_context "gitea.dev/services/context"
	repo_service "gitea.dev/services/repository"
)

// HookScriptSettings returns the effective custom hook script limits of the repository
func HookScriptSettings(ctx *gitea_context.PrivateContext) {
	limits := repo_service.EffectiveHookScriptSettings(ctx.Repo.Repository)
	ctx.JSON(http.StatusOK, private.HookScriptSettingsResponse{
		ScriptTimeout: limits.ScriptTimeout,
		TotalTimeout:  limits.TotalTimeout,
		MaxOutputSize: limits.MaxOutputSize,
	})
}

// HookScriptRun stores one custom hook script execution record
func HookScriptRun(ctx *gitea_context.PrivateContext) {
	record := web.GetForm[*private.HookScriptRunRecord](ctx)
	if ctx.Written() {
		return
	}

	run := &git_model.HookRun{
		RepoID:      ctx.Repo.Repository.ID,
		IsWiki:      record.IsWiki,
		Phase:       record.Phase,
		HookName:    record.HookName,
		StartedUnix: timeutil.TimeStamp(record.StartedUnix),
		DurationMs:  record.DurationMs,
		ExitCode:    record.ExitCode,
		IsTimeout:   record.IsTimeout,
		IsTruncated: record.IsTruncated,
		IsSkipped:   record.IsSkipped,
	}
	if err := git_model.InsertHookRun(ctx, run); err != nil {
		ctx.PrivateInternalErrorf("Unable to insert hook script run record: %v", err)
		return
	}
	ctx.PlainText(http.StatusOK, "success")
}
