// Copyright 2023 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package setting

import (
	"net/http"
	"strconv"
	"strings"

	"gitea.dev/models/db"
	git_model "gitea.dev/models/git"
	repo_model "gitea.dev/models/repo"
	"gitea.dev/modules/git"
	"gitea.dev/modules/setting"
	"gitea.dev/routers/web/repo"
	"gitea.dev/services/context"
	repo_service "gitea.dev/services/repository"
)

const hookRunPageSize = 20

type hookScriptLimitView struct {
	Field       string
	Label       string
	Effective   int64
	Override    string
	Global      int64
	IsCustom    bool
	IsUnlimited bool
}

func buildHookScriptLimitViews(ctx *context.Context, repo *repo_model.Repository) []hookScriptLimitView {
	effective := repo_service.EffectiveHookScriptSettings(repo)
	toString := func(value *int64) string {
		if value == nil {
			return ""
		}
		return strconv.FormatInt(*value, 10)
	}
	toStringPtrInt := func(value *int) *int64 {
		if value == nil {
			return nil
		}
		v := int64(*value)
		return &v
	}
	return []hookScriptLimitView{
		{
			Field: "hook_script_timeout", Label: string(ctx.Tr("repo.settings.hook_script_timeout")),
			Effective: int64(effective.ScriptTimeout), Override: toString(toStringPtrInt(repo.HookScriptTimeout)),
			Global: int64(setting.Git.Hooks.ScriptTimeout), IsCustom: repo.HookScriptTimeout != nil,
			IsUnlimited: effective.ScriptTimeout == 0,
		},
		{
			Field: "hook_total_timeout", Label: string(ctx.Tr("repo.settings.hook_total_timeout")),
			Effective: int64(effective.TotalTimeout), Override: toString(toStringPtrInt(repo.HookTotalTimeout)),
			Global: int64(setting.Git.Hooks.TotalTimeout), IsCustom: repo.HookTotalTimeout != nil,
			IsUnlimited: effective.TotalTimeout == 0,
		},
		{
			Field: "hook_max_output_size", Label: string(ctx.Tr("repo.settings.hook_max_output_size")),
			Effective: effective.MaxOutputSize, Override: toString(repo.HookMaxOutputSize),
			Global: setting.Git.Hooks.MaxOutputSize, IsCustom: repo.HookMaxOutputSize != nil,
			IsUnlimited: effective.MaxOutputSize == 0,
		},
	}
}

// GitHooks hooks of a repository
func GitHooks(ctx *context.Context) {
	ctx.Data["Title"] = ctx.Tr("repo.settings.githooks")
	ctx.Data["PageIsSettingsGitHooks"] = true

	hooks, err := git.ListHooks(ctx.Repo.GitRepo)
	if err != nil {
		ctx.ServerError("Hooks", err)
		return
	}
	ctx.Data["Hooks"] = hooks

	repo := ctx.Repo.Repository
	ctx.Data["HookScriptLimitViews"] = buildHookScriptLimitViews(ctx, repo)
	ctx.Data["EffectiveHookScriptSettings"] = repo_service.EffectiveHookScriptSettings(repo)
	ctx.Data["GlobalHookScriptSettings"] = setting.Git.Hooks

	hookName := strings.TrimSpace(ctx.FormString("hook_name"))
	phase := ctx.FormString("phase")
	if !git_model.IsValidHookPhase(phase) {
		phase = ""
	}
	page, _ := strconv.Atoi(ctx.FormString("page"))
	if page <= 0 {
		page = 1
	}
	runs, total, err := git_model.FindHookRuns(ctx, git_model.FindHookRunsOptions{
		ListOptions: db.ListOptions{Page: page, PageSize: hookRunPageSize},
		RepoID:      repo.ID,
		HookName:    hookName,
		Phase:       phase,
	})
	if err != nil {
		ctx.ServerError("FindHookRuns", err)
		return
	}
	ctx.Data["HookRuns"] = runs
	ctx.Data["HookRunTotal"] = total
	ctx.Data["HookRunHookName"] = hookName
	ctx.Data["HookRunPhase"] = phase
	ctx.Data["Page"] = context.NewPagerBuilder(ctx).TotalCount(total).PerPageLimit(hookRunPageSize).CurPage(page).Build()

	names, err := git_model.FindHookRunHookNames(ctx, repo.ID)
	if err != nil {
		ctx.ServerError("FindHookRunHookNames", err)
		return
	}
	ctx.Data["HookRunNames"] = names

	ctx.HTML(http.StatusOK, tplGithooks)
}

