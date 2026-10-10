// Copyright 2020 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package cron

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	cron_model "gitea.dev/models/cron"
	"gitea.dev/models/db"
	system_model "gitea.dev/models/system"
	user_model "gitea.dev/models/user"
	"gitea.dev/modules/globallock"
	"gitea.dev/modules/graceful"
	"gitea.dev/modules/log"
	"gitea.dev/modules/process"
	"gitea.dev/modules/setting"
	"gitea.dev/modules/timeutil"
	"gitea.dev/modules/translation"

	"github.com/go-co-op/gocron/v2"
)

var (
	lock     = sync.Mutex{}
	started  = false
	tasks    = []*Task{}
	tasksMap = map[string]*Task{}
)

// ErrTaskAlreadyRunning is returned by RunWithUser when the overlap policy is "reject"
// and a previous run of the task has not finished.
var ErrTaskAlreadyRunning = errors.New("the cron task is already running")

// Task represents a Cron task
type Task struct {
	lock        sync.Mutex
	Name        string
	config      Config
	fun         func(context.Context, *user_model.User, Config) error
	Status      string
	LastMessage string
	LastDoer    string
	ExecTimes   int64
	// This stores the time of the last manual run of this task.
	LastRun time.Time

	// inFlight is true while a run is executing in this process.
	inFlight bool
	// queued is true while one trigger is waiting for the in-flight run (overlap policy "queue").
	queued bool
}

// DoRunAtStart returns if this task should run at the start
func (t *Task) DoRunAtStart() bool {
	return t.config.DoRunAtStart()
}

// IsEnabled returns if this task is enabled as cron task
func (t *Task) IsEnabled() bool {
	return t.config.IsEnabled()
}

// GetConfig will return a copy of the task's config
func (t *Task) GetConfig() Config {
	if reflect.TypeOf(t.config).Kind() == reflect.Pointer {
		return reflect.New(reflect.ValueOf(t.config).Elem().Type()).Interface().(Config) //nolint:forcetypeassert // pointer
	}
	return reflect.New(reflect.TypeOf(t.config)).Elem().Interface().(Config) //nolint:forcetypeassert // not pointer
}

// Run will run the task incrementing the cron counter with no user defined
func (t *Task) Run() {
	if err := t.RunWithUser(&user_model.User{
		ID:        -1,
		Name:      "(Cron)",
		LowerName: "(cron)",
	}, t.config); err != nil {
		log.Warn("Scheduled trigger of cron task %q was not started: %v", t.Name, err)
	}
}

// RunWithUserAsync is RunWithUser for callers that must not block: the overlap decision happens
// synchronously (so a rejection is returned immediately) and an accepted run executes in background.
func (t *Task) RunWithUserAsync(doer *user_model.User, config Config) error {
	admission, err := t.admit(doer)
	if err != nil {
		return err
	}
	if !admission.accepted {
		return nil
	}
	go t.execute(doer, config, admission)
	return nil
}

func getCronTaskLockKey(name string) string {
	return "cron_task:" + name
}

// runAdmission is the result of the synchronous trigger decision. accepted=false means
// nothing to execute (a silent legacy skip or a recorded skipped trigger).
type runAdmission struct {
	accepted bool
	release  globallock.ReleaseFunc
	// queued means the lock was busy and the execution has to block on acquiring it before running.
	queued bool
	// legacy keeps the old behavior when execution history is disabled.
	legacy bool
}

// RunWithUser triggers the task at the time with User and runs it in the caller's goroutine.
// The overlap decision is synchronous: a rejected trigger returns ErrTaskAlreadyRunning.
func (t *Task) RunWithUser(doer *user_model.User, config Config) error {
	admission, err := t.admit(doer)
	if err != nil {
		return err
	}
	if admission.accepted {
		t.execute(doer, config, admission)
	}
	return nil
}

