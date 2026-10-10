// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package queue

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"gitea.dev/modules/setting"

	"github.com/prometheus/client_golang/prometheus"
)

var (
	// ErrQueueFull means the push failed because the queue had no free capacity within the block time.
	ErrQueueFull = errors.New("queue is full")
	// ErrQueueBackendUnavailable means the push failed because the queue storage backend returned an error.
	ErrQueueBackendUnavailable = errors.New("queue backend is unavailable")
)

const (
	lastErrorKindProcessing = "processing"
	lastErrorKindBackend    = "backend"
	lastErrorKindDropped    = "dropped"
	lastErrorKindRejected   = "rejected"

	degradeCauseBackend = "backend"
	degradeCauseFailure = "failure"

	lastErrorSummaryMaxLength = 200
	// compact the window slice after this many stale samples slid out, to avoid retaining a huge backing array
	windowCompactAt = 128
)

// LastErrorInfo is the most recent failure observed on a queue.
type LastErrorInfo struct {
	Kind    string
	Summary string
	Time    time.Time
}

// MetricsSnapshot is a point-in-time copy of a queue's metrics, for the admin page and the collector.
type MetricsSnapshot struct {
	Enabled bool
	StartAt time.Time // stats start from zero at process (re)start

	Enqueued      int64
	Processed     int64
	Failed        int64
	Dropped       int64
	Rejected      int64
	BackendErrors int64

	Degraded      bool
	DegradedSince time.Time
	RecoveredAt   time.Time
	DegradeCause  string

	LastError *LastErrorInfo
}

type breakerSample struct {
	at     int64 // unix nano
	failed bool
	cause  string // degradeCauseBackend or degradeCauseFailure
}

// queueMetrics holds per-queue in-process counters and the sliding-window degradation state.
// All counters only increase; zero value means metrics disabled, all methods are no-ops in that case.
type queueMetrics struct {
	enabled      bool
	startAt      time.Time
	window       time.Duration
	threshold    float64
	minSamples   int
	pushBehavior string

	enqueued      atomic.Int64
	processed     atomic.Int64
	failed        atomic.Int64
	dropped       atomic.Int64
	rejected      atomic.Int64
	backendErrors atomic.Int64

	mu            sync.Mutex
	samples       []breakerSample
	totalSamples  int
	badSamples    int
	badBackends   int
	degraded      bool
	degradedSince time.Time
	recoveredAt   time.Time
	degradeCause  string
	lastError     *LastErrorInfo
}

func newQueueMetrics(cfg *BaseConfig) *queueMetrics {
	return &queueMetrics{
		enabled:      cfg.MetricsEnabled,
		startAt:      time.Now(),
		window:       cfg.DegradeWindow,
		threshold:    cfg.DegradeFailureThreshold,
		minSamples:   cfg.DegradeMinSamples,
		pushBehavior: cfg.DegradePushBehavior,
	}
}

func (m *queueMetrics) recordEnqueued() {
	if !m.enabled {
		return
	}
	m.enqueued.Add(1)
	m.addSamples(1, false, "")
}

// recordProcessed counts the per-item results of one handler batch.
func (m *queueMetrics) recordProcessed(succeeded, failed int) {
	if !m.enabled {
		return
	}
	if succeeded > 0 {
		m.processed.Add(int64(succeeded))
	}
	if failed > 0 {
		m.failed.Add(int64(failed))
		m.setLastError(lastErrorKindProcessing, fmt.Sprintf("%d of %d item(s) returned unhandled by handler", failed, succeeded+failed))
	}
	m.addSamples(succeeded, false, "")
	m.addSamples(failed, true, degradeCauseFailure)
}

// recordHandlerPanic counts a whole batch as failed when its handler panicked.
func (m *queueMetrics) recordHandlerPanic(batchSize int, panicValue any) {
	if !m.enabled || batchSize <= 0 {
		return
	}
	m.failed.Add(int64(batchSize))
	m.setLastError(lastErrorKindProcessing, fmt.Sprintf("handler panicked: %v", panicValue))
	m.addSamples(batchSize, true, degradeCauseFailure)
}

func (m *queueMetrics) recordDropped(err error) {
	if !m.enabled {
		return
	}
	m.dropped.Add(1)
	m.setLastError(lastErrorKindDropped, fmt.Sprintf("undecodable item discarded: %v", err))
}

// recordRejected counts a push the queue refused without accepting the item:
// it was full, or it fast-failed while degraded. Rejections are not breaker samples,
// so caller retry storms can never keep a recovered queue in the degraded state.
func (m *queueMetrics) recordRejected(backendUnavailable bool) {
	if !m.enabled {
		return
	}
	m.rejected.Add(1)
	if backendUnavailable {
		m.setLastError(lastErrorKindRejected, "push fast-failed while the queue is degraded by backend errors")
	} else {
		m.setLastError(lastErrorKindRejected, "push rejected because the queue is full")
	}
}

