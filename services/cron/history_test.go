// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package cron

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	cron_model "gitea.dev/models/cron"
	"gitea.dev/models/db"
	"gitea.dev/models/unittest"
	user_model "gitea.dev/models/user"
	"gitea.dev/modules/setting"
	"gitea.dev/modules/test"
	"gitea.dev/modules/timeutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var testDoer = &user_model.User{ID: 42, Name: "test-admin"}

var testDoerScheduled = &user_model.User{ID: -1, Name: "(Cron)"}

func mockCronSettings(t *testing.T, enabled bool, policy string, runTimeout time.Duration, retainCount int64) func() {
	t.Helper()
	resets := []func(){
		test.MockVariableValue(&setting.Cron.EnableExecutionHistory, enabled),
		test.MockVariableValue(&setting.Cron.OverlapPolicy, policy),
		test.MockVariableValue(&setting.Cron.RunTimeout, runTimeout),
		test.MockVariableValue(&setting.Cron.HistoryRetentionCount, retainCount),
		test.MockVariableValue(&setting.Cron.HistoryRetentionDuration, time.Duration(0)),
	}
	return func() {
		for _, r := range resets {
			r()
		}
	}
}

func resetTaskRuns(t *testing.T) {
	t.Helper()
	_, err := db.GetEngine(t.Context()).Exec("DELETE FROM cron_task_run")
	require.NoError(t, err)
}

func newTestTask(name string, fun func(ctx context.Context) error) *Task {
	return &Task{
		Name:   name,
		config: &BaseConfig{},
		fun: func(ctx context.Context, _ *user_model.User, _ Config) error {
			return fun(ctx)
		},
	}
}

func runSync(t *testing.T, task *Task, doer *user_model.User) error {
	t.Helper()
	admission, err := task.admit(doer)
	if err != nil || !admission.accepted {
		return err
	}
	task.execute(doer, nil, admission)
	return nil
}

func runsOf(t *testing.T, name string) []*cron_model.TaskRun {
	t.Helper()
	runs, _, err := cron_model.ListTaskRuns(t.Context(), name, 1, 50)
	require.NoError(t, err)
	return runs
}

func waitFor(t *testing.T, msg string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	require.True(t, cond(), msg)
}

func blockingFun(entered chan<- struct{}, release <-chan struct{}, retErr error) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		entered <- struct{}{}
		<-release
		return retErr
	}
}

func TestRunWithHistoryStatusesAndFailureCount(t *testing.T) {
	assert.NoError(t, unittest.PrepareTestDatabase())
	resetTaskRuns(t)
	defer mockCronSettings(t, true, setting.CronOverlapPolicySkip, 0, 100)()

	// success with full record fields
	okTask := newTestTask("t_statuses_ok", func(ctx context.Context) error { return nil })
	require.NoError(t, runSync(t, okTask, testDoer))
	okRuns := runsOf(t, "t_statuses_ok")
	require.Len(t, okRuns, 1)
	r := okRuns[0]
	assert.Equal(t, cron_model.StatusSuccess, r.Status)
	assert.Equal(t, cron_model.CategoryNone, r.ErrorCategory)
	assert.Equal(t, cron_model.TriggerTypeManual, r.TriggerType)
	assert.Equal(t, int64(42), r.DoerID)
	assert.Equal(t, "test-admin", r.DoerName)
	assert.Positive(t, r.FinishedUnix)
	assert.GreaterOrEqual(t, r.FinishedUnix, r.StartedUnix)
	assert.Equal(t, "finished", okTask.Status)

	// two failures then a success resets the consecutive failure count
	failTask := newTestTask("t_statuses_fail", func(ctx context.Context) error {
		return assertErr
	})
	require.NoError(t, runSync(t, failTask, testDoer))
	require.NoError(t, runSync(t, failTask, testDoerScheduled))
	failRuns := runsOf(t, "t_statuses_fail")
	require.Len(t, failRuns, 2)
	for _, run := range failRuns {
		assert.Equal(t, cron_model.StatusFailed, run.Status)
		assert.Equal(t, cron_model.CategoryTaskError, run.ErrorCategory)
		assert.Equal(t, assertErr.Error(), run.Message)
	}
	count, err := cron_model.CountConsecutiveFailures(t.Context(), "t_statuses_fail", 100)
	require.NoError(t, err)
	assert.EqualValues(t, 2, count)
	assert.Equal(t, "error", failTask.Status)
	assert.Equal(t, assertErr.Error(), failTask.LastMessage)

	failTask.fun = func(ctx context.Context, _ *user_model.User, _ Config) error { return nil }
	require.NoError(t, runSync(t, failTask, testDoer))
	count, err = cron_model.CountConsecutiveFailures(t.Context(), "t_statuses_fail", 100)
	require.NoError(t, err)
	assert.Zero(t, count)
}

