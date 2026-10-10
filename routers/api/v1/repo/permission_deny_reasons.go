// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package repo

import (
	"net/http"

	audit_model "gitea.dev/models/audit"
	api "gitea.dev/modules/structs"
	"gitea.dev/services/context"
)

// ListPermissionDenyReasons returns denial counts per reason for the repository.
//
// swagger:operation GET /repos/{owner}/{repo}/permission/deny-reasons repository repoListPermissionDenyReasons
// ---
// summary: Count repository permission denials by reason
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
//   - name: since
//     in: query
//     description: unix timestamp, inclusive
//     type: integer
//   - name: until
//     in: query
//     description: unix timestamp, exclusive
//     type: integer
//   - name: unit
//     in: query
//     description: machine name of a unit to filter by
//     type: string
//
// responses:
//
//	"200":
//	  "$ref": "#/responses/PermissionDenyReasonCountList"
//	"403":
//	  "$ref": "#/responses/forbidden"
func ListPermissionDenyReasons(ctx *context.APIContext) {
	listPermissionDenyReasons(ctx, &audit_model.PermissionDenyCountOptions{RepoID: ctx.Repo.Repository.ID})
}

// shared list implementation, scoped by opts.
func listPermissionDenyReasons(ctx *context.APIContext, opts *audit_model.PermissionDenyCountOptions) {
	opts.Since = ctx.FormInt64("since")
	opts.Until = ctx.FormInt64("until")
	opts.Unit = ctx.FormString("unit")

	counts, err := audit_model.CountPermissionDenyEvents(ctx, opts)
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}

	result := make([]api.PermissionDenyReasonCount, 0, len(counts))
	for _, c := range counts {
		result = append(result, api.PermissionDenyReasonCount{Reason: c.Reason, Count: c.Count})
	}
	ctx.JSON(http.StatusOK, result)
}
