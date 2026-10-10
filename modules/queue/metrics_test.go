// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package queue

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"gitea.dev/modules/setting"
	"gitea.dev/modules/test"

	"github.com/prometheus/client_golang/prometheus"
	promtestutil "github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
)

func metricsQueueSettings() setting.QueueSettings {
	return setting.QueueSettings{
		Type: "channel", Datadir: "unused", Length: 100, BatchLength: 1, MaxWorkers: 1,
		MetricsEnabled:          true,
		DegradeWindow:           time.Minute,
		DegradeFailureThreshold: 0.5,
		DegradeMinSamples:       20,
		DegradePushBehavior:     DegradedPushFastFail,
	}
}

func waitForCondition(t *testing.T, timeout time.Duration, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("condition not met within %v", timeout)
}

func TestQueueMetricsDisabledKeepsCurrentBehavior(t *testing.T) {
	defer test.MockVariableValue(&pushBlockTime, 30*time.Millisecond)()

	qs := metricsQueueSettings()
	qs.MetricsEnabled = false
	qs.Length = 1
	q, err := newWorkerPoolQueueForTest[int]("test-metrics-disabled", qs, func(items ...int) []int { return nil }, false)
	assert.NoError(t, err)

	s := q.GetQueueMetrics()
	assert.False(t, s.Enabled)

	assert.NoError(t, q.Push(1))
	err = q.Push(2) // channel capacity is 1, nobody consumes
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.NotErrorIs(t, err, ErrQueueFull)
}

func TestQueueMetricsPushFullClassifiedAndCounted(t *testing.T) {
	defer test.MockVariableValue(&pushBlockTime, 30*time.Millisecond)()

	qs := metricsQueueSettings()
	qs.Length = 1
	q, err := newWorkerPoolQueueForTest[int]("test-metrics-full", qs, func(items ...int) []int { return nil }, false)
	assert.NoError(t, err)

	assert.NoError(t, q.Push(1))
	err = q.Push(2)
	assert.ErrorIs(t, err, ErrQueueFull)

	s := q.GetQueueMetrics()
	assert.Equal(t, int64(1), s.Enqueued)
	assert.Equal(t, int64(1), s.Rejected)
	assert.Equal(t, int64(0), s.BackendErrors)
	assert.False(t, s.Degraded)
	assert.NotNil(t, s.LastError)
	assert.Equal(t, lastErrorKindRejected, s.LastError.Kind)
}

func TestQueueMetricsProcessedCounters(t *testing.T) {
	oldUnhandled := unhandledItemRequeueDuration.Load()
	unhandledItemRequeueDuration.Store(0)
	defer unhandledItemRequeueDuration.Store(oldUnhandled)

	qs := metricsQueueSettings()
	qs.BatchLength = 2
	var failOnce atomic.Int32
	q, err := newWorkerPoolQueueForTest("test-metrics-processed", qs, func(items ...int) (unhandled []int) {
		for _, item := range items {
			if item == 1 && failOnce.Add(1) == 1 {
				unhandled = append(unhandled, item) // fails once, then succeeds after requeue
			}
		}
		return unhandled
	}, false)
	assert.NoError(t, err)

	stop := runWorkerPoolQueue(q)
	for i := range 4 {
		assert.NoError(t, q.Push(i))
	}
	assert.NoError(t, q.FlushWithContext(t.Context(), 0))
	stop()

	s := q.GetQueueMetrics()
	assert.Equal(t, int64(5), s.Enqueued)  // 4 pushes + 1 requeue
	assert.Equal(t, int64(4), s.Processed) // item 1 handled twice, one of them failed
	assert.Equal(t, int64(1), s.Failed)
	assert.Equal(t, int64(0), s.Dropped)
	assert.Equal(t, int64(0), s.Rejected)
	assert.Equal(t, int64(0), s.BackendErrors)
}

