// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package webhook

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gitea.dev/models/db"
	"gitea.dev/models/unittest"
	webhook_model "gitea.dev/models/webhook"
	"gitea.dev/modules/optional"
	"gitea.dev/modules/setting"
	"gitea.dev/modules/timeutil"
	"gitea.dev/modules/util"
	webhook_module "gitea.dev/modules/webhook"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func enableRetrySettings(t *testing.T, maxAttempts int) {
	t.Helper()
	old := setting.Webhook
	t.Cleanup(func() { setting.Webhook = old })
	setting.Webhook.RetryMaxAttempts = maxAttempts
	setting.Webhook.RetryBackoff = time.Millisecond
	setting.Webhook.RetryBackoffMax = 5 * time.Millisecond
	setting.Webhook.RetryJitterFactor = 0
}

func newRetryTestWebhook(t *testing.T, url string, active bool) *webhook_model.Webhook {
	t.Helper()
	hook := &webhook_model.Webhook{
		RepoID:      3,
		URL:         url,
		ContentType: webhook_model.ContentTypeJSON,
		IsActive:    active,
		Type:        webhook_module.GITEA,
	}
	require.NoError(t, webhook_model.CreateWebhook(t.Context(), hook))
	return hook
}

func newRetryTestTask(t *testing.T, hook *webhook_model.Webhook) *webhook_model.HookTask {
	t.Helper()
	task, err := webhook_model.CreateHookTask(t.Context(), &webhook_model.HookTask{
		HookID:         hook.ID,
		PayloadContent: `{"hello":"world"}`,
		EventType:      webhook_module.HookEventPush,
		PayloadVersion: 2,
	})
	require.NoError(t, err)
	return task
}

// forceRetryDue rewrites next_retry_at so the task becomes claimable without waiting for the backoff.
func forceRetryDue(t *testing.T, taskID int64) {
	t.Helper()
	_, err := db.GetEngine(t.Context()).ID(taskID).Cols("next_retry_at").
		Update(&webhook_model.HookTask{NextRetryAt: 1})
	require.NoError(t, err)
}

func reloadRetryTask(t *testing.T, taskID int64) *webhook_model.HookTask {
	t.Helper()
	task, err := webhook_model.GetHookTaskByID(t.Context(), taskID)
	require.NoError(t, err)
	return task
}

