// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package cron_test

import (
	"testing"
	"time"

	cron_model "gitea.dev/models/cron"
	"gitea.dev/models/db"
	"gitea.dev/models/unittest"
	"gitea.dev/modules/timeutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func addRun(t *testing.T, run *cron_model.TaskRun) *cron_model.TaskRun {
	t.Helper()
	require.NoError(t, cron_model.InsertTaskRun(t.Context(), run))
	return run
}

// cron_task_run has no fixture yml, so PrepareTestDatabase does not empty it.
func resetTaskRuns(t *testing.T) {
	t.Helper()
	_, err := db.GetEngine(t.Context()).Exec("DELETE FROM cron_task_run")
	require.NoError(t, err)
}

func TestTaskRunFinishAndList(t *testing.T) {
	assert.NoError(t, unittest.PrepareTestDatabase())
	resetTaskRuns(t)
	ctx := t.Context()

	started := timeutil.TimeStampNow()
	run := addRun(t, &cron_model.TaskRun{
		TaskName:    "task_roundtrip",
		TriggerType: cron_model.TriggerTypeManual,
		DoerID:      1,
		DoerName:    "admin",
		Status:      cron_model.StatusRunning,
		StartedUnix: started,
	})
	assert.Positive(t, run.ID)

	running, err := cron_model.TaskHasRunningRun(ctx, "task_roundtrip")
	assert.NoError(t, err)
	assert.True(t, running)

	run.Status = cron_model.StatusSuccess
	require.NoError(t, cron_model.FinishTaskRun(ctx, run))
	assert.Positive(t, run.FinishedUnix)
	assert.GreaterOrEqual(t, run.DurationMs, int64(0))

	running, err = cron_model.TaskHasRunningRun(ctx, "task_roundtrip")
	assert.NoError(t, err)
	assert.False(t, running)

	runs, total, err := cron_model.ListTaskRuns(ctx, "task_roundtrip", 1, 10)
	assert.NoError(t, err)
	assert.EqualValues(t, 1, total)
	if assert.Len(t, runs, 1) {
		assert.Equal(t, cron_model.StatusSuccess, runs[0].Status)
		assert.Equal(t, "admin", runs[0].DoerName)
	}

	_, total, err = cron_model.ListTaskRuns(ctx, "no_such_task", 1, 10)
	assert.NoError(t, err)
	assert.Zero(t, total)
}

func TestCountConsecutiveFailures(t *testing.T) {
	assert.NoError(t, unittest.PrepareTestDatabase())
	resetTaskRuns(t)
	ctx := t.Context()

	count, err := cron_model.CountConsecutiveFailures(ctx, "task_failures", 100)
	assert.NoError(t, err)
	assert.Zero(t, count)

	// Insert order (oldest to newest): success, failed, timeout, cancelled, skipped, failed.
	// Scanning newest-first: failed +1; skipped/cancelled transparent; timeout +1; failed +1; success stops.
	statuses := []string{
		cron_model.StatusSuccess,
		cron_model.StatusFailed,
		cron_model.StatusTimeout,
		cron_model.StatusCancelled,
		cron_model.StatusSkipped,
		cron_model.StatusFailed,
	}
	for i, status := range statuses {
		addRun(t, &cron_model.TaskRun{
			TaskName:    "task_failures",
			Status:      status,
			StartedUnix: timeutil.TimeStamp(int64(i + 1)),
		})
	}
	count, err = cron_model.CountConsecutiveFailures(ctx, "task_failures", 100)
	assert.NoError(t, err)
	assert.EqualValues(t, 3, count)
}

func TestGetLastSkippedRun(t *testing.T) {
	assert.NoError(t, unittest.PrepareTestDatabase())
	resetTaskRuns(t)
	ctx := t.Context()

	addRun(t, &cron_model.TaskRun{TaskName: "task_skip", Status: cron_model.StatusSkipped, Message: "first skip", StartedUnix: timeutil.TimeStamp(1)})
	addRun(t, &cron_model.TaskRun{TaskName: "task_skip", Status: cron_model.StatusSuccess, StartedUnix: timeutil.TimeStamp(2)})
	addRun(t, &cron_model.TaskRun{TaskName: "task_skip", Status: cron_model.StatusSkipped, Message: "second skip", StartedUnix: timeutil.TimeStamp(3)})
	addRun(t, &cron_model.TaskRun{TaskName: "task_other", Status: cron_model.StatusSuccess, StartedUnix: timeutil.TimeStamp(4)})

	run, has, err := cron_model.GetLastSkippedRun(ctx, "task_skip")
	assert.NoError(t, err)
	require.True(t, has)
	assert.Equal(t, "second skip", run.Message)

	_, has, err = cron_model.GetLastSkippedRun(ctx, "task_other")
	assert.NoError(t, err)
	assert.False(t, has)
}

func TestCleanupTaskRunsKeepsInFlightByCount(t *testing.T) {
	assert.NoError(t, unittest.PrepareTestDatabase())
	resetTaskRuns(t)
	ctx := t.Context()

	// An in-flight row older than the overflowing skip rows must never be pruned by the count policy.
	addRun(t, &cron_model.TaskRun{TaskName: "task_inflight", Status: cron_model.StatusRunning, StartedUnix: timeutil.TimeStamp(1)})
	addRun(t, &cron_model.TaskRun{TaskName: "task_inflight", Status: cron_model.StatusSkipped, ErrorCategory: cron_model.CategoryOverlapSkipped, StartedUnix: timeutil.TimeStampNow(), FinishedUnix: timeutil.TimeStampNow()})
	secondSkip := addRun(t, &cron_model.TaskRun{TaskName: "task_inflight", Status: cron_model.StatusSkipped, ErrorCategory: cron_model.CategoryOverlapSkipped, StartedUnix: timeutil.TimeStampNow(), FinishedUnix: timeutil.TimeStampNow()})

	require.NoError(t, cron_model.CleanupTaskRuns(ctx, 1, 0, "task_inflight"))

	runs, total, err := cron_model.ListTaskRuns(ctx, "task_inflight", 1, 50)
	require.NoError(t, err)
	assert.EqualValues(t, 2, total) // running + newest skipped
	require.Len(t, runs, 2)
	assert.Equal(t, secondSkip.ID, runs[0].ID)
	assert.Equal(t, cron_model.StatusRunning, runs[1].Status)

	running, err := cron_model.TaskHasRunningRun(ctx, "task_inflight")
	require.NoError(t, err)
	assert.True(t, running)
}