// recordBackendError counts an actual error observed while talking to the queue backend.
func (m *queueMetrics) recordBackendError(err error) {
	if !m.enabled || err == nil {
		return
	}
	m.backendErrors.Add(1)
	m.setLastError(lastErrorKindBackend, fmt.Sprintf("queue backend error: %v", err))
	m.addSamples(1, true, degradeCauseBackend)
}

func (m *queueMetrics) setLastError(kind, summary string) {
	if len(summary) > lastErrorSummaryMaxLength {
		summary = summary[:lastErrorSummaryMaxLength] + "..."
	}
	m.mu.Lock()
	m.lastError = &LastErrorInfo{Kind: kind, Summary: summary, Time: time.Now()}
	m.mu.Unlock()
}

func (m *queueMetrics) addSamples(n int, failed bool, cause string) {
	if n <= 0 {
		return
	}
	now := time.Now()
	m.mu.Lock()
	nowNano := now.UnixNano()
	for range n {
		m.samples = append(m.samples, breakerSample{at: nowNano, failed: failed, cause: cause})
	}
	m.totalSamples += n
	if failed {
		m.badSamples += n
		if cause == degradeCauseBackend {
			m.badBackends += n
		}
	}
	m.evaluateLocked(now)
	m.mu.Unlock()
}

// evaluate prunes samples outside the sliding window and updates the degraded state.
func (m *queueMetrics) evaluate() {
	if !m.enabled {
		return
	}
	m.mu.Lock()
	m.evaluateLocked(time.Now())
	m.mu.Unlock()
}

// evaluateLocked must be called with m.mu held. Each sample is pruned at most once as the window slides.
func (m *queueMetrics) evaluateLocked(now time.Time) {
	cutoff := now.Add(-m.window).UnixNano()
	stale := 0
	for stale < len(m.samples) && m.samples[stale].at < cutoff {
		s := m.samples[stale]
		m.totalSamples--
		if s.failed {
			m.badSamples--
			if s.cause == degradeCauseBackend {
				m.badBackends--
			}
		}
		stale++
	}
	if stale > 0 {
		if stale >= windowCompactAt {
			m.samples = append([]breakerSample(nil), m.samples[stale:]...)
		} else {
			m.samples = m.samples[stale:]
		}
	}

	if !m.degraded {
		// no judgment while the window has too few samples, so a healthy idle queue never gets false alarms
		if m.totalSamples >= m.minSamples && float64(m.badSamples)/float64(m.totalSamples) > m.threshold {
			m.degraded = true
			m.degradedSince = now
			if m.badBackends > 0 {
				m.degradeCause = degradeCauseBackend
			} else {
				m.degradeCause = degradeCauseFailure
			}
		}
	} else if m.badSamples == 0 {
		// auto recover once no failure/backend-error remains in the window
		m.degraded = false
		m.recoveredAt = now
		m.degradeCause = ""
		m.degradedSince = time.Time{}
	}
}

func (m *queueMetrics) isDegraded() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.degraded
}

// fastFailError returns the error a degraded push should fast-fail with, based on what tripped the breaker.
func (m *queueMetrics) fastFailError() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.degradeCause == degradeCauseBackend {
		return ErrQueueBackendUnavailable
	}
	return ErrQueueFull
}

func (m *queueMetrics) snapshot() MetricsSnapshot {
	s := MetricsSnapshot{
		Enabled:       m.enabled,
		StartAt:       m.startAt,
		Enqueued:      m.enqueued.Load(),
		Processed:     m.processed.Load(),
		Failed:        m.failed.Load(),
		Dropped:       m.dropped.Load(),
		Rejected:      m.rejected.Load(),
		BackendErrors: m.backendErrors.Load(),
	}
	m.mu.Lock()
	s.Degraded = m.degraded
	s.DegradedSince = m.degradedSince
	s.RecoveredAt = m.recoveredAt
	s.DegradeCause = m.degradeCause
	if m.lastError != nil {
		lastErr := *m.lastError
		s.LastError = &lastErr
	}
	m.mu.Unlock()
	return s
}

// queueCollector exposes the per-queue metrics to prometheus.
type queueCollector struct {
	managedQueues func() map[int64]ManagedWorkerPoolQueue

	queueLength    *prometheus.Desc
	workersActive  *prometheus.Desc
	workersMax     *prometheus.Desc
	degraded       *prometheus.Desc
	enqueued       *prometheus.Desc
	processed      *prometheus.Desc
	failed         *prometheus.Desc
	dropped        *prometheus.Desc
	rejected       *prometheus.Desc
	backendErrors  *prometheus.Desc
	statsStartTime *prometheus.Desc
}