// admit performs the synchronous overlap decision and acquires the global lock when possible.
func (t *Task) admit(doer *user_model.User) (runAdmission, error) {
	ctx := graceful.GetManager().ShutdownContext()
	locked, release, err := globallock.TryLock(ctx, getCronTaskLockKey(t.Name))
	if err != nil {
		log.Error("Failed to acquire lock for cron task %q: %v", t.Name, err)
		if !setting.Cron.EnableExecutionHistory {
			return runAdmission{}, nil
		}
		return runAdmission{}, fmt.Errorf("admit: %w", err)
	}
	if locked {
		return runAdmission{accepted: true, release: release, legacy: !setting.Cron.EnableExecutionHistory}, nil
	}

	if !setting.Cron.EnableExecutionHistory {
		log.Trace("a cron task %q is already running", t.Name)
		return runAdmission{}, nil
	}

	switch setting.Cron.OverlapPolicy {
	case setting.CronOverlapPolicyReject:
		return runAdmission{}, fmt.Errorf("admit: %w (task %q)", ErrTaskAlreadyRunning, t.Name)
	case setting.CronOverlapPolicyQueue:
		t.lock.Lock()
		if t.queued {
			t.lock.Unlock()
			t.recordSkippedRun(ctx, doer, cron_model.CategoryQueueFull, "a queued run is already waiting")
			return runAdmission{}, nil
		}
		t.queued = true
		t.lock.Unlock()
		return runAdmission{accepted: true, queued: true}, nil
	default: // skip
		t.recordSkippedRun(ctx, doer, cron_model.CategoryOverlapSkipped, "the previous run has not finished")
		return runAdmission{}, nil
	}
}

// execute runs an admitted task. It is executed in its own goroutine for manual triggers.
func (t *Task) execute(doer *user_model.User, config Config, admission runAdmission) {
	if !admission.accepted {
		return
	}
	if config == nil {
		config = t.config
	}
	if admission.legacy {
		defer admission.release()
		t.runLegacy(doer, config)
		return
	}

	ctx := graceful.GetManager().ShutdownContext()
	release := admission.release
	if admission.queued {
		var err error
		release, err = globallock.Lock(ctx, getCronTaskLockKey(t.Name))
		if err != nil {
			// The waiting trigger was canceled, typically because the server is shutting down.
			log.Warn("Queued cron task %q did not start: %v", t.Name, err)
			t.lock.Lock()
			t.queued = false
			t.lock.Unlock()
			return
		}
	}
	defer release()

	t.lock.Lock()
	t.queued = false
	t.inFlight = true
	t.ExecTimes++
	t.lock.Unlock()
	defer func() {
		t.lock.Lock()
		t.inFlight = false
		t.lock.Unlock()
	}()

	t.runWithHistory(doer, config)
}

// runLegacy keeps the exact pre-history behavior: no DB records, panic is only logged.
func (t *Task) runLegacy(doer *user_model.User, config Config) {
	t.lock.Lock()
	t.ExecTimes++
	t.lock.Unlock()

	graceful.GetManager().RunWithShutdownContext(func(baseCtx context.Context) {
		defer func() {
			if err := recover(); err != nil {
				// Recover a panic within the execution of the task.
				combinedErr := fmt.Errorf("%s\n%s", err, log.Stack(2))
				log.Error("PANIC whilst running task: %s Value: %v", t.Name, combinedErr)
			}
		}()
		// Store the time of this run, before the function is executed, so it
		// matches the behavior of what the cron library does.
		t.lock.Lock()
		t.LastRun = time.Now()
		t.lock.Unlock()

		pm := process.GetManager()
		doerName := ""
		if doer != nil && doer.ID != -1 {
			doerName = doer.Name
		}

		ctx, _, finished := pm.AddContext(baseCtx, config.FormatMessage(translation.NewLocale("en-US"), t.Name, "process", doerName))
		defer finished()

		if err := t.fun(ctx, doer, config); err != nil {
			var message string
			var status string
			if errCancelled, ok := err.(db.ErrCancelled); ok {
				status = "cancelled"
				message = errCancelled.Message
			} else {
				status = "error"
				message = err.Error()
			}

			t.lock.Lock()
			t.LastMessage = message
			t.Status = status
			t.LastDoer = doerName
			t.lock.Unlock()

			if err := system_model.CreateNotice(ctx, system_model.NoticeTask, config.FormatMessage(translation.NewLocale("en-US"), t.Name, "cancelled", doerName, message)); err != nil {
				log.Error("CreateNotice: %v", err)
			}
			return
		}

		t.lock.Lock()
		t.Status = "finished"
		t.LastMessage = ""
		t.LastDoer = doerName
		t.lock.Unlock()

		if config.DoNoticeOnSuccess() {
			if err := system_model.CreateNotice(ctx, system_model.NoticeTask, config.FormatMessage(translation.NewLocale("en-US"), t.Name, "finished", doerName)); err != nil {
				log.Error("CreateNotice: %v", err)
			}
		}
	})
}