func statusHTTPServer(t *testing.T, statusFn func() int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(statusFn())
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestNextRetryDelay(t *testing.T) {
	old := setting.Webhook
	t.Cleanup(func() { setting.Webhook = old })
	setting.Webhook.RetryBackoff = 100 * time.Millisecond
	setting.Webhook.RetryBackoffMax = time.Second
	setting.Webhook.RetryJitterFactor = 0

	assert.Equal(t, 100*time.Millisecond, nextRetryDelay(1))
	assert.Equal(t, 200*time.Millisecond, nextRetryDelay(2))
	assert.Equal(t, 400*time.Millisecond, nextRetryDelay(3))
	assert.Equal(t, time.Second, nextRetryDelay(10))

	// jitter factor 1 spreads the delay across [0, 2*base]
	setting.Webhook.RetryJitterFactor = 1
	for range 100 {
		d := nextRetryDelay(1)
		assert.GreaterOrEqual(t, d, time.Duration(0))
		assert.LessOrEqual(t, d, 200*time.Millisecond)
	}

	// factor 0.2 keeps it around the base
	setting.Webhook.RetryJitterFactor = 0.2
	for range 100 {
		d := nextRetryDelay(1)
		assert.GreaterOrEqual(t, d, 80*time.Millisecond)
		assert.LessOrEqual(t, d, 120*time.Millisecond)
	}
}

func TestClassifyHookTaskStatus(t *testing.T) {
	retryable, _ := classifyHookTaskStatus(http.StatusRequestTimeout)
	assert.True(t, retryable)
	retryable, _ = classifyHookTaskStatus(http.StatusTooManyRequests)
	assert.True(t, retryable)
	retryable, _ = classifyHookTaskStatus(http.StatusBadGateway)
	assert.True(t, retryable)
	retryable, _ = classifyHookTaskStatus(http.StatusInternalServerError)
	assert.True(t, retryable)

	retryable, _ = classifyHookTaskStatus(http.StatusBadRequest)
	assert.False(t, retryable)
	retryable, _ = classifyHookTaskStatus(http.StatusNotFound)
	assert.False(t, retryable)
	retryable, _ = classifyHookTaskStatus(http.StatusUnauthorized)
	assert.False(t, retryable)
}

func TestWebhookRetryExhaustedEntersDead(t *testing.T) {
	assert.NoError(t, unittest.PrepareTestDatabase())
	enableRetrySettings(t, 2)

	var hits atomic.Int32
	srv := statusHTTPServer(t, func() int { hits.Add(1); return http.StatusInternalServerError })
	hook := newRetryTestWebhook(t, srv.URL+"/hook", true)
	task := newRetryTestTask(t, hook)

	require.NoError(t, Deliver(t.Context(), task))
	task = reloadRetryTask(t, task.ID)
	assert.Equal(t, webhook_model.HookTaskStatusWaiting, task.Status)
	assert.Equal(t, 1, task.Retries)
	assert.Greater(t, int64(task.NextRetryAt), int64(task.Delivered))
	assert.Contains(t, task.LastError, "500")
	assert.Equal(t, http.StatusInternalServerError, task.ResponseInfo.Status)

	forceRetryDue(t, task.ID)
	task = reloadRetryTask(t, task.ID)
	require.NoError(t, Deliver(t.Context(), task))
	task = reloadRetryTask(t, task.ID)
	assert.Equal(t, webhook_model.HookTaskStatusWaiting, task.Status)
	assert.Equal(t, 2, task.Retries)

	forceRetryDue(t, task.ID)
	task = reloadRetryTask(t, task.ID)
	require.NoError(t, Deliver(t.Context(), task))
	task = reloadRetryTask(t, task.ID)
	assert.Equal(t, webhook_model.HookTaskStatusDead, task.Status)
	assert.Equal(t, 2, task.Retries)
	assert.Zero(t, task.NextRetryAt)
	assert.Equal(t, int32(3), hits.Load())
}

func TestWebhookRetrySucceedsAfterFailure(t *testing.T) {
	assert.NoError(t, unittest.PrepareTestDatabase())
	enableRetrySettings(t, 3)

	var hits atomic.Int32
	srv := statusHTTPServer(t, func() int {
		if hits.Add(1) == 1 {
			return http.StatusBadGateway
		}
		return http.StatusOK
	})
	hook := newRetryTestWebhook(t, srv.URL+"/hook", true)
	task := newRetryTestTask(t, hook)

	require.NoError(t, Deliver(t.Context(), task))
	task = reloadRetryTask(t, task.ID)
	assert.Equal(t, webhook_model.HookTaskStatusWaiting, task.Status)
	assert.Equal(t, 1, task.Retries)

	forceRetryDue(t, task.ID)
	task = reloadRetryTask(t, task.ID)
	require.NoError(t, Deliver(t.Context(), task))
	task = reloadRetryTask(t, task.ID)
	assert.True(t, task.IsSucceed)
	assert.Equal(t, webhook_model.HookTaskStatusSucceed, task.Status)
	assert.Empty(t, task.LastError)
	assert.Zero(t, task.NextRetryAt)
	assert.Equal(t, 1, task.Retries, "the automatic counter is preserved on success")
}

func TestWebhookRetryStatusClassification(t *testing.T) {
	assert.NoError(t, unittest.PrepareTestDatabase())
	enableRetrySettings(t, 3)

	// explicit rejection: no retry
	srv404 := statusHTTPServer(t, func() int { return http.StatusNotFound })
	task404 := newRetryTestTask(t, newRetryTestWebhook(t, srv404.URL+"/hook", true))
	require.NoError(t, Deliver(t.Context(), task404))
	task404 = reloadRetryTask(t, task404.ID)
	assert.Equal(t, webhook_model.HookTaskStatusFailed, task404.Status)
	assert.Zero(t, task404.Retries)
	assert.Contains(t, task404.LastError, "404")

	// rate limited: scheduled for retry
	srv429 := statusHTTPServer(t, func() int { return http.StatusTooManyRequests })
	task429 := newRetryTestTask(t, newRetryTestWebhook(t, srv429.URL+"/hook", true))
	require.NoError(t, Deliver(t.Context(), task429))
	task429 = reloadRetryTask(t, task429.ID)
	assert.Equal(t, webhook_model.HookTaskStatusWaiting, task429.Status)
	assert.Equal(t, 1, task429.Retries)
}

func TestWebhookRetryTransportError(t *testing.T) {
	assert.NoError(t, unittest.PrepareTestDatabase())
	enableRetrySettings(t, 2)

	// a server that existed but is closed now refuses the connection
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	srvURL := srv.URL
	srv.Close()
	hook := newRetryTestWebhook(t, srvURL+"/hook", true)
	task := newRetryTestTask(t, hook)

	require.Error(t, Deliver(t.Context(), task))
	task = reloadRetryTask(t, task.ID)
	assert.Equal(t, webhook_model.HookTaskStatusWaiting, task.Status)
	assert.Equal(t, 1, task.Retries)
	assert.NotEmpty(t, task.LastError)
}

func TestWebhookRetryInactiveWebhook(t *testing.T) {
	assert.NoError(t, unittest.PrepareTestDatabase())
	enableRetrySettings(t, 2)

	var hits atomic.Int32
	srv := statusHTTPServer(t, func() int { hits.Add(1); return http.StatusOK })
	hook := newRetryTestWebhook(t, srv.URL+"/hook", false)
	task := newRetryTestTask(t, hook)

	require.NoError(t, Deliver(t.Context(), task))
	task = reloadRetryTask(t, task.ID)
	assert.Equal(t, webhook_model.HookTaskStatusFailed, task.Status)
	assert.Zero(t, task.Retries)
	assert.Equal(t, "webhook is inactive", task.LastError)
	assert.Zero(t, hits.Load())
}

func TestWebhookRetrySameRequestAndSignature(t *testing.T) {
	assert.NoError(t, unittest.PrepareTestDatabase())
	enableRetrySettings(t, 2)

	var mu sync.Mutex
	bodies, signatures, deliveries := make([]string, 0, 2), make([]string, 0, 2), make([]string, 0, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(body))
		signatures = append(signatures, r.Header.Get("X-Gitea-Signature"))
		deliveries = append(deliveries, r.Header.Get("X-Gitea-Delivery"))
		mu.Unlock()
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)

	hook := &webhook_model.Webhook{
		RepoID:      3,
		URL:         srv.URL + "/hook",
		ContentType: webhook_model.ContentTypeJSON,
		IsActive:    true,
		Type:        webhook_module.GITEA,
		Secret:      "test-secret",
	}
	require.NoError(t, webhook_model.CreateWebhook(t.Context(), hook))
	task := newRetryTestTask(t, hook)

	require.NoError(t, Deliver(t.Context(), task))
	forceRetryDue(t, task.ID)
	task = reloadRetryTask(t, task.ID)
	require.NoError(t, Deliver(t.Context(), task))

	require.Len(t, bodies, 2)
	assert.Equal(t, bodies[0], bodies[1])
	assert.Equal(t, signatures[0], signatures[1])
	assert.NotEmpty(t, signatures[0])
	assert.Equal(t, task.UUID, deliveries[0])
	assert.Equal(t, deliveries[0], deliveries[1])
}

