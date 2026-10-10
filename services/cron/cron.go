// Copyright 2014 The Gogs Authors. All rights reserved.
// Copyright 2019 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package cron

import (
	"context"
	"runtime/pprof"
	"time"

	cron_model "gitea.dev/models/cron"
	"gitea.dev/modules/graceful"
	"gitea.dev/modules/log"
	"gitea.dev/modules/process"
	"gitea.dev/modules/setting"
	"gitea.dev/modules/translation"

	"github.com/go-co-op/gocron/v2"
)

var scheduler gocron.Scheduler

func init() {
	var err error
	scheduler, err = gocron.NewScheduler(gocron.WithLocation(time.Local))
	if err != nil {
		log.Fatal("Unable to create cron scheduler: %v", err)
	}
}

// Init begins cron tasks
// Each cron task is run within the shutdown context as a running server
// AtShutdown the cron server is stopped
func Init(original context.Context) {
	defer pprof.SetGoroutineLabels(original)
	_, _, finished := process.GetManager().AddTypedContext(graceful.GetManager().ShutdownContext(), "Service: Cron", process.SystemProcessType, true)

	if setting.Cron.EnableExecutionHistory {
		recoverInterruptedTaskRuns(original)
	}

	initBasicTasks()
	initExtendedTasks()
	initActionsTasks()

	if setting.Cron.EnableExecutionHistory {
		preloadLastTaskStates(original)
		if err := cron_model.CleanupTaskRuns(original, setting.Cron.HistoryRetentionCount, setting.Cron.HistoryRetentionDuration); err != nil {
			log.Error("CleanupTaskRuns: %v", err)
		}
	}

	lock.Lock()
	for _, task := range tasks {
		if task.IsEnabled() && task.DoRunAtStart() {
			go task.Run()
		}
	}

	scheduler.Start()
	started = true
	lock.Unlock()
	graceful.GetManager().RunAtShutdown(context.Background(), func() {
		if err := scheduler.Shutdown(); err != nil {
			log.Error("Unable to shutdown cron scheduler: %v", err)
		}
		lock.Lock()
		started = false
		lock.Unlock()
		finished()
	})
}

// recoverInterruptedTaskRuns marks runs left running by a killed process so that
// they are neither treated as still running nor lost from the history.
func recoverInterruptedTaskRuns(ctx context.Context) {
	affected, err := cron_model.MarkInterruptedRuns(ctx)
	if err != nil {
		log.Error("MarkInterruptedRuns: %v", err)
		return
	}
	if affected > 0 {
		log.Warn("Marked %d cron task run(s) as interrupted after restart", affected)
	}
}

// preloadLastTaskStates restores the latest result of each task into the in-memory fields,
// which otherwise start empty after a restart. Skipped and interrupted runs are ignored.
func preloadLastTaskStates(ctx context.Context) {
	lock.Lock()
	snapshot := make([]*Task, len(tasks))
	copy(snapshot, tasks)
	lock.Unlock()

	for _, task := range snapshot {
		runs, _, err := cron_model.ListTaskRuns(ctx, task.Name, 1, 10)
		if err != nil {
			log.Error("ListTaskRuns: %v", err)
			continue
		}
		for _, run := range runs {
			if run.Status == cron_model.StatusSkipped || run.Status == cron_model.StatusInterrupted {
				continue
			}
			task.lock.Lock()
			task.Status = mapRunStatusToLegacy(run.Status)
			task.LastMessage = run.Message
			task.LastDoer = run.DoerName
			task.lock.Unlock()
			break
		}
	}
}

func mapRunStatusToLegacy(status string) string {
	switch status {
	case cron_model.StatusSuccess:
		return "finished"
	case cron_model.StatusCancelled:
		return "cancelled"
	default:
		return "error"
	}
}

// TaskTableRow represents a task row in the tasks table
type TaskTableRow struct {
	Name        string
	Spec        string
	Next        time.Time
	Prev        time.Time
	Status      string
	LastMessage string
	LastDoer    string
	ExecTimes   int64
	task        *Task

	// Execution history aggregates, populated only when [cron] ENABLE_EXECUTION_HISTORY is on.
	Running             bool
	ConsecutiveFailures int64
	LastSkippedTime     time.Time
	LastSkippedReason   string
}

func (t *TaskTableRow) FormatLastMessage(locale translation.Locale) string {
	if t.Status == "finished" {
		return t.task.GetConfig().FormatMessage(locale, t.Name, t.Status, t.LastDoer)
	}

	return t.task.GetConfig().FormatMessage(locale, t.Name, t.Status, t.LastDoer, t.LastMessage)
}

// TaskTable represents a table of tasks
type TaskTable []*TaskTableRow

// ListTasks returns all running cron tasks.
func ListTasks() TaskTable {
	jobs := scheduler.Jobs()
	jobMap := map[string]gocron.Job{}
	for _, job := range jobs {
		// the first tag is the task name
		tags := job.Tags()
		if len(tags) == 0 { // should never happen
			continue
		}
		jobMap[tags[0]] = job
	}

	// Snapshot the tasks and their in-memory fields under the lock, then do the history
	// aggregations without blocking task registration and triggers.
	lock.Lock()
	tTable := make([]*TaskTableRow, 0, len(tasks))
	for _, task := range tasks {
		spec := "-"
		var (
			next time.Time
			prev time.Time
		)
		if e, ok := jobMap[task.Name]; ok {
			tags := e.Tags()
			if len(tags) > 1 {
				spec = tags[1] // the second tag is the task spec
			}
			next, _ = e.NextRun()
			prev, _ = e.LastRunStartedAt()
		}

		task.lock.Lock()
		// If the manual run is after the cron run, use that instead.
		if prev.Before(task.LastRun) {
			prev = task.LastRun
		}
		row := &TaskTableRow{
			Name:        task.Name,
			Spec:        spec,
			Next:        next,
			Prev:        prev,
			ExecTimes:   task.ExecTimes,
			LastMessage: task.LastMessage,
			Status:      task.Status,
			LastDoer:    task.LastDoer,
			task:        task,
			Running:     task.inFlight,
		}
		task.lock.Unlock()

		tTable = append(tTable, row)
	}
	lock.Unlock()

	if setting.Cron.EnableExecutionHistory {
		for _, row := range tTable {
			fillTaskHistory(row)
		}
	}

	return tTable
}

// ListTaskRuns returns the recent recorded runs of one task, newest first.
func ListTaskRuns(name string, page, pageSize int) ([]*cron_model.TaskRun, int64, error) {
	return cron_model.ListTaskRuns(graceful.GetManager().ShutdownContext(), name, page, pageSize)
}

func fillTaskHistory(row *TaskTableRow) {
	ctx := graceful.GetManager().ShutdownContext()

	dbRunning, err := cron_model.TaskHasRunningRun(ctx, row.Name)
	if err != nil {
		log.Error("TaskHasRunningRun: %v", err)
	}
	row.Running = row.Running || dbRunning

	window := setting.Cron.HistoryRetentionCount
	if window <= 0 {
		window = 1000
	}
	failures, err := cron_model.CountConsecutiveFailures(ctx, row.Name, window)
	if err != nil {
		log.Error("CountConsecutiveFailures: %v", err)
	} else {
		row.ConsecutiveFailures = failures
	}

	if lastSkipped, has, err := cron_model.GetLastSkippedRun(ctx, row.Name); err != nil {
		log.Error("GetLastSkippedRun: %v", err)
	} else if has {
		row.LastSkippedTime = lastSkipped.StartedUnix.AsTime()
		row.LastSkippedReason = lastSkipped.Message
	}
}