func TestQueueMetricsDroppedCorruptedItem(t *testing.T) {
	// feed a corrupted raw entry straight into the base queue, bypassing JSON marshal
	qs := metricsQueueSettings()
	q, err := newWorkerPoolQueueForTest[int]("test-metrics-dropped", qs, func(items ...int) (unhandled []int) { return nil }, false)
	assert.NoError(t, err)
	assert.NoError(t, q.baseQueue.PushItem(context.Background(), []byte("{corrupted")))

	stop := runWorkerPoolQueue(q)
	assert.NoError(t, q.FlushWithContext(t.Context(), 0))
	stop()

	s := q.GetQueueMetrics()
	assert.Equal(t, int64(1), s.Dropped)
	assert.Equal(t, int64(0), s.Processed)
	assert.NotNil(t, s.LastError)
	assert.Equal(t, lastErrorKindDropped, s.LastError.Kind)
}

func TestQueueMetricsHandlerPanicCountsBatch(t *testing.T) {
	qs := metricsQueueSettings()
	var panicked atomic.Bool
	q, err := newWorkerPoolQueueForTest("test-metrics-panic", qs, func(items ...int) (unhandled []int) {
		if items[0] == 0 && !panicked.Swap(true) {
			panic("boom")
		}
		return nil
	}, false)
	assert.NoError(t, err)

	stop := runWorkerPoolQueue(q)
	assert.NoError(t, q.Push(0)) // panicked item is lost, same semantics as before
	assert.NoError(t, q.Push(1))
	assert.NoError(t, q.FlushWithContext(t.Context(), 0))
	stop()

	s := q.GetQueueMetrics()
	assert.Equal(t, int64(2), s.Enqueued)
	assert.Equal(t, int64(1), s.Processed)
	assert.Equal(t, int64(1), s.Failed)
	assert.Equal(t, lastErrorKindProcessing, s.LastError.Kind)
}

func TestQueueMetricsBreakerWindow(t *testing.T) {
	// not enough samples in the window: no judgment
	m := newQueueMetrics(&BaseConfig{
		MetricsEnabled: true, DegradeWindow: 300 * time.Millisecond,
		DegradeFailureThreshold: 0.5, DegradeMinSamples: 4, DegradePushBehavior: DegradedPushFastFail,
	})
	m.recordBackendError(errors.New("e1"))
	m.recordBackendError(errors.New("e2"))
	m.recordBackendError(errors.New("e3"))
	assert.False(t, m.snapshot().Degraded)

	// crossing the threshold with enough samples trips the breaker with backend cause
	m.recordBackendError(errors.New("e4"))
	s := m.snapshot()
	assert.True(t, s.Degraded)
	assert.Equal(t, degradeCauseBackend, s.DegradeCause)
	assert.False(t, s.DegradedSince.IsZero())
	assert.ErrorIs(t, m.fastFailError(), ErrQueueBackendUnavailable)

	// rejections (fast-fail refusals) must not feed the breaker window
	badSamples := m.badSamples
	m.recordRejected(true)
	m.recordRejected(false)
	assert.Equal(t, int64(2), m.snapshot().Rejected)
	assert.Equal(t, badSamples, m.badSamples)

	// after the window slides past all failures, the queue auto recovers
	time.Sleep(350 * time.Millisecond)
	m.evaluate()
	s = m.snapshot()
	assert.False(t, s.Degraded)
	assert.False(t, s.RecoveredAt.IsZero())
	assert.Equal(t, 0, m.totalSamples) // old failures were pruned, not counted twice

	// one new failure after recovery must not re-trip below min samples
	m.recordProcessed(0, 1)
	assert.False(t, m.snapshot().Degraded)

	// ratio strictly greater than threshold; handler failures trip with failure cause
	m2 := newQueueMetrics(&BaseConfig{
		MetricsEnabled: true, DegradeWindow: time.Minute,
		DegradeFailureThreshold: 0.5, DegradeMinSamples: 4, DegradePushBehavior: DegradedPushFastFail,
	})
	m2.recordProcessed(2, 2) // exactly 50%: still normal
	assert.False(t, m2.snapshot().Degraded)
	m2.recordProcessed(0, 1) // 3/5 = 60%: degraded
	s2 := m2.snapshot()
	assert.True(t, s2.Degraded)
	assert.Equal(t, degradeCauseFailure, s2.DegradeCause)
	assert.ErrorIs(t, m2.fastFailError(), ErrQueueFull)
}