func TestRunWithHistoryPanic(t *testing.T) {
	assert.NoError(t, unittest.PrepareTestDatabase())
	resetTaskRuns(t)
	defer mockCronSettings(t, true, setting.CronOverlapPolicySkip, 0, 100)()

	panicTask := newTestTask("t_panic", func(ctx context.Context) error {
		panic("boom panic")
	})
	require.NoError(t, runSync(t, panicTask, testDoer))

	runs := runsOf(t, "t_panic")
	require.Len(t, runs, 1)
	assert.Equal(t, cron_model.StatusFailed, runs[0].Status)
	assert.Equal(t, cron_model.CategoryPanic, runs[0].ErrorCategory)
	assert.Contains(t, runs[0].Message, "boom panic")
	assert.Equal(t, "error", panicTask.Status)
}

func TestRunWithHistoryCancelledKeepsFailureCount(t *testing.T) {
	assert.NoError(t, unittest.PrepareTestDatabase())
	resetTaskRuns(t)
	defer mockCronSettings(t, true, setting.CronOverlapPolicySkip, 0, 100)()

	task := newTestTask("t_cancelled", func(ctx context.Context) error { return assertErr })
	require.NoError(t, runSync(t, task, testDoer)) // failure -> count 1

	task.fun = func(ctx context.Context, _ *user_model.User, _ Config) error {
		return db.ErrCancelled{Message: "stopped"}
	}
	require.NoError(t, runSync(t, task, testDoer))

	runs := runsOf(t, "t_cancelled")
	require.Len(t, runs, 2)
	assert.Equal(t, cron_model.StatusCancelled, runs[0].Status)
	assert.Equal(t, cron_model.CategoryContextCanceled, runs[0].ErrorCategory)
	assert.Equal(t, "stopped", runs[0].Message)

	count, err := cron_model.CountConsecutiveFailures(t.Context(), "t_cancelled", 100)
	require.NoError(t, err)
	assert.EqualValues(t, 1, count) // cancelled does not increment
	assert.Equal(t, "cancelled", task.Status)
}

