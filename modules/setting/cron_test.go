// Copyright 2020 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package setting

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func Test_getCronSettings(t *testing.T) {
	type BaseStruct struct {
		Base   bool
		Second string
	}

	type Extended struct {
		BaseStruct
		Extend bool
	}

	iniStr := `
[cron.test]
BASE = true
SECOND = white rabbit
EXTEND = true
`
	cfg, err := NewConfigProviderFromData(iniStr)
	assert.NoError(t, err)

	extended := &Extended{
		BaseStruct: BaseStruct{
			Second: "queen of hearts",
		},
	}

	_, err = getCronSettings(cfg, "test", extended)
	assert.NoError(t, err)
	assert.True(t, extended.Base)
	assert.Equal(t, "white rabbit", extended.Second)
	assert.True(t, extended.Extend)
}

// Test_getCronSettings2 tests that getCronSettings can not handle two levels of embedding
func Test_getCronSettings2(t *testing.T) {
	type BaseStruct struct {
		Enabled    bool
		RunAtStart bool
		Schedule   string
	}

	type Extended struct {
		BaseStruct
		Extend bool
	}
	type Extended2 struct {
		Extended
		Third string
	}

	iniStr := `
[cron.test]
ENABLED = TRUE
RUN_AT_START = TRUE
SCHEDULE = @every 1h
EXTEND = true
THIRD = white rabbit
`
	cfg, err := NewConfigProviderFromData(iniStr)
	assert.NoError(t, err)

	extended := &Extended2{
		Extended: Extended{
			BaseStruct: BaseStruct{
				Enabled:    false,
				RunAtStart: false,
				Schedule:   "@every 72h",
			},
			Extend: false,
		},
		Third: "black rabbit",
	}

	_, err = getCronSettings(cfg, "test", extended)
	assert.NoError(t, err)

	// This confirms the first level of embedding works
	assert.Equal(t, "white rabbit", extended.Third)
	assert.True(t, extended.Extend)

	// This confirms 2 levels of embedding doesn't work
	assert.False(t, extended.Enabled)
	assert.False(t, extended.RunAtStart)
	assert.Equal(t, "@every 72h", extended.Schedule)
}

func Test_loadCronFrom(t *testing.T) {
	reset := func() {
		Cron = struct {
			EnableExecutionHistory   bool
			HistoryRetentionCount    int64
			HistoryRetentionDuration time.Duration
			OverlapPolicy            string
			RunTimeout               time.Duration
		}{}
	}

	t.Run("defaults", func(t *testing.T) {
		reset()
		cfg, err := NewConfigProviderFromData(``)
		assert.NoError(t, err)
		assert.NoError(t, loadCronFrom(cfg))
		assert.False(t, Cron.EnableExecutionHistory)
		assert.EqualValues(t, 100, Cron.HistoryRetentionCount)
		assert.Equal(t, time.Duration(0), Cron.HistoryRetentionDuration)
		assert.Equal(t, CronOverlapPolicySkip, Cron.OverlapPolicy)
		assert.Equal(t, time.Duration(0), Cron.RunTimeout)
	})

	t.Run("valid custom values", func(t *testing.T) {
		reset()
		cfg, err := NewConfigProviderFromData(`
[cron]
ENABLE_EXECUTION_HISTORY = true
HISTORY_RETENTION_COUNT = 20
HISTORY_RETENTION_DURATION = 720h
OVERLAP_POLICY = queue
RUN_TIMEOUT = 30
`)
		assert.NoError(t, err)
		assert.NoError(t, loadCronFrom(cfg))
		assert.True(t, Cron.EnableExecutionHistory)
		assert.EqualValues(t, 20, Cron.HistoryRetentionCount)
		assert.Equal(t, 720*time.Hour, Cron.HistoryRetentionDuration)
		assert.Equal(t, CronOverlapPolicyQueue, Cron.OverlapPolicy)
		assert.Equal(t, 30*time.Second, Cron.RunTimeout) // bare integer means seconds
	})

	t.Run("count 0 is allowed with duration set", func(t *testing.T) {
		reset()
		cfg, err := NewConfigProviderFromData(`
[cron]
ENABLE_EXECUTION_HISTORY = true
HISTORY_RETENTION_COUNT = 0
HISTORY_RETENTION_DURATION = 168h
OVERLAP_POLICY = reject
`)
		assert.NoError(t, err)
		assert.NoError(t, loadCronFrom(cfg))
		assert.EqualValues(t, 0, Cron.HistoryRetentionCount)
		assert.Equal(t, CronOverlapPolicyReject, Cron.OverlapPolicy)
	})

	cases := []struct {
		name string
		ini  string
	}{
		{
			"invalid policy",
			"[cron]\nOVERLAP_POLICY = wait",
		},
		{
			"negative count",
			"[cron]\nHISTORY_RETENTION_COUNT = -1",
		},
		{
			"invalid duration",
			"[cron]\nHISTORY_RETENTION_DURATION = soon",
		},
		{
			"negative run timeout",
			"[cron]\nRUN_TIMEOUT = -5s",
		},
		{
			"history enabled without any retention limit",
			"[cron]\nENABLE_EXECUTION_HISTORY = true\nHISTORY_RETENTION_COUNT = 0",
		},
	}
	for _, c := range cases {
		t.Run("invalid: "+c.name, func(t *testing.T) {
			reset()
			cfg, err := NewConfigProviderFromData(c.ini)
			assert.NoError(t, err)
			assert.ErrorContains(t, loadCronFrom(cfg), "loadCronFrom:")
		})
	}
}