func TestMarkInterruptedRuns(t *testing.T) {
	assert.NoError(t, unittest.PrepareTestDatabase())
	resetTaskRuns(t)
	ctx := t.Context()

	addRun(t, &cron_model.TaskRun{TaskName: "task_interrupt", Status: cron_model.StatusSuccess, StartedUnix: timeutil.TimeStamp(1), FinishedUnix: timeutil.TimeStamp(2)})
	addRun(t, &cron_model.TaskRun{TaskName: "task_interrupt", Status: cron_model.StatusRunning, StartedUnix: timeutil.TimeStamp(10)})
	addRun(t, &cron_model.TaskRun{TaskName: "task_interrupt2", Status: cron_model.StatusRunning, StartedUnix: timeutil.TimeStamp(11)})

	affected, err := cron_model.MarkInterruptedRuns(ctx)
	assert.NoError(t, err)
	assert.EqualValues(t, 2, affected)

	runs, _, err := cron_model.ListTaskRuns(ctx, "task_interrupt", 1, 10)
	assert.NoError(t, err)
	require.Len(t, runs, 2)
	assert.Equal(t, cron_model.StatusInterrupted, runs[0].Status) // newest first
	assert.Equal(t, cron_model.CategoryProcessKilled, runs[0].ErrorCategory)
	assert.Zero(t, runs[0].FinishedUnix)
	assert.Equal(t, cron_model.StatusSuccess, runs[1].Status)

	// Idempotent: no running rows remain.
	affected, err = cron_model.MarkInterruptedRuns(ctx)
	assert.NoError(t, err)
	assert.Zero(t, affected)

	running, err := cron_model.TaskHasRunningRun(ctx, "task_interrupt")
	assert.NoError(t, err)
	assert.False(t, running)
}

func TestCleanupTaskRuns(t *testing.T) {
	assert.NoError(t, unittest.PrepareTestDatabase())
	resetTaskRuns(t)
	ctx := t.Context()

	now := time.Now()
	ts := func(d time.Duration) timeutil.TimeStamp { return timeutil.TimeStamp(now.Add(d).Unix()) }

	// task_a: 5 rows, newest first: id order a1..a5
	// a1 running (unfinished, must survive everything)
	addRun(t, &cron_model.TaskRun{TaskName: "task_a", Status: cron_model.StatusRunning, StartedUnix: ts(-10 * time.Hour)})
	// a2 finished 10h ago, a3 5h, a4 2h, a1-started ordering below: use explicit chronological ids
	addRun(t, &cron_model.TaskRun{TaskName: "task_a", Status: cron_model.StatusFailed, StartedUnix: ts(-10 * time.Hour), FinishedUnix: ts(-10 * time.Hour)})
	addRun(t, &cron_model.TaskRun{TaskName: "task_a", Status: cron_model.StatusSuccess, StartedUnix: ts(-5 * time.Hour), FinishedUnix: ts(-5 * time.Hour)})
	addRun(t, &cron_model.TaskRun{TaskName: "task_a", Status: cron_model.StatusInterrupted, ErrorCategory: cron_model.CategoryProcessKilled, StartedUnix: ts(-4 * time.Hour)})
	addRun(t, &cron_model.TaskRun{TaskName: "task_a", Status: cron_model.StatusSuccess, StartedUnix: ts(-2 * time.Hour), FinishedUnix: ts(-2 * time.Hour)})
	// task_b: 3 finished rows
	for i := range 3 {
		addRun(t, &cron_model.TaskRun{TaskName: "task_b", Status: cron_model.StatusSuccess, StartedUnix: ts(-time.Duration(i) * time.Hour), FinishedUnix: ts(-time.Duration(i) * time.Hour)})
	}

	// Retain 2 finished rows per task; duration cutoff 3h.
	require.NoError(t, cron_model.CleanupTaskRuns(ctx, 2, 3*time.Hour))

	leftA, totalA, err := cron_model.ListTaskRuns(ctx, "task_a", 1, 50)
	require.NoError(t, err)
	assert.EqualValues(t, 3, totalA) // newest finished(-2h), interrupted(-4h), running(-10h)
	require.Len(t, leftA, 3)
	assert.Equal(t, cron_model.StatusSuccess, leftA[0].Status)
	assert.Equal(t, cron_model.StatusInterrupted, leftA[1].Status)
	assert.Equal(t, cron_model.StatusRunning, leftA[2].Status)

	leftB, totalB, err := cron_model.ListTaskRuns(ctx, "task_b", 1, 50)
	require.NoError(t, err)
	// newest two finished survive by count; the third (oldest) also exceeds the duration cutoff anyway.
	assert.EqualValues(t, 2, totalB)
	require.Len(t, leftB, 2)
	for _, r := range leftB {
		assert.Equal(t, cron_model.StatusSuccess, r.Status)
	}
}