func TestRunWithHistoryTimeoutFinalizesEarlyAndKeepsLock(t *testing.T) {
	assert.NoError(t, unittest.PrepareTestDatabase())
	resetTaskRuns(t)
	defer mockCronSettings(t, true, setting.CronOverlapPolicySkip, 80*time.Millisecond, 100)()

	var calls atomic.Int32
	entered := make(chan struct{}, 1)
	lingering := make(chan struct{}, 1)
	allowReturn := make(chan struct{})
	task := newTestTask("t_timeout", func(ctx context.Context) error {
		calls.Add(1)
		entered <- struct{}{}
		<-ctx.Done()
		// The function stays alive after the deadline until the test allows it to return.
		lingering <- struct{}{}
		<-allowReturn
		return ctx.Err()
	})

	runDone := make(chan struct{})
	go func() {
		_ = runSync(t, task, testDoer)
		close(runDone)
	}()
	<-entered

	// While the function is still in flight, a second trigger is skipped and never executes.
	admission, err := task.admit(testDoer)
	require.NoError(t, err)
	assert.False(t, admission.accepted)
	waitFor(t, "overlap_skipped record", func() bool {
		_, has, _ := cron_model.GetLastSkippedRun(t.Context(), "t_timeout")
		return has
	})

	// The run is finalized as timeout at the deadline.
	var timeoutRun *cron_model.TaskRun
	waitFor(t, "timeout record", func() bool {
		runs := runsOf(t, "t_timeout")
		for _, run := range runs {
			if run.Status == cron_model.StatusTimeout {
				timeoutRun = run
				return true
			}
		}
		return false
	})
	require.NotNil(t, timeoutRun)
	assert.Equal(t, cron_model.CategoryDeadline, timeoutRun.ErrorCategory)
	assert.Contains(t, timeoutRun.Message, "timed out")
	assert.GreaterOrEqual(t, timeoutRun.DurationMs, int64(50))
	assert.Less(t, timeoutRun.DurationMs, int64(5000))

	// The function has observed the cancellation but is still holding the lock, so the colliding
	// trigger above never executed and the timeout record is finalized before the function returns.
	<-lingering
	assert.Equal(t, int32(1), calls.Load())
	close(allowReturn)
	<-runDone

	// The lingering result did not overwrite the timeout record.
	runs := runsOf(t, "t_timeout")
	var terminal *cron_model.TaskRun
	for _, run := range runs {
		if run.ID == timeoutRun.ID {
			terminal = run
		}
	}
	require.NotNil(t, terminal)
	assert.Equal(t, cron_model.StatusTimeout, terminal.Status)
	count, err := cron_model.CountConsecutiveFailures(t.Context(), "t_timeout", 100)
	require.NoError(t, err)
	assert.EqualValues(t, 1, count) // timeout counts as a failure
}

func TestRunWithHistoryTaskDeadlineIsNotRunTimeout(t *testing.T) {
	assert.NoError(t, unittest.PrepareTestDatabase())
	resetTaskRuns(t)
	// A generous run timeout: the task's own deadline error must not be mistaken for a run timeout.
	defer mockCronSettings(t, true, setting.CronOverlapPolicySkip, 10*time.Second, 100)()

	task := newTestTask("t_task_deadline", func(ctx context.Context) error {
		return context.DeadlineExceeded // returns before the run timeout elapsed, our ctx is not done
	})
	require.NoError(t, runSync(t, task, testDoer))

	runs := runsOf(t, "t_task_deadline")
	require.Len(t, runs, 1)
	assert.Equal(t, cron_model.StatusFailed, runs[0].Status)
	assert.Equal(t, cron_model.CategoryTaskError, runs[0].ErrorCategory)
}

func TestOverlapPolicySkip(t *testing.T) {
	assert.NoError(t, unittest.PrepareTestDatabase())
	resetTaskRuns(t)
	defer mockCronSettings(t, true, setting.CronOverlapPolicySkip, 0, 100)()

	var calls atomic.Int32
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	task := newTestTask("t_skip", func(ctx context.Context) error {
		calls.Add(1)
		return blockingFun(entered, release, nil)(ctx)
	})

	runDone := make(chan struct{})
	go func() { _ = runSync(t, task, testDoer); close(runDone) }()
	<-entered

	// One manual and one scheduled colliding trigger: both recorded, neither executes.
	adm, err := task.admit(testDoer)
	require.NoError(t, err)
	assert.False(t, adm.accepted)
	adm, err = task.admit(testDoerScheduled)
	require.NoError(t, err)
	assert.False(t, adm.accepted)

	close(release)
	<-runDone

	assert.Equal(t, int32(1), calls.Load())
	runs := runsOf(t, "t_skip")
	var skipped, success int
	for _, run := range runs {
		switch run.Status {
		case cron_model.StatusSkipped:
			skipped++
			assert.Equal(t, cron_model.CategoryOverlapSkipped, run.ErrorCategory)
			assert.NotEmpty(t, run.Message)
		case cron_model.StatusSuccess:
			success++
		}
	}
	assert.Equal(t, 2, skipped)
	assert.Equal(t, 1, success)
	for _, run := range runs {
		if run.Status == cron_model.StatusSkipped {
			assert.Equal(t, run.StartedUnix, run.FinishedUnix) // skipped triggers take no execution time
			assert.Zero(t, run.DurationMs)
		}
	}

	// Aggregates visible for the list.
	row := &TaskTableRow{Name: "t_skip"}
	fillTaskHistory(row)
	assert.False(t, row.Running)
	assert.Zero(t, row.ConsecutiveFailures)
	assert.NotEmpty(t, row.LastSkippedReason)
}