func TestQueueMetricsDegradedFastFailAndRequeueBypass(t *testing.T) {
	oldUnhandled := unhandledItemRequeueDuration.Load()
	unhandledItemRequeueDuration.Store(0)
	defer unhandledItemRequeueDuration.Store(oldUnhandled)

	qs := metricsQueueSettings()
	qs.DegradeWindow = time.Minute
	qs.DegradeFailureThreshold = 0.01
	qs.DegradeMinSamples = 1
	q, err := newWorkerPoolQueueForTest("test-metrics-degraded", qs, func(items ...int) (unhandled []int) {
		return items // handler always fails: every processed item is a breaker failure sample
	}, false)
	assert.NoError(t, err)

	stop := runWorkerPoolQueue(q)
	assert.NoError(t, q.Push(1))
	waitForCondition(t, 2*time.Second, func() bool { return q.GetQueueMetrics().Degraded })

	// public pushes now fast-fail, classified (failure cause -> full), and counted as rejected
	err = q.Push(2)
	assert.ErrorIs(t, err, ErrQueueFull)
	assert.Positive(t, q.GetQueueMetrics().Rejected)

	// internal requeue bypasses the gate: already accepted items must not be dropped while degraded
	enqueuedBefore := q.GetQueueMetrics().Enqueued
	assert.NoError(t, q.pushItem(3, true))
	assert.Greater(t, q.GetQueueMetrics().Enqueued, enqueuedBefore)

	stop()

	// a queue configured to block keeps the old waiting push behavior while degraded
	qsBlock := metricsQueueSettings()
	qsBlock.DegradeFailureThreshold = 0.01
	qsBlock.DegradeMinSamples = 1
	qsBlock.DegradePushBehavior = DegradedPushBlock
	qb, err := newWorkerPoolQueueForTest("test-metrics-degraded-block", qsBlock, func(items ...int) (unhandled []int) {
		return items
	}, false)
	assert.NoError(t, err)
	stopb := runWorkerPoolQueue(qb)
	assert.NoError(t, qb.Push(1))
	waitForCondition(t, 2*time.Second, func() bool { return qb.GetQueueMetrics().Degraded })
	assert.NoError(t, qb.Push(2)) // channel has room, so blocking push succeeds
	stopb()
}

func TestBackoffOnErrorKeepsRetrying(t *testing.T) {
	var errorCallbacks, calls atomic.Int32
	wantErr := errors.New("temporary backend error")
	_, err := backoffCall(
		context.Background(),
		backoffOptions{
			begin: time.Millisecond, upper: time.Millisecond, end: time.After(time.Second),
			onError: func(error) { errorCallbacks.Add(1) },
		},
		func() (retry bool, _ any, err error) {
			if calls.Add(1) < 3 {
				return true, nil, wantErr
			}
			return false, nil, nil
		},
	)
	assert.NoError(t, err)
	assert.Equal(t, int32(2), errorCallbacks.Load())
	assert.Equal(t, int32(3), calls.Load())
}

func TestQueueMetricsCollector(t *testing.T) {
	qEnabled, err := newWorkerPoolQueueForTest[int]("test-metrics-collector-on", metricsQueueSettings(), nil, false)
	assert.NoError(t, err)
	qDisabled, err := newWorkerPoolQueueForTest[int]("test-metrics-collector-off", setting.QueueSettings{
		Type: "channel", Length: 10, BatchLength: 1, MaxWorkers: 1,
	}, nil, false)
	assert.NoError(t, err)

	reg := prometheus.NewRegistry()
	reg.MustRegister(newQueueCollectorFor(func() map[int64]ManagedWorkerPoolQueue {
		return map[int64]ManagedWorkerPoolQueue{1: qEnabled, 2: qDisabled}
	}))

	// only the metrics-enabled queue is exported
	gatherCount := func(name string) int {
		n, err := promtestutil.GatherAndCount(reg, name)
		assert.NoError(t, err)
		return n
	}
	assert.Equal(t, 1, gatherCount("gitea_queue_length"))
	assert.Equal(t, 1, gatherCount("gitea_queue_enqueued_total"))
	assert.Equal(t, 1, gatherCount("gitea_queue_degraded"))
	assert.Equal(t, 1, gatherCount("gitea_queue_stats_start_timestamp_seconds"))
}
