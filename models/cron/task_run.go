// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package cron

import (
	"context"
	"fmt"
	"time"

	"gitea.dev/models/db"
	"gitea.dev/modules/timeutil"
)

const (
	TriggerTypeSchedule = "schedule" // triggered by the cron schedule
	TriggerTypeManual   = "manual"   // triggered by an admin
)

const (
	StatusRunning     = "running"
	StatusSuccess     = "success"
	StatusFailed      = "failed"
	StatusCancelled   = "cancelled"
	StatusSkipped     = "skipped"
	StatusTimeout     = "timeout"
	StatusInterrupted = "interrupted" // a running record left behind by a killed process
)

const (
	CategoryNone            = ""
	CategoryTaskError       = "task_error"
	CategoryPanic           = "panic"
	CategoryContextCanceled = "context_canceled"
	CategoryDeadline        = "deadline_exceeded"
	CategoryOverlapSkipped  = "overlap_skipped"
	CategoryQueueFull       = "queue_full"
	CategoryProcessKilled   = "process_killed"
)

// TaskRun is one execution (or attempted execution) record of an instance-level cron task.
type TaskRun struct {
	ID            int64  `xorm:"pk autoincr"`
	TaskName      string `xorm:"INDEX NOT NULL"`
	TriggerType   string
	DoerID        int64 // -1 for scheduled runs
	DoerName      string
	Status        string `xorm:"INDEX"`
	ErrorCategory string
	StartedUnix   timeutil.TimeStamp
	FinishedUnix  timeutil.TimeStamp // 0 while the run has not finished
	DurationMs    int64
	Message       string `xorm:"TEXT"`
}

func init() {
	db.RegisterModel(new(TaskRun))
}

// TableName keeps the table independent of the struct name, matching the v358 migration.
func (TaskRun) TableName() string {
	return "cron_task_run"
}

func InsertTaskRun(ctx context.Context, run *TaskRun) error {
	return db.Insert(ctx, run)
}

// FinishTaskRun finalizes a run exactly once: it fills the end time before updating the terminal columns.
// DurationMs is expected to be set by the caller because the unix-second timestamps cannot express sub-second runs.
func FinishTaskRun(ctx context.Context, run *TaskRun) error {
	run.FinishedUnix = timeutil.TimeStampNow()
	_, err := db.GetEngine(ctx).ID(run.ID).
		Cols("status", "error_category", "finished_unix", "duration_ms", "message").
		Update(run)
	if err != nil {
		return fmt.Errorf("FinishTaskRun: %w", err)
	}
	return nil
}

// ListTaskRuns returns the most recent runs of one task, newest first.
func ListTaskRuns(ctx context.Context, taskName string, page, pageSize int) ([]*TaskRun, int64, error) {
	count, err := db.GetEngine(ctx).Where("task_name = ?", taskName).Count(new(TaskRun))
	if err != nil {
		return nil, 0, fmt.Errorf("ListTaskRuns: %w", err)
	}
	runs := make([]*TaskRun, 0, pageSize)
	if err = db.GetEngine(ctx).Where("task_name = ?", taskName).
		Desc("id").Limit(pageSize, (page-1)*pageSize).Find(&runs); err != nil {
		return nil, 0, fmt.Errorf("ListTaskRuns: %w", err)
	}
	return runs, count, nil
}

// TaskHasRunningRun reports whether the task has a run in progress according to the records.
func TaskHasRunningRun(ctx context.Context, taskName string) (bool, error) {
	has, err := db.GetEngine(ctx).Where("task_name = ?", taskName).And("status = ?", StatusRunning).Exist(&TaskRun{})
	if err != nil {
		return false, fmt.Errorf("TaskHasRunningRun: %w", err)
	}
	return has, nil
}

// CountConsecutiveFailures counts terminal runs backwards from the latest: failed/timeout increment,
// success stops the count, cancelled/skipped/interrupted are ignored and the scan continues.
func CountConsecutiveFailures(ctx context.Context, taskName string, maxRows int64) (int64, error) {
	runs := make([]*TaskRun, 0, maxRows)
	if err := db.GetEngine(ctx).Where("task_name = ?", taskName).
		Desc("id").Limit(int(maxRows), 0).Find(&runs); err != nil {
		return 0, fmt.Errorf("CountConsecutiveFailures: %w", err)
	}
	var count int64
	for _, run := range runs {
		switch run.Status {
		case StatusFailed, StatusTimeout:
			count++
		case StatusSuccess:
			return count, nil
		}
	}
	return count, nil
}

// GetLastSkippedRun returns the most recent skipped run of the task, if any.
func GetLastSkippedRun(ctx context.Context, taskName string) (*TaskRun, bool, error) {
	run := &TaskRun{}
	has, err := db.GetEngine(ctx).Where("task_name = ?", taskName).And("status = ?", StatusSkipped).
		Desc("id").Limit(1).Get(run)
	if err != nil {
		return nil, false, fmt.Errorf("GetLastSkippedRun: %w", err)
	}
	return run, has, nil
}

// MarkInterruptedRuns turns running records left over from a previous process into interrupted ones,
// without assigning an end time. Returns the number of affected rows.
func MarkInterruptedRuns(ctx context.Context) (int64, error) {
	res, err := db.GetEngine(ctx).Exec(
		"UPDATE cron_task_run SET status = ?, error_category = ? WHERE status = ?",
		StatusInterrupted, CategoryProcessKilled, StatusRunning,
	)
	if err != nil {
		return 0, fmt.Errorf("MarkInterruptedRuns: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("MarkInterruptedRuns: %w", err)
	}
	return affected, nil
}

// CleanupTaskRuns applies the retention policy. Finished records older than retainDuration are deleted,
// and only the newest retainCount finished records per task are kept. Unfinished records are never
// deleted. An empty taskNames list cleans all tasks.
func CleanupTaskRuns(ctx context.Context, retainCount int64, retainDuration time.Duration, taskNames ...string) error {
	if retainDuration > 0 {
		cutoff := time.Now().Add(-retainDuration).Unix()
		del := db.GetEngine(ctx).Where("finished_unix > 0").And("finished_unix < ?", cutoff)
		if len(taskNames) > 0 {
			del.In("task_name", taskNames)
		}
		if _, err := del.Delete(&TaskRun{}); err != nil {
			return fmt.Errorf("CleanupTaskRuns: %w", err)
		}
	}

	if retainCount <= 0 {
		return nil
	}

	names := taskNames
	if len(names) == 0 {
		if err := db.GetEngine(ctx).Table(&TaskRun{}).Cols("task_name").Distinct("task_name").Find(&names); err != nil {
			return fmt.Errorf("CleanupTaskRuns: %w", err)
		}
	}
	for _, name := range names {
		// Only finished rows are eligible, so an in-flight run can never be pruned out from under itself.
		// Everything at or below the id of the (retainCount+1)-th newest finished row is overflow.
		thresholdIDs := make([]int64, 0, 1)
		if err := db.GetEngine(ctx).Table(&TaskRun{}).Cols("id").
			Where("task_name = ?", name).And("finished_unix > 0").
			Desc("id").Limit(1, int(retainCount)).Find(&thresholdIDs); err != nil {
			return fmt.Errorf("CleanupTaskRuns: %w", err)
		}
		if len(thresholdIDs) == 0 {
			continue
		}
		if _, err := db.GetEngine(ctx).Where("task_name = ?", name).And("id <= ?", thresholdIDs[0]).
			And("finished_unix > 0").Delete(&TaskRun{}); err != nil {
			return fmt.Errorf("CleanupTaskRuns: %w", err)
		}
	}
	return nil
}
