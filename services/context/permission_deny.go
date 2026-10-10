// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package context

import (
	"strings"

	access_model "gitea.dev/models/perm/access"
	repo_model "gitea.dev/models/repo"
	"gitea.dev/models/unit"
	user_model "gitea.dev/models/user"
	"gitea.dev/modules/setting"
	audit_service "gitea.dev/services/audit"
)

const (
	DenyReasonHeader = "X-Gitea-Deny-Reason"
	DenySourceHeader = "X-Gitea-Deny-Source"
)

// PermissionDenyEnabled reports whether denied requests carry reason headers and audit events.
func PermissionDenyEnabled() bool {
	return setting.PermissionDenyEnabled()
}

// DenyPermission attaches the reason headers for d and records the audit event according to
// configuration. It never changes the response the calling middleware is about to write.
func DenyPermission(ctx *Base, doer *user_model.User, repo *repo_model.Repository, d *access_model.Decision) {
	ctx.RespHeader().Set(DenyReasonHeader, string(d.DenyReason))
	ctx.RespHeader().Set(DenySourceHeader, string(d.Source))
	audit_service.RecordPermissionDeny(ctx, repo, doer, d)
}

// ExplainUnitDecision explains doer's permission on unit, running the same read-only pipeline
// as a real request. Errors fall back to the no-access decision, matching the fail-closed response.
func ExplainUnitDecision(ctx *Base, repo *repo_model.Repository, doer *user_model.User, tp unit.Type) *access_model.Decision {
	d, err := access_model.ExplainDoerRepoPermission(ctx, repo, doer, tp)
	if err != nil {
		return &access_model.Decision{
			AccessMode: 0,
			Source:     access_model.DecisionSourceRepoUnitPublic,
			DenyReason: access_model.DenyReasonRepoUnitRestricted,
			Unit:       tp,
		}
	}
	return d
}

// InsufficientModeDecision converts an Explain result that allows below the required mode into
// a deny decision, keeping the effective source and mapping it to the closest deny reason.
func InsufficientModeDecision(d *access_model.Decision) *access_model.Decision {
	if !d.Allowed {
		return d
	}
	switch d.Source {
	case access_model.DecisionSourceTeamUnitMode, access_model.DecisionSourceTeamMember:
		d.DenyReason = access_model.DenyReasonTeamUnitNotGranted
		d.Source = access_model.DecisionSourceTeamUnitMode
	case access_model.DecisionSourceCollaborator:
		d.DenyReason = access_model.DenyReasonRepoUnitRestricted
		d.Source = access_model.DecisionSourceRepoUnitPublic
	default:
		d.DenyReason = access_model.DenyReasonRepoUnitRestricted
		d.Source = access_model.DecisionSourceRepoUnitPublic
	}
	d.Allowed = false
	return d
}

// UnitFromRequestPath derives the repository unit the request targets. Paths without a unit
// segment (git data and API-style calls) fall back to code, since a totally denied caller is
// rejected identically for every unit.
func UnitFromRequestPath(ctx *Base, repoLink string) unit.Type {
	p := strings.TrimPrefix(ctx.Req.URL.EscapedPath(), setting.AppSubURL+repoLink)
	segments := strings.Split(strings.Trim(p, "/"), "/")
	seg := ""
	if len(segments) > 0 {
		seg = segments[0]
		if seg == "-" && len(segments) > 1 {
			seg = segments[1]
		}
	}
	switch seg {
	case "issues":
		return unit.TypeIssues
	case "pulls":
		return unit.TypePullRequests
	case "releases":
		return unit.TypeReleases
	case "wiki":
		return unit.TypeWiki
	case "projects":
		return unit.TypeProjects
	case "packages":
		return unit.TypePackages
	case "actions":
		return unit.TypeActions
	default:
		return unit.TypeCode
	}
}