type panicError struct {
	value any
	stack string
}

func (e *panicError) Error() string {
	return fmt.Sprintf("%v", e.value)
}

// runWithHistory executes once, writing the running/terminal TaskRun record around t.fun.
func (t *Task) runWithHistory(doer *user_model.User, config Config) {
	graceful.GetManager().RunWithShutdownContext(func(baseCtx context.Context) {
		startedAt := time.Now()
		t.lock.Lock()
		t.LastRun = startedAt
		t.lock.Unlock()

		triggerType := cron_model.TriggerTypeSchedule
		doerID := int64(-1)
		doerName := ""
		if doer != nil && doer.ID != -1 {
			triggerType = cron_model.TriggerTypeManual
			doerID = doer.ID
			doerName = doer.Name
		}

		run := &cron_model.TaskRun{
			TaskName:    t.Name,
			TriggerType: triggerType,
			DoerID:      doerID,
			DoerName:    doerName,
			Status:      cron_model.StatusRunning,
			StartedUnix: timeutil.TimeStamp(startedAt.Unix()),
		}
		if err := cron_model.InsertTaskRun(baseCtx, run); err != nil {
			log.Error("InsertTaskRun: %v", err)
			run.ID = 0 // do not finalize a row that was not created
		}

		ctx, cancelTimeout := context.WithCancel(baseCtx)
		if setting.Cron.RunTimeout > 0 {
			ctx, cancelTimeout = context.WithTimeout(baseCtx, setting.Cron.RunTimeout)
		}
		defer cancelTimeout()

		ctx, _, finished := process.GetManager().AddContext(ctx, config.FormatMessage(translation.NewLocale("en-US"), t.Name, "process", doerName))
		defer finished()

		funErrCh := make(chan error, 1)
		go func() {
			defer func() {
				if r := recover(); r != nil {
					log.Error("PANIC whilst running task: %s Value: %v\n%s", t.Name, r, log.Stack(2))
					funErrCh <- &panicError{value: r, stack: log.Stack(2)}
				}
			}()
			funErrCh <- t.fun(ctx, doer, config)
		}()

		timedOut := false
		var timerCh <-chan time.Time
		if setting.Cron.RunTimeout > 0 {
			timer := time.NewTimer(setting.Cron.RunTimeout)
			defer timer.Stop()
			timerCh = timer.C
		}

		var funErr error
		select {
		case funErr = <-funErrCh:
		case <-timerCh:
			timedOut = true
		}

		if timedOut {
			// Finalize at the timeout moment; the late function result must not overwrite this record.
			message := "run timed out after " + setting.Cron.RunTimeout.String()
			t.finishRunRecord(baseCtx, run, startedAt, cron_model.StatusTimeout, cron_model.CategoryDeadline, message)
			t.updateLastState(cron_model.StatusTimeout, message, doerName)
			// ctx is canceled by the timeout, so persist the notice with the shutdown context instead.
			t.createResultNotice(baseCtx, config, cron_model.StatusTimeout, doerName, message)
			if err := cron_model.CleanupTaskRuns(baseCtx, setting.Cron.HistoryRetentionCount, setting.Cron.HistoryRetentionDuration, t.Name); err != nil {
				log.Error("CleanupTaskRuns: %v", err)
			}
			// Keep the lock until the function actually returns, so two executions can never overlap.
			<-funErrCh
			return
		}

		status, category, message := classifyRunError(funErr)
		// A function returning the deadline error at the same instant the timer fires is still a timeout.
		// Check our own ctx so a deadline from a child context created by the task is not mistaken for it.
		if funErr != nil && errors.Is(funErr, context.DeadlineExceeded) && errors.Is(ctx.Err(), context.DeadlineExceeded) {
			status = cron_model.StatusTimeout
			category = cron_model.CategoryDeadline
			message = "run timed out after " + setting.Cron.RunTimeout.String()
		}
		t.finishRunRecord(baseCtx, run, startedAt, status, category, message)
		t.updateLastState(status, message, doerName)
		t.createResultNotice(baseCtx, config, status, doerName, message)
		if err := cron_model.CleanupTaskRuns(baseCtx, setting.Cron.HistoryRetentionCount, setting.Cron.HistoryRetentionDuration, t.Name); err != nil {
			log.Error("CleanupTaskRuns: %v", err)
		}
	})
}