func newQueueCollector() *queueCollector {
	return newQueueCollectorFor(GetManager().ManagedQueues)
}

func newQueueCollectorFor(managedQueues func() map[int64]ManagedWorkerPoolQueue) *queueCollector {
	labels := []string{"queue_name", "queue_type"}
	return &queueCollector{
		managedQueues:  managedQueues,
		queueLength:    prometheus.NewDesc("gitea_queue_length", "Current number of items waiting in the queue", labels, nil),
		workersActive:  prometheus.NewDesc("gitea_queue_workers_active", "Number of currently active workers (in-flight)", labels, nil),
		workersMax:     prometheus.NewDesc("gitea_queue_workers_max", "Configured maximum number of workers", labels, nil),
		degraded:       prometheus.NewDesc("gitea_queue_degraded", "1 if the queue is degraded within the judgment window, 0 otherwise", labels, nil),
		enqueued:       prometheus.NewDesc("gitea_queue_enqueued_total", "Total number of items accepted into the queue since process start", labels, nil),
		processed:      prometheus.NewDesc("gitea_queue_processed_total", "Total number of items processed successfully since process start", labels, nil),
		failed:         prometheus.NewDesc("gitea_queue_failed_total", "Total number of items whose handler returned them unhandled since process start", labels, nil),
		dropped:        prometheus.NewDesc("gitea_queue_dropped_total", "Total number of undecodable items discarded since process start", labels, nil),
		rejected:       prometheus.NewDesc("gitea_queue_rejected_total", "Total number of pushes refused (queue full or degraded fast-fail) since process start", labels, nil),
		backendErrors:  prometheus.NewDesc("gitea_queue_backend_errors_total", "Total number of queue backend errors since process start", labels, nil),
		statsStartTime: prometheus.NewDesc("gitea_queue_stats_start_timestamp_seconds", "Unix timestamp when the queue metrics started from zero", labels, nil),
	}
}

func (c *queueCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.queueLength
	ch <- c.workersActive
	ch <- c.workersMax
	ch <- c.degraded
	ch <- c.enqueued
	ch <- c.processed
	ch <- c.failed
	ch <- c.dropped
	ch <- c.rejected
	ch <- c.backendErrors
	ch <- c.statsStartTime
}

func (c *queueCollector) Collect(ch chan<- prometheus.Metric) {
	for _, mq := range c.managedQueues() {
		s := mq.GetQueueMetrics()
		if !s.Enabled {
			continue
		}
		name, typ := mq.GetName(), mq.GetType()
		degradedValue := 0.0
		if s.Degraded {
			degradedValue = 1
		}
		ch <- prometheus.MustNewConstMetric(c.queueLength, prometheus.GaugeValue, float64(mq.GetQueueItemNumber()), name, typ)
		ch <- prometheus.MustNewConstMetric(c.workersActive, prometheus.GaugeValue, float64(mq.GetWorkerActiveNumber()), name, typ)
		ch <- prometheus.MustNewConstMetric(c.workersMax, prometheus.GaugeValue, float64(mq.GetWorkerMaxNumber()), name, typ)
		ch <- prometheus.MustNewConstMetric(c.degraded, prometheus.GaugeValue, degradedValue, name, typ)
		ch <- prometheus.MustNewConstMetric(c.enqueued, prometheus.CounterValue, float64(s.Enqueued), name, typ)
		ch <- prometheus.MustNewConstMetric(c.processed, prometheus.CounterValue, float64(s.Processed), name, typ)
		ch <- prometheus.MustNewConstMetric(c.failed, prometheus.CounterValue, float64(s.Failed), name, typ)
		ch <- prometheus.MustNewConstMetric(c.dropped, prometheus.CounterValue, float64(s.Dropped), name, typ)
		ch <- prometheus.MustNewConstMetric(c.rejected, prometheus.CounterValue, float64(s.Rejected), name, typ)
		ch <- prometheus.MustNewConstMetric(c.backendErrors, prometheus.CounterValue, float64(s.BackendErrors), name, typ)
		ch <- prometheus.MustNewConstMetric(c.statsStartTime, prometheus.GaugeValue, float64(s.StartAt.Unix()), name, typ)
	}
}

var registerQueueMetricsOnce sync.Once

// RegisterMetricsCollector registers the queue prometheus collector. It is a no-op when queue metrics are disabled.
func RegisterMetricsCollector() {
	if !setting.QueueMetricsEnabled {
		return
	}
	registerQueueMetricsOnce.Do(func() {
		prometheus.MustRegister(newQueueCollector())
	})
}
