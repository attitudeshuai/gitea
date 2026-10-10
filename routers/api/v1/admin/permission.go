// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package admin

import (
	"net/http"

	audit_model "gitea.dev/models/audit"
	api "gitea.dev/modules/structs"
	"gitea.dev/services/context"
)

// ListPermissionDenyReasons returns denial counts per reason across every repository.
//
// swagger:operation GET /admin/permission/deny-reasons admin adminListPermissionDenyReasons
// ---
// summary: Count all permission denials by reason
// produces:
// - application/json
// parameters:
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
	opts := &audit_model.PermissionDenyCountOptions{
		Since: ctx.FormInt64("since"),
		Until: ctx.FormInt64("until"),
		Unit:  ctx.FormString("unit"),
	}

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