func TestOverlapPolicyQueue(t *testing.T) {
	assert.NoError(t, unittest.PrepareTestDatabase())
	resetTaskRuns(t)
	defer mockCronSettings(t, true, setting.CronOverlapPolicyQueue, 0, 100)()

	entered1 := make(chan struct{}, 1)
	release1 := make(chan struct{})
	entered2 := make(chan struct{}, 1)
	release2 := make(chan struct{})
	var calls atomic.Int32
	task := newTestTask("t_queue", nil)
	task.fun = func(ctx context.Context, _ *user_model.User, _ Config) error {
		n := calls.Add(1)
		switch n {
		case 1:
			return blockingFun(entered1, release1, nil)(ctx)
		default:
			return blockingFun(entered2, release2, assertErr)(ctx)
		}
	}

	firstDone := make(chan struct{})
	go func() { _ = runSync(t, task, testDoer); close(firstDone) }()
	<-entered1

	// Second trigger queues; third overflows as queue_full.
	queuedAdm, err := task.admit(testDoer)
	require.NoError(t, err)
	assert.True(t, queuedAdm.accepted)
	assert.True(t, queuedAdm.queued)

	overflowAdm, err := task.admit(testDoer)
	require.NoError(t, err)
	assert.False(t, overflowAdm.accepted)
	overflowRuns := runsOf(t, "t_queue")
	var overflow *cron_model.TaskRun
	for _, run := range overflowRuns {
		if run.ErrorCategory == cron_model.CategoryQueueFull {
			overflow = run
		}
	}
	require.NotNil(t, overflow)
	assert.Equal(t, cron_model.StatusSkipped, overflow.Status)

	secondDone := make(chan struct{})
	go func() { task.execute(testDoer, nil, queuedAdm); close(secondDone) }()

	close(release1)
	<-firstDone
	<-entered2 // second run starts only after the first finished

	firstRuns := runsOf(t, "t_queue")
	var run1, run2 *cron_model.TaskRun
	for _, run := range firstRuns {
		if run.Status == cron_model.StatusSuccess {
			run1 = run
		}
	}
	require.NotNil(t, run1)

	close(release2)
	<-secondDone

	finalRuns := runsOf(t, "t_queue")
	for _, run := range finalRuns {
		if run.Status == cron_model.StatusFailed && run.ErrorCategory == cron_model.CategoryTaskError {
			run2 = run
		}
	}
	require.NotNil(t, run2)
	assert.Equal(t, int32(2), calls.Load())
	assert.LessOrEqual(t, run1.FinishedUnix, run2.StartedUnix) // no overlap, second start is not earlier
}

func TestOverlapPolicyReject(t *testing.T) {
	assert.NoError(t, unittest.PrepareTestDatabase())
	resetTaskRuns(t)
	defer mockCronSettings(t, true, setting.CronOverlapPolicyReject, 0, 100)()

	var calls atomic.Int32
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	task := newTestTask("t_reject", nil)
	task.fun = func(ctx context.Context, _ *user_model.User, _ Config) error {
		calls.Add(1)
		return blockingFun(entered, release, nil)(ctx)
	}

	done := make(chan struct{})
	go func() { _ = runSync(t, task, testDoer); close(done) }()
	<-entered

	_, err := task.admit(testDoer)
	require.ErrorIs(t, err, ErrTaskAlreadyRunning)

	close(release)
	<-done

	assert.Equal(t, int32(1), calls.Load())
	runs := runsOf(t, "t_reject")
	require.Len(t, runs, 1) // rejected triggers leave no record
	assert.Equal(t, cron_model.StatusSuccess, runs[0].Status)
}

