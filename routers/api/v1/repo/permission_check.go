// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package repo

import (
	"net/http"
	"strings"

	access_model "gitea.dev/models/perm/access"
	"gitea.dev/models/unit"
	user_model "gitea.dev/models/user"
	api "gitea.dev/modules/structs"
	"gitea.dev/services/context"
)

// CheckPermission returns the read-only explained permission for one repo unit.
//
// swagger:operation GET /repos/{owner}/{repo}/permission/check repository repoCheckPermission
// ---
// summary: Check repository permission for a user on a unit
// produces:
// - application/json
// parameters:
//   - name: owner
//     in: path
//     description: owner of the repo
//     type: string
//     required: true
//   - name: repo
//     in: path
//     description: name of the repo
//     type: string
//     required: true
//   - name: unit
//     in: query
//     description: machine name of the unit
//     type: string
//     required: true
//   - name: username
//     in: query
//     description: user to check, defaults to the caller (anonymous when not signed in)
//     type: string
//
// responses:
//
//	"200":
//	  "$ref": "#/responses/RepoPermissionDecision"
//	"400":
//	  "$ref": "#/responses/error"
//	"403":
//	  "$ref": "#/responses/forbidden"
//	"404":
//	  "$ref": "#/responses/notFound"
func CheckPermission(ctx *context.APIContext) {
	tp := unit.TypeFromName(ctx.FormString("unit"))
	if tp == unit.TypeInvalid {
		ctx.APIError(http.StatusBadRequest, "invalid unit type")
		return
	}

	var subject *user_model.User
	username := ctx.FormString("username")
	if username != "" {
		isSelf := ctx.IsSigned && strings.EqualFold(ctx.Doer.LowerName, username)
		if !isSelf && !ctx.IsUserSiteAdmin() && !ctx.IsUserRepoAdmin() {
			ctx.APIError(http.StatusForbidden, "only site admins and repository admins can query another user's permission")
			return
		}

		var err error
		subject, err = user_model.GetUserByName(ctx, username)
		if err != nil {
			if user_model.IsErrUserNotExist(err) {
				ctx.APIError(http.StatusNotFound, err.Error())
			} else {
				ctx.APIErrorInternal(err)
			}
			return
		}
	} else {
		subject = ctx.Doer // nil when anonymous
	}

	d, err := access_model.ExplainDoerRepoPermission(ctx, ctx.Repo.Repository, subject, tp)
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}

	ctx.JSON(http.StatusOK, api.RepoPermissionDecision{
		Allowed:    d.Allowed,
		Mode:       api.AccessLevelName(d.AccessMode.ToString()),
		Source:     string(d.Source),
		DenyReason: string(d.DenyReason),
		Unit:       tp.Name(),
	})
}
