// Copyright 2020 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package structs

import "time"

// Cron represents a Cron task
type Cron struct {
	// The name of the cron task
	Name string `json:"name"`
	// The cron schedule expression (e.g., "0 0 * * *")
	Schedule string `json:"schedule"`
	// The next scheduled execution time
	Next time.Time `json:"next"`
	// The previous execution time
	Prev time.Time `json:"prev"`
	// The total number of times this cron task has been executed
	ExecTimes int64 `json:"exec_times"`
	// Whether a run is currently in progress (only present when cron execution history is enabled)
	Running *bool `json:"running,omitempty"`
	// Number of consecutive failed/timeout runs (only present when cron execution history is enabled)
	ConsecutiveFailures *int64 `json:"consecutive_failures,omitempty"`
	// The most recent skipped trigger, if any (only present when cron execution history is enabled)
	LastSkipped *CronSkipped `json:"last_skipped,omitempty"`
}

// CronSkipped describes the most recent trigger that was skipped instead of executed.
type CronSkipped struct {
	// When the trigger was skipped
	Time time.Time `json:"time"`
	// Why the trigger was skipped
	Reason string `json:"reason"`
}

// CronTaskRun represents one recorded run (or attempted run) of a cron task.
type CronTaskRun struct {
	ID            int64      `json:"id"`
	TaskName      string     `json:"task_name"`
	TriggerType   string     `json:"trigger_type"`
	DoerID        int64      `json:"doer_id"`
	DoerName      string     `json:"doer_name"`
	Status        string     `json:"status"`
	ErrorCategory string     `json:"error_category"`
	Started       time.Time  `json:"started"`
	Finished      *time.Time `json:"finished,omitempty"`
	DurationMs    int64      `json:"duration_ms"`
	Message       string     `json:"message"`
}
