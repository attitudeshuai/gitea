// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package private

import (
	"context"
	"fmt"
	"net/url"

	"gitea.dev/modules/httplib"
	"gitea.dev/modules/setting"
)

// HookScriptSettingsResponse contains the effective execution limits for custom hook scripts of a repository
type HookScriptSettingsResponse struct {
	ScriptTimeout int   `json:"script_timeout"`
	TotalTimeout  int   `json:"total_timeout"`
	MaxOutputSize int64 `json:"max_output_size"`
}

// HookScriptRunRecord is one custom hook script execution reported by the "hook run-script" command
type HookScriptRunRecord struct {
	IsWiki      bool   `json:"is_wiki"`
	Phase       string `json:"phase"`
	HookName    string `json:"hook_name"`
	StartedUnix int64  `json:"started_unix"`
	DurationMs  int64  `json:"duration_ms"`
	ExitCode    *int   `json:"exit_code"` // nil when the script was skipped and never executed
	IsTimeout   bool   `json:"is_timeout"`
	IsTruncated bool   `json:"is_truncated"`
	IsSkipped   bool   `json:"is_skipped"`
}

func newHookScriptRequest(ctx context.Context, kind, ownerName, repoName string, body ...any) *httplib.ClientRequest {
	reqURL := setting.LocalURL + fmt.Sprintf("api/internal/hook/%s/%s/%s", kind, url.PathEscape(ownerName), url.PathEscape(repoName))
	return newInternalRequestAPI(ctx, reqURL, "POST", body...)
}

// HookGetScriptSettings returns the effective custom hook script limits for the repository
func HookGetScriptSettings(ctx context.Context, ownerName, repoName string) (*HookScriptSettingsResponse, ResponseExtra) {
	req := newHookScriptRequest(ctx, "script-settings", ownerName, repoName)
	return requestJSONResp(req, &HookScriptSettingsResponse{})
}

// HookRecordScriptRun stores one custom hook script execution record
func HookRecordScriptRun(ctx context.Context, ownerName, repoName string, record HookScriptRunRecord) ResponseExtra {
	req := newHookScriptRequest(ctx, "script-run", ownerName, repoName, record)
	return requestJSONClientMsg(req, "success")
}