func TestRecoverInterruptedRunsAtStartup(t *testing.T) {
	assert.NoError(t, unittest.PrepareTestDatabase())
	resetTaskRuns(t)
	defer mockCronSettings(t, true, setting.CronOverlapPolicySkip, 0, 100)()

	require.NoError(t, cron_model.InsertTaskRun(t.Context(), &cron_model.TaskRun{
		TaskName:    "t_interrupted",
		Status:      cron_model.StatusRunning,
		StartedUnix: timeutil.TimeStampNow(),
	}))

	recoverInterruptedTaskRuns(t.Context())

	runs := runsOf(t, "t_interrupted")
	require.Len(t, runs, 1)
	assert.Equal(t, cron_model.StatusInterrupted, runs[0].Status)
	assert.Equal(t, cron_model.CategoryProcessKilled, runs[0].ErrorCategory)
	assert.Zero(t, runs[0].FinishedUnix)

	row := &TaskTableRow{Name: "t_interrupted"}
	fillTaskHistory(row)
	assert.False(t, row.Running)
}

func TestHistoryDisabledKeepsLegacyBehavior(t *testing.T) {
	assert.NoError(t, unittest.PrepareTestDatabase())
	resetTaskRuns(t)
	defer mockCronSettings(t, false, setting.CronOverlapPolicyReject, 0, 100)()

	var calls atomic.Int32
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	task := newTestTask("t_disabled", nil)
	task.fun = func(ctx context.Context, _ *user_model.User, _ Config) error {
		calls.Add(1)
		return blockingFun(entered, release, nil)(ctx)
	}

	done := make(chan struct{})
	go func() { _ = runSync(t, task, testDoer); close(done) }()
	<-entered

	// Collision is silently ignored even though the policy says reject: history is off.
	adm, err := task.admit(testDoer)
	assert.NoError(t, err)
	assert.False(t, adm.accepted)

	close(release)
	<-done
	assert.Equal(t, int32(1), calls.Load())
	assert.Empty(t, runsOf(t, "t_disabled")) // no records at all

	// Panics remain log-only and do not touch the last status.
	task = newTestTask("t_disabled_panic", func(ctx context.Context) error { panic("legacy panic") })
	require.NotPanics(t, func() { require.NoError(t, runSync(t, task, testDoer)) })
	assert.Empty(t, task.Status)
}

func TestHistoryStorageFailureDoesNotBreakRun(t *testing.T) {
	assert.NoError(t, unittest.PrepareTestDatabase())
	resetTaskRuns(t)
	defer mockCronSettings(t, true, setting.CronOverlapPolicySkip, 0, 100)()

	_, err := db.GetEngine(t.Context()).Exec("DROP TABLE cron_task_run")
	require.NoError(t, err)
	t.Cleanup(func() {
		// Recreate the table so later tests (and fixture resets) have it again.
		assert.NoError(t, db.SyncAllTables())
	})

	executed := make(chan struct{}, 1)
	task := newTestTask("t_storage_fail", func(ctx context.Context) error {
		executed <- struct{}{}
		return nil
	})
	require.NoError(t, runSync(t, task, testDoer))
	select {
	case <-executed:
	default:
		t.Fatal("task function did not run despite history storage failure")
	}
	assert.Equal(t, "finished", task.Status)
}

// assertErr is a plain task error.
var assertErr = assertErrValue{}

type assertErrValue struct{}

func (assertErrValue) Error() string { return "boom task error" }
