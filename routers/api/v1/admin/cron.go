// Copyright 2020 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package admin

import (
	"errors"
	"net/http"

	"gitea.dev/modules/log"
	"gitea.dev/modules/setting"
	"gitea.dev/modules/structs"
	"gitea.dev/modules/util"
	"gitea.dev/routers/api/v1/utils"
	"gitea.dev/services/context"
	"gitea.dev/services/cron"
)

// ListCronTasks api for getting cron tasks
func ListCronTasks(ctx *context.APIContext) {
	// swagger:operation GET /admin/cron admin adminCronList
	// ---
	// summary: List cron tasks
	// produces:
	// - application/json
	// parameters:
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
	//     "$ref": "#/responses/CronList"
	//   "403":
	//     "$ref": "#/responses/forbidden"
	tasks := cron.ListTasks()
	count := len(tasks)

	listOpts := utils.GetListOptions(ctx)
	tasks = util.PaginateSlice(tasks, listOpts.Page, listOpts.PageSize)

	res := make([]structs.Cron, len(tasks))
	for i, task := range tasks {
		res[i] = structs.Cron{
			Name:      task.Name,
			Schedule:  task.Spec,
			Next:      task.Next,
			Prev:      task.Prev,
			ExecTimes: task.ExecTimes,
		}
		if setting.Cron.EnableExecutionHistory {
			running := task.Running
			consecutiveFailures := task.ConsecutiveFailures
			res[i].Running = &running
			res[i].ConsecutiveFailures = &consecutiveFailures
			if !task.LastSkippedTime.IsZero() {
				res[i].LastSkipped = &structs.CronSkipped{
					Time:   task.LastSkippedTime,
					Reason: task.LastSkippedReason,
				}
			}
		}
	}

	ctx.SetTotalCountHeader(int64(count))
	ctx.JSON(http.StatusOK, res)
}

// ListCronTaskRuns api for getting the recent runs of a cron task
func ListCronTaskRuns(ctx *context.APIContext) {
	// swagger:operation GET /admin/cron/{task}/runs admin adminCronTaskRunList
	// ---
	// summary: List recent runs of a cron task
	// produces:
	// - application/json
	// parameters:
	// - name: task
	//   in: path
	//   description: task to list runs for
	//   type: string
	//   required: true
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
	//     "$ref": "#/responses/CronTaskRunList"
	//   "403":
	//     "$ref": "#/responses/forbidden"
	//   "404":
	//     "$ref": "#/responses/notFound"
	if !setting.Cron.EnableExecutionHistory {
		ctx.APIErrorNotFound()
		return
	}
	task := cron.GetTask(ctx.PathParam("task"))
	if task == nil {
		ctx.APIErrorNotFound()
		return
	}

	listOpts := utils.GetListOptions(ctx)
	runs, total, err := cron.ListTaskRuns(task.Name, listOpts.Page, listOpts.PageSize)
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}

	res := make([]structs.CronTaskRun, 0, len(runs))
	for _, run := range runs {
		item := structs.CronTaskRun{
			ID:            run.ID,
			TaskName:      run.TaskName,
			TriggerType:   run.TriggerType,
			DoerID:        run.DoerID,
			DoerName:      run.DoerName,
			Status:        run.Status,
			ErrorCategory: run.ErrorCategory,
			Started:       run.StartedUnix.AsTime(),
			DurationMs:    run.DurationMs,
			Message:       run.Message,
		}
		if run.FinishedUnix > 0 {
			finished := run.FinishedUnix.AsTime()
			item.Finished = &finished
		}
		res = append(res, item)
	}

	ctx.SetTotalCountHeader(total)
	ctx.JSON(http.StatusOK, res)
}

// PostCronTask api for getting cron tasks
func PostCronTask(ctx *context.APIContext) {
	// swagger:operation POST /admin/cron/{task} admin adminCronRun
	// ---
	// summary: Run cron task
	// produces:
	// - application/json
	// parameters:
	// - name: task
	//   in: path
	//   description: task to run
	//   type: string
	//   required: true
	// responses:
	//   "204":
	//     "$ref": "#/responses/empty"
	//   "404":
	//     "$ref": "#/responses/notFound"
	//   "409":
	//     description: A previous run of the task has not finished and the overlap policy is "reject"
	task := cron.GetTask(ctx.PathParam("task"))
	if task == nil {
		ctx.APIErrorNotFound()
		return
	}
	if err := task.RunWithUser(ctx.Doer, nil); err != nil {
		if errors.Is(err, cron.ErrTaskAlreadyRunning) {
			ctx.APIError(http.StatusConflict, err.Error())
			return
		}
		ctx.APIErrorInternal(err)
		return
	}
	log.Trace("Cron Task %s started by admin(%s)", task.Name, ctx.Doer.Name)

	ctx.Status(http.StatusNoContent)
}