// GitHooksSettingsPost saves the repository overrides for the custom hook script execution limits
func GitHooksSettingsPost(ctx *context.Context) {
	// parseField returns nil when the field is empty (inheriting the global setting)
	parseField := func(field string) (*int64, bool) {
		raw := strings.TrimSpace(ctx.FormString(field))
		if raw == "" {
			return nil, true
		}
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return nil, false
		}
		return &value, true
	}

	scriptTimeout64, ok := parseField("hook_script_timeout")
	if !ok {
		gitHooksSettingsFlashError(ctx, string(ctx.Tr("repo.settings.hook_script_limits_invalid_number", "SCRIPT_TIMEOUT")))
		return
	}
	totalTimeout64, ok := parseField("hook_total_timeout")
	if !ok {
		gitHooksSettingsFlashError(ctx, string(ctx.Tr("repo.settings.hook_script_limits_invalid_number", "TOTAL_TIMEOUT")))
		return
	}
	maxOutputSize, ok := parseField("hook_max_output_size")
	if !ok {
		gitHooksSettingsFlashError(ctx, string(ctx.Tr("repo.settings.hook_script_limits_invalid_number", "MAX_OUTPUT_SIZE")))
		return
	}

	var scriptTimeout, totalTimeout *int
	if scriptTimeout64 != nil {
		value := int(*scriptTimeout64)
		scriptTimeout = &value
	}
	if totalTimeout64 != nil {
		value := int(*totalTimeout64)
		totalTimeout = &value
	}

	if err := repo_service.ValidateHookScriptOverrides(scriptTimeout, totalTimeout, maxOutputSize); err != nil {
		gitHooksSettingsFlashError(ctx, err.Error())
		return
	}

	repo := ctx.Repo.Repository
	repo.HookScriptTimeout = scriptTimeout
	repo.HookTotalTimeout = totalTimeout
	repo.HookMaxOutputSize = maxOutputSize
	if err := repo_model.UpdateRepositoryColsNoAutoTime(ctx, repo, "hook_script_timeout", "hook_total_timeout", "hook_max_output_size"); err != nil {
		ctx.ServerError("UpdateRepositoryColsNoAutoTime", err)
		return
	}

	ctx.Flash.Success(ctx.Tr("repo.settings.update_settings_success"))
	ctx.Redirect(ctx.Repo.RepoLink + "/settings/hooks/git")
}

func gitHooksSettingsFlashError(ctx *context.Context, message string) {
	ctx.Flash.Error(message)
	ctx.Redirect(ctx.Repo.RepoLink + "/settings/hooks/git")
}

// GitHooksEdit render for editing a hook of repository page
func GitHooksEdit(ctx *context.Context) {
	ctx.Data["Title"] = ctx.Tr("repo.settings.githooks")
	ctx.Data["PageIsSettingsGitHooks"] = true

	name := ctx.PathParam("name")
	hook, err := git.GetHook(ctx.Repo.GitRepo, name)
	if err != nil {
		if err == git.ErrNotValidHook {
			ctx.NotFound(err)
		} else {
			ctx.ServerError("GetHook", err)
		}
		return
	}
	ctx.Data["Hook"] = hook
	ctx.Data["CodeEditorConfig"] = repo.CodeEditorConfig{Filename: name + ".sh", IndentStyle: "tab", TabWidth: 4}
	ctx.HTML(http.StatusOK, tplGithookEdit)
}

// GitHooksEditPost response for editing a git hook of a repository
func GitHooksEditPost(ctx *context.Context) {
	name := ctx.PathParam("name")
	hook, err := git.GetHook(ctx.Repo.GitRepo, name)
	if err != nil {
		if err == git.ErrNotValidHook {
			ctx.NotFound(err)
		} else {
			ctx.ServerError("GetHook", err)
		}
		return
	}
	hook.Content = ctx.FormString("content")
	if err = hook.Update(); err != nil {
		ctx.ServerError("hook.Update", err)
		return
	}
	ctx.Redirect(ctx.Repo.RepoLink + "/settings/hooks/git")
}