func TestWebhookRetryConcurrentClaimSendsOnce(t *testing.T) {
	assert.NoError(t, unittest.PrepareTestDatabase())
	enableRetrySettings(t, 5)

	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		entered <- struct{}{}
		<-release
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	hook := newRetryTestWebhook(t, srv.URL+"/hook", true)
	task := newRetryTestTask(t, hook)

	var wg sync.WaitGroup
	wg.Go(func() {
		_ = Deliver(t.Context(), task)
	})

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first delivery never reached the server")
	}

	// a second worker loading the row mid-flight sees a fresh sending state and must not send
	competitor := reloadRetryTask(t, task.ID)
	assert.NoError(t, Deliver(t.Context(), competitor))

	close(release)
	wg.Wait()
	assert.Equal(t, int32(1), hits.Load())
	final := reloadRetryTask(t, task.ID)
	assert.Equal(t, webhook_model.HookTaskStatusSucceed, final.Status)
}

func TestRequeueDeadTasks(t *testing.T) {
	assert.NoError(t, unittest.PrepareTestDatabase())
	enableRetrySettings(t, 3)

	srv := statusHTTPServer(t, func() int { return http.StatusOK })
	hook := newRetryTestWebhook(t, srv.URL+"/hook", true)
	task := newRetryTestTask(t, hook)
	task.IsDelivered = true
	task.Status = webhook_model.HookTaskStatusDead
	task.Retries = 3
	task.Delivered = timeutil.TimeStampNanoNow() - 1000
	task.LastError = "received non-successful HTTP status 500"
	require.NoError(t, webhook_model.UpdateHookTask(t.Context(), task))

	n, err := RequeueDeadTasks(t.Context(), hook.ID, optional.None[webhook_module.HookEventType](), 0, 0)
	require.NoError(t, err)
	assert.EqualValues(t, 1, n)

	// idempotent: the row is no longer dead
	n, err = RequeueDeadTasks(t.Context(), hook.ID, optional.None[webhook_module.HookEventType](), 0, 0)
	require.NoError(t, err)
	assert.Zero(t, n)

	task = reloadRetryTask(t, task.ID)
	assert.Equal(t, webhook_model.HookTaskStatusWaiting, task.Status)
	require.NoError(t, Deliver(t.Context(), task))
	task = reloadRetryTask(t, task.ID)
	assert.Equal(t, webhook_model.HookTaskStatusSucceed, task.Status)
	assert.Equal(t, 3, task.Retries, "operator redelivery keeps the automatic counter")

	// a missing webhook surfaces ErrNotExist
	_, err = RequeueDeadTasks(t.Context(), 99999999, optional.None[webhook_module.HookEventType](), 0, 0)
	assert.ErrorIs(t, err, util.ErrNotExist)
}

func TestWebhookRetryThroughQueue(t *testing.T) {
	assert.NoError(t, unittest.PrepareTestDatabase())
	enableRetrySettings(t, 2)

	got := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		got <- struct{}{}
	}))
	t.Cleanup(srv.Close)

	hook := newRetryTestWebhook(t, srv.URL+"/hook", true)
	task := newRetryTestTask(t, hook)
	require.NoError(t, Deliver(t.Context(), task))
	task = reloadRetryTask(t, task.ID)
	assert.Equal(t, webhook_model.HookTaskStatusWaiting, task.Status)

	// when the backoff is due, the task flows through the real queue and handler
	forceRetryDue(t, task.ID)
	require.NoError(t, enqueueHookTask(task.ID))
	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("the queued retry never reached the server")
	}

	// the worker finishes its update asynchronously, wait for the terminal state
	require.Eventually(t, func() bool {
		final := reloadRetryTask(t, task.ID)
		return final.Status == webhook_model.HookTaskStatusWaiting && final.Retries == 2
	}, 5*time.Second, 50*time.Millisecond)
}