func classifyRunError(err error) (status, category, message string) {
	if err == nil {
		return cron_model.StatusSuccess, cron_model.CategoryNone, ""
	}
	if errCancelled, ok := errors.AsType[db.ErrCancelled](err); ok {
		return cron_model.StatusCancelled, cron_model.CategoryContextCanceled, errCancelled.Message
	}
	if panicErr, ok := errors.AsType[*panicError](err); ok {
		return cron_model.StatusFailed, cron_model.CategoryPanic, panicErr.Error()
	}
	return cron_model.StatusFailed, cron_model.CategoryTaskError, err.Error()
}

func (t *Task) finishRunRecord(ctx context.Context, run *cron_model.TaskRun, startedAt time.Time, status, category, message string) {
	if run.ID == 0 {
		return
	}
	run.Status = status
	run.ErrorCategory = category
	run.Message = message
	run.DurationMs = time.Since(startedAt).Milliseconds()
	if err := cron_model.FinishTaskRun(ctx, run); err != nil {
		log.Error("FinishTaskRun: %v", err)
	}
}

// updateLastState keeps the in-memory fields populated, mapping new statuses to the legacy dashboard strings.
func (t *Task) updateLastState(status, message, doerName string) {
	t.lock.Lock()
	defer t.lock.Unlock()
	t.LastDoer = doerName
	switch status {
	case cron_model.StatusSuccess:
		t.Status = "finished"
		t.LastMessage = ""
	case cron_model.StatusCancelled:
		t.Status = "cancelled"
		t.LastMessage = message
	default: // failed, timeout
		t.Status = "error"
		t.LastMessage = message
	}
}

func (t *Task) createResultNotice(ctx context.Context, config Config, status, doerName, message string) {
	switch status {
	case cron_model.StatusSuccess:
		if !config.DoNoticeOnSuccess() {
			return
		}
		if err := system_model.CreateNotice(ctx, system_model.NoticeTask, config.FormatMessage(translation.NewLocale("en-US"), t.Name, "finished", doerName)); err != nil {
			log.Error("CreateNotice: %v", err)
		}
	default:
		if err := system_model.CreateNotice(ctx, system_model.NoticeTask, config.FormatMessage(translation.NewLocale("en-US"), t.Name, "cancelled", doerName, message)); err != nil {
			log.Error("CreateNotice: %v", err)
		}
	}
}

