// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package webhook

import (
	"context"
	"math"
	"math/rand/v2"
	"time"

	webhook_model "gitea.dev/models/webhook"
	"gitea.dev/modules/log"
	"gitea.dev/modules/optional"
	"gitea.dev/modules/setting"
	api "gitea.dev/modules/structs"
	"gitea.dev/modules/timeutil"
	webhook_module "gitea.dev/modules/webhook"
)

// retryScanInterval is how often the scheduler looks for due and stale tasks.
// Workers never sleep for the backoff duration: failed tasks leave the queue and
// wait as rows until the scheduler enqueues them again.
const retryScanInterval = 5 * time.Second

func retryEnabled() bool {
	return setting.Webhook.RetryMaxAttempts > 0
}

// staleSendingDuration is the grace period before an in-flight attempt that
// never wrote back a result (e.g. crashed process) may be reclaimed.
func staleSendingDuration() time.Duration {
	d := 2 * time.Duration(setting.Webhook.DeliverTimeout) * time.Second
	if d < 2*time.Minute {
		return 2 * time.Minute
	}
	return d
}

// nextRetryDelay computes the exponential backoff for the given automatic
// retry number (1-based) capped at the configured maximum and spread by jitter:
// delay = min(base * 2^(attempt-1), max) * (1 + rand(-jitter, +jitter)).
func nextRetryDelay(attempt int) time.Duration {
	d := float64(setting.Webhook.RetryBackoff)
	d *= math.Pow(2, float64(attempt-1))
	if maxDelay := float64(setting.Webhook.RetryBackoffMax); d > maxDelay {
		d = maxDelay
	}
	factor := 1 + (2*rand.Float64()-1)*setting.Webhook.RetryJitterFactor
	delay := time.Duration(d * factor)
	if delay < 0 {
		return 0
	}
	return delay
}

// claimHookTask atomically claims the next attempt of a task. Only one of
// several concurrent workers (on one or more instances) can win the claim.
func claimHookTask(ctx context.Context, t *webhook_model.HookTask, now timeutil.TimeStampNano) (bool, error) {
	if !t.IsDelivered {
		return webhook_model.ClaimPendingHookTask(ctx, t, now)
	}
	switch t.Status {
	case webhook_model.HookTaskStatusWaiting:
		if t.NextRetryAt == 0 || t.NextRetryAt > now {
			return false, nil
		}
		return webhook_model.ClaimDueRetryHookTask(ctx, t, now)
	case webhook_model.HookTaskStatusSending:
		staleBefore := now - timeutil.TimeStampNano(staleSendingDuration())
		if t.Delivered > staleBefore {
			return false, nil
		}
		return webhook_model.ClaimStaleSendingHookTask(ctx, t, now, staleBefore)
	default:
		return false, nil
	}
}

// isHookTaskDue is the queue-side pre-check mirroring claimHookTask; the claim
// stays the authoritative deduplication point.
func isHookTaskDue(t *webhook_model.HookTask, now timeutil.TimeStampNano) bool {
	if !t.IsDelivered {
		return true
	}
	switch t.Status {
	case webhook_model.HookTaskStatusWaiting:
		return t.NextRetryAt > 0 && t.NextRetryAt <= now
	case webhook_model.HookTaskStatusSending:
		return t.Delivered <= now-timeutil.TimeStampNano(staleSendingDuration())
	}
	return false
}

// enqueueDueRetryTasks pushes waiting tasks whose backoff is due to the queue.
func enqueueDueRetryTasks(ctx context.Context) {
	now := timeutil.TimeStampNanoNow()
	enqueueHookTaskBatches(ctx, "Webhook: Populate due retry queue", func(lowerID int64) ([]int64, error) {
		return webhook_model.FindDueRetryHookTaskIDs(ctx, now, lowerID)
	})
}

// enqueueStaleSendingTasks reclaims attempts abandoned by crashed processes.
func enqueueStaleSendingTasks(ctx context.Context) {
	staleBefore := timeutil.TimeStampNanoNow() - timeutil.TimeStampNano(staleSendingDuration())
	enqueueHookTaskBatches(ctx, "Webhook: Populate stale sending queue", func(lowerID int64) ([]int64, error) {
		return webhook_model.FindStaleSendingHookTaskIDs(ctx, staleBefore, lowerID)
	})
}

