// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package structs

import "time"

// HookTask delivery states.
const (
	HookTaskStatusPending = "pending" // not attempted yet
	HookTaskStatusSucceed = "succeed" // the last attempt succeeded
	HookTaskStatusFailed  = "failed"  // terminal failure that is never retried
	HookTaskStatusWaiting = "waiting" // retryable failure, waiting for backoff
	HookTaskStatusDead    = "dead"    // automatic retries exhausted
	HookTaskStatusSending = "sending" // an attempt is in flight
)

// HookTaskRequest is the recorded HTTP request of a delivery attempt.
type HookTaskRequest struct {
	URL        string            `json:"url"`
	HTTPMethod string            `json:"http_method"`
	Headers    map[string]string `json:"headers"`
	Body       string            `json:"body"`
}

// HookTaskResponse is the recorded HTTP response of a delivery attempt.
type HookTaskResponse struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
}

// HookTask represents one webhook delivery task and its retry state.
type HookTask struct {
	ID          int64  `json:"id"`
	HookID      int64  `json:"hook_id"`
	UUID        string `json:"uuid"`
	EventType   string `json:"event_type"`
	Status      string `json:"status"`
	IsDelivered bool   `json:"is_delivered"`
	IsSucceed   bool   `json:"is_succeed"`
	Retries     int    `json:"retries"`
	LastError   string `json:"last_error"`

	Request  *HookTaskRequest  `json:"request,omitempty"`
	Response *HookTaskResponse `json:"response,omitempty"`

	Delivered   *time.Time `json:"delivered,omitempty"`
	NextRetryAt *time.Time `json:"next_retry_at,omitempty"`
}

// RedeliverHookTasksOption selects dead-letter tasks for a batch redelivery.
type RedeliverHookTasksOption struct {
	EventType string     `json:"event_type"`
	Since     *time.Time `json:"since,omitempty"`
	Until     *time.Time `json:"until,omitempty"`
}

// RedeliverHookTasksResponse contains the number of dead-letter tasks requeued.
type RedeliverHookTasksResponse struct {
	Redelivered int64 `json:"redelivered"`
}