func (t *Task) recordSkippedRun(ctx context.Context, doer *user_model.User, category, reason string) {
	triggerType := cron_model.TriggerTypeSchedule
	doerID := int64(-1)
	doerName := ""
	if doer != nil && doer.ID != -1 {
		triggerType = cron_model.TriggerTypeManual
		doerID = doer.ID
		doerName = doer.Name
	}
	now := timeutil.TimeStampNow()
	run := &cron_model.TaskRun{
		TaskName:      t.Name,
		TriggerType:   triggerType,
		DoerID:        doerID,
		DoerName:      doerName,
		Status:        cron_model.StatusSkipped,
		ErrorCategory: category,
		StartedUnix:   now,
		FinishedUnix:  now,
		Message:       reason,
	}
	if err := cron_model.InsertTaskRun(ctx, run); err != nil {
		log.Error("InsertTaskRun: %v", err)
		return
	}
	if err := cron_model.CleanupTaskRuns(ctx, setting.Cron.HistoryRetentionCount, setting.Cron.HistoryRetentionDuration, t.Name); err != nil {
		log.Error("CleanupTaskRuns: %v", err)
	}
}

// GetTask gets the named task
func GetTask(name string) *Task {
	lock.Lock()
	defer lock.Unlock()
	log.Info("Getting %s in %v", name, tasksMap[name])

	return tasksMap[name]
}

// RegisterTask allows a task to be registered with the cron service
func RegisterTask[T Config](name string, config T, fun func(context.Context, *user_model.User, T) error) error {
	log.Debug("Registering task: %s", name)

	i18nKey := "admin.dashboard." + name
	if value := translation.NewLocale("en-US").TrString(i18nKey); value == i18nKey {
		return fmt.Errorf("translation is missing for task %q, please add translation for %q", name, i18nKey)
	}

	_, err := setting.GetCronSettings(name, config)
	if err != nil {
		log.Error("Unable to register cron task with name: %s Error: %v", name, err)
		return err
	}

	task := &Task{
		Name:   name,
		config: config,
		fun: func(ctx context.Context, doer *user_model.User, runConfig Config) error {
			return fun(ctx, doer, runConfig.(T)) //nolint:forcetypeassert // must be valid
		},
	}
	lock.Lock()
	locked := true
	defer func() {
		if locked {
			lock.Unlock()
		}
	}()
	if _, has := tasksMap[task.Name]; has {
		log.Error("A task with this name: %s has already been registered", name)
		return fmt.Errorf("duplicate task with name: %s", task.Name)
	}

	if config.IsEnabled() {
		// We cannot use the entry return as there is no way to lock it
		if err := addTaskToScheduler(task); err != nil {
			return err
		}
	}

	tasks = append(tasks, task)
	tasksMap[task.Name] = task
	if started && config.IsEnabled() && config.DoRunAtStart() {
		lock.Unlock()
		locked = false
		task.Run()
	}

	return nil
}

// RegisterTaskFatal will register a task but if there is an error log.Fatal
func RegisterTaskFatal[T Config](name string, config T, fun func(context.Context, *user_model.User, T) error) {
	if err := RegisterTask(name, config, fun); err != nil {
		log.Fatal("Unable to register cron task %s Error: %v", name, err)
	}
}

func addTaskToScheduler(task *Task) error {
	tags := []string{task.Name, task.config.GetSchedule()} // name and schedule can't be get from job, so we add them as tag
	withSeconds := scheduleHasSeconds(task.config.GetSchedule())
	_, err := scheduler.NewJob(
		gocron.CronJob(task.config.GetSchedule(), withSeconds),
		gocron.NewTask(task.Run),
		gocron.WithTags(tags...),
	)
	if err != nil {
		log.Error("Unable to register cron task with name: %s Error: %v", task.Name, err)
		return err
	}
	return nil
}

func scheduleHasSeconds(schedule string) bool {
	return len(strings.Fields(schedule)) >= 6
}