func populateRetrySendingQueue(ctx context.Context) {
	enqueueDueRetryTasks(ctx)
	if ctx.Err() == nil {
		enqueueStaleSendingTasks(ctx)
	}
}

func retryScheduleLoop(ctx context.Context) {
	ticker := time.NewTicker(retryScanInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			enqueueDueRetryTasks(ctx)
			enqueueStaleSendingTasks(ctx)
		}
	}
}

// HookTaskStatusName maps the model status to its stable API name.
func HookTaskStatusName(status webhook_model.HookTaskStatus) string {
	switch status {
	case webhook_model.HookTaskStatusSucceed:
		return api.HookTaskStatusSucceed
	case webhook_model.HookTaskStatusFailed:
		return api.HookTaskStatusFailed
	case webhook_model.HookTaskStatusWaiting:
		return api.HookTaskStatusWaiting
	case webhook_model.HookTaskStatusDead:
		return api.HookTaskStatusDead
	case webhook_model.HookTaskStatusSending:
		return api.HookTaskStatusSending
	default:
		return api.HookTaskStatusPending
	}
}

// ParseHookTaskStatusName parses an API status name into a model status.
func ParseHookTaskStatusName(name string) (webhook_model.HookTaskStatus, bool) {
	switch name {
	case api.HookTaskStatusPending:
		return webhook_model.HookTaskStatusPending, true
	case api.HookTaskStatusSucceed:
		return webhook_model.HookTaskStatusSucceed, true
	case api.HookTaskStatusFailed:
		return webhook_model.HookTaskStatusFailed, true
	case api.HookTaskStatusWaiting:
		return webhook_model.HookTaskStatusWaiting, true
	case api.HookTaskStatusDead:
		return webhook_model.HookTaskStatusDead, true
	case api.HookTaskStatusSending:
		return webhook_model.HookTaskStatusSending, true
	default:
		return 0, false
	}
}

// ToHookTask converts a model HookTask to its API representation.
func ToHookTask(t *webhook_model.HookTask) *api.HookTask {
	dto := &api.HookTask{
		ID:          t.ID,
		HookID:      t.HookID,
		UUID:        t.UUID,
		EventType:   string(t.EventType),
		Status:      HookTaskStatusName(t.Status),
		IsDelivered: t.IsDelivered,
		IsSucceed:   t.IsSucceed,
		Retries:     t.Retries,
		LastError:   t.LastError,
	}
	if t.RequestInfo != nil {
		dto.Request = &api.HookTaskRequest{
			URL:        t.RequestInfo.URL,
			HTTPMethod: t.RequestInfo.HTTPMethod,
			Headers:    t.RequestInfo.Headers,
			Body:       t.RequestInfo.Body,
		}
	}
	if t.ResponseInfo != nil {
		dto.Response = &api.HookTaskResponse{
			Status:  t.ResponseInfo.Status,
			Headers: t.ResponseInfo.Headers,
			Body:    t.ResponseInfo.Body,
		}
	}
	if t.Delivered > 0 {
		delivered := t.Delivered.AsTime()
		dto.Delivered = &delivered
	}
	if t.NextRetryAt > 0 {
		nextRetryAt := t.NextRetryAt.AsTime()
		dto.NextRetryAt = &nextRetryAt
	}
	return dto
}

// RequeueDeadTasks resets matching dead-letter tasks of one webhook for an
// operator-triggered batch redelivery and enqueues them. The automatic retry
// counter is preserved; concurrent invocations cannot send an attempt twice.
func RequeueDeadTasks(ctx context.Context, hookID int64, eventType optional.Option[webhook_module.HookEventType],
	since, until timeutil.TimeStampNano,
) (int64, error) {
	if _, err := webhook_model.GetWebhookByID(ctx, hookID); err != nil {
		return 0, err
	}

	taskIDs, err := webhook_model.RequeueDeadHookTasks(ctx, hookID, webhook_model.RequeueDeadHookTasksOptions{
		EventType: eventType,
		Since:     since,
		Until:     until,
	})
	if err != nil {
		return 0, err
	}
	for _, taskID := range taskIDs {
		if err := enqueueHookTask(taskID); err != nil {
			log.Error("Unable to push requeued HookTask[%d] to the Webhook Sending queue: %v", taskID, err)
		}
	}
	return int64(len(taskIDs)), nil
}
