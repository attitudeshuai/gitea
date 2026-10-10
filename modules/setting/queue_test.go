// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package setting

import (
	"testing"
	"time"

	"gitea.dev/modules/test"

	"github.com/stretchr/testify/assert"
)

func TestQueueRedisSharedConnFallback(t *testing.T) {
	tests := []struct {
		name      string
		iniStr    string
		queueName string
		wantConn  string
	}{
		{
			name: "redis queue with empty CONN_STR falls back to shared [redis]",
			iniStr: `
[redis]
CONN_STR = redis://127.0.0.1:6379/0
[queue]
TYPE = redis
`,
			queueName: "test",
			wantConn:  "redis://127.0.0.1:6379/0",
		},
		{
			name: "base [queue] CONN_STR wins over shared [redis]",
			iniStr: `
[redis]
CONN_STR = redis://127.0.0.1:6379/0
[queue]
TYPE = redis
CONN_STR = redis://10.0.0.1:6379/1
`,
			queueName: "test",
			wantConn:  "redis://10.0.0.1:6379/1",
		},
		{
			name: "per-queue CONN_STR wins over shared [redis]",
			iniStr: `
[redis]
CONN_STR = redis://127.0.0.1:6379/0
[queue]
TYPE = redis
[queue.test]
CONN_STR = redis://10.0.0.1:6379/2
`,
			queueName: "test",
			wantConn:  "redis://10.0.0.1:6379/2",
		},
		{
			name: "other queues still fall back to shared [redis] when only one is overridden",
			iniStr: `
[redis]
CONN_STR = redis://127.0.0.1:6379/0
[queue]
TYPE = redis
[queue.test]
CONN_STR = redis://10.0.0.1:6379/2
`,
			queueName: "other",
			wantConn:  "redis://127.0.0.1:6379/0",
		},
		{
			name: "no shared [redis] keeps previous behavior (built-in localhost default)",
			iniStr: `
[queue]
TYPE = redis
`,
			queueName: "test",
			wantConn:  "redis://127.0.0.1:6379/0",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer test.MockVariableValue(&Redis)()
			cfg, err := NewConfigProviderFromData(tt.iniStr)
			assert.NoError(t, err)

			loadRedisFrom(cfg)
			q, err := GetQueueSettings(cfg, tt.queueName)
			assert.NoError(t, err)
			assert.Equal(t, tt.wantConn, q.ConnStr)
		})
	}
}

func TestQueueMetricsSettingsValidation(t *testing.T) {
	tests := []struct {
		name    string
		iniStr  string
		wantErr string
	}{
		{name: "empty config is valid", iniStr: ``},
		{name: "valid full config", iniStr: `
[queue]
METRICS_ENABLED = true
DEGRADE_WINDOW = 5m
DEGRADE_FAILURE_THRESHOLD = 0.8
DEGRADE_MIN_SAMPLES = 7
DEGRADE_PUSH_BEHAVIOR = block
`},
		{name: "valid per-queue config", iniStr: `
[queue.test]
METRICS_ENABLED = false
DEGRADE_WINDOW = 30s
DEGRADE_FAILURE_THRESHOLD = 0.1
DEGRADE_MIN_SAMPLES = 1
DEGRADE_PUSH_BEHAVIOR = fast-fail
`},
		{name: "invalid bool", iniStr: `
[queue]
METRICS_ENABLED = maybe
`, wantErr: "[queue].METRICS_ENABLED"},
		{name: "invalid window", iniStr: `
[queue]
DEGRADE_WINDOW = soon
`, wantErr: "[queue].DEGRADE_WINDOW"},
		{name: "zero window", iniStr: `
[queue.foo]
DEGRADE_WINDOW = 0
`, wantErr: "[queue.foo].DEGRADE_WINDOW"},
		{name: "invalid threshold", iniStr: `
[queue]
DEGRADE_FAILURE_THRESHOLD = 1.5
`, wantErr: "[queue].DEGRADE_FAILURE_THRESHOLD"},
		{name: "invalid min samples", iniStr: `
[queue.bar]
DEGRADE_MIN_SAMPLES = -2
`, wantErr: "[queue.bar].DEGRADE_MIN_SAMPLES"},
		{name: "invalid push behavior", iniStr: `
[queue]
DEGRADE_PUSH_BEHAVIOR = drop
`, wantErr: "[queue].DEGRADE_PUSH_BEHAVIOR"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := NewConfigProviderFromData(tt.iniStr)
			assert.NoError(t, err)
			err = validateQueueConfigSections(cfg)
			if tt.wantErr == "" {
				assert.NoError(t, err)
			} else {
				assert.ErrorContains(t, err, tt.wantErr)
			}
		})
	}
}

func TestQueueMetricsSettingsDefaultsAndOverrides(t *testing.T) {
	cfg, err := NewConfigProviderFromData(``)
	assert.NoError(t, err)
	q, err := GetQueueSettings(cfg, "test")
	assert.NoError(t, err)
	assert.False(t, q.MetricsEnabled)
	assert.Equal(t, queueDegradeWindowDefault, q.DegradeWindow)
	assert.InEpsilon(t, queueDegradeFailureThresholdDefault, q.DegradeFailureThreshold, 1e-9)
	assert.Equal(t, queueDegradeMinSamplesDefault, q.DegradeMinSamples)
	assert.Equal(t, queueDegradePushBehaviorDefault, q.DegradePushBehavior)

	cfg, err = NewConfigProviderFromData(`
[queue]
METRICS_ENABLED = true
DEGRADE_WINDOW = 2m
DEGRADE_FAILURE_THRESHOLD = 0.9
DEGRADE_MIN_SAMPLES = 11
DEGRADE_PUSH_BEHAVIOR = block
`)
	assert.NoError(t, err)
	q, err = GetQueueSettings(cfg, "test")
	assert.NoError(t, err)
	assert.True(t, q.MetricsEnabled)
	assert.Equal(t, 2*time.Minute, q.DegradeWindow)
	assert.InEpsilon(t, 0.9, q.DegradeFailureThreshold, 1e-9)
	assert.Equal(t, 11, q.DegradeMinSamples)
	assert.Equal(t, "block", q.DegradePushBehavior)
}
