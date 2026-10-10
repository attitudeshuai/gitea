// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package admin

import (
	"errors"
	"net/http"
	"time"

	"gitea.dev/models/webhook"
	"gitea.dev/modules/optional"
	api "gitea.dev/modules/structs"
	"gitea.dev/modules/timeutil"
	"gitea.dev/modules/util"
	"gitea.dev/modules/web"
	webhook_module "gitea.dev/modules/webhook"
	"gitea.dev/routers/api/v1/utils"
	"gitea.dev/services/context"
	webhook_service "gitea.dev/services/webhook"
)

// ListHookTasks lists recent delivery tasks of a webhook, optionally filtered
// by delivery status, event type and delivery time range.
func ListHookTasks(ctx *context.APIContext) {
	// swagger:operation GET /admin/hooks/{id}/tasks admin adminListHookTasks
	// ---
	// summary: List a webhook's delivery tasks
	// produces:
	// - application/json
	// parameters:
	// - name: id
	//   in: path
	//   description: id of the hook
	//   type: integer
	//   format: int64
	//   required: true
	// - name: status
	//   in: query
	//   description: filter by delivery status
	//   type: string
	//   enum: [pending, succeed, failed, waiting, dead, sending]
	// - name: event_type
	//   in: query
	//   description: filter by event type, e.g. push
	//   type: string
	// - name: since
	//   in: query
	//   description: only tasks delivered at or after this time, ISO 8601 (RFC 3339)
	//   type: string
	//   format: date-time
	// - name: until
	//   in: query
	//   description: only tasks delivered at or before this time, ISO 8601 (RFC 3339)
	//   type: string
	//   format: date-time
	// - name: page
	//   in: query
	//   description: page number of results to return (1-based)
	//   type: integer
	// - name: limit
	//   in: query
	//   description: page size of results
	//   type: integer
	// responses:
	//   "200":
	//     "$ref": "#/responses/HookTaskList"
	//   "404":
	//     "$ref": "#/responses/notFound"
	//   "422":
	//     "$ref": "#/responses/validationError"

	hookID := ctx.PathParamInt64("id")
	if _, err := webhook.GetWebhookByID(ctx, hookID); err != nil {
		if errors.Is(err, util.ErrNotExist) {
			ctx.APIErrorNotFound()
		} else {
			ctx.APIErrorInternal(err)
		}
		return
	}

	listOptions := utils.GetListOptions(ctx)
	opts := webhook.FindHookTasksOptions{
		ListOptions: listOptions,
		HookID:      hookID,
	}
	if statusName := ctx.FormString("status"); statusName != "" {
		status, ok := webhook_service.ParseHookTaskStatusName(statusName)
		if !ok {
			ctx.APIError(http.StatusUnprocessableEntity, "invalid 'status' value: "+statusName)
			return
		}
		opts.Status = optional.Some(status)
	}
	if eventType := ctx.FormString("event_type"); eventType != "" {
		opts.EventType = optional.Some(webhook_module.HookEventType(eventType))
	}
	since, ok := parseHookTaskTime(ctx, "since", ctx.FormString("since"))
	if !ok {
		return
	}
	until, ok := parseHookTaskTime(ctx, "until", ctx.FormString("until"))
	if !ok {
		return
	}
	opts.Since = since
	opts.Until = until

	tasks, total, err := webhook.FindHookTasks(ctx, opts)
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}

	dtos := make([]*api.HookTask, len(tasks))
	for i := range tasks {
		dtos[i] = webhook_service.ToHookTask(tasks[i])
	}
	ctx.SetLinkHeader(total, listOptions.PageSize)
	ctx.SetTotalCountHeader(total)
	ctx.JSON(http.StatusOK, dtos)
}

// RedeliverHookTasks requeues matching dead-letter tasks of a webhook for
// another delivery attempt with the same payload and signature.
func RedeliverHookTasks(ctx *context.APIContext) {
	// swagger:operation POST /admin/hooks/{id}/tasks/redeliver admin adminRedeliverHookTasks
	// ---
	// summary: Batch-redeliver a webhook's dead-letter tasks
	// consumes:
	// - application/json
	// produces:
	// - application/json
	// parameters:
	// - name: id
	//   in: path
	//   description: id of the hook
	//   type: integer
	//   format: int64
	//   required: true
	// - name: options
	//   in: body
	//   description: filters selecting the dead-letter tasks
	//   required: false
	//   schema:
	//     "$ref": "#/definitions/RedeliverHookTasksOption"
	// responses:
	//   "200":
	//     "$ref": "#/responses/HookTaskRedeliverResult"
	//   "404":
	//     "$ref": "#/responses/notFound"
	//   "422":
	//     "$ref": "#/responses/validationError"

	hookID := ctx.PathParamInt64("id")
	form := web.GetForm[*api.RedeliverHookTasksOption](ctx)

	var eventType optional.Option[webhook_module.HookEventType]
	if form != nil && form.EventType != "" {
		eventType = optional.Some(webhook_module.HookEventType(form.EventType))
	}
	var since, until timeutil.TimeStampNano
	if form != nil && form.Since != nil {
		since = timeutil.TimeStampNano(form.Since.UnixNano())
	}
	if form != nil && form.Until != nil {
		until = timeutil.TimeStampNano(form.Until.UnixNano())
	}

	redelivered, err := webhook_service.RequeueDeadTasks(ctx, hookID, eventType, since, until)
	if err != nil {
		if errors.Is(err, util.ErrNotExist) {
			ctx.APIErrorNotFound()
		} else {
			ctx.APIErrorInternal(err)
		}
		return
	}
	ctx.JSON(http.StatusOK, &api.RedeliverHookTasksResponse{Redelivered: redelivered})
}

func parseHookTaskTime(ctx *context.APIContext, name, raw string) (timeutil.TimeStampNano, bool) {
	if raw == "" {
		return 0, true
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		ctx.APIError(http.StatusUnprocessableEntity, "invalid '"+name+"' format, expected ISO 8601 (RFC 3339)")
		return 0, false
	}
	return timeutil.TimeStampNano(t.UnixNano()), true
}
