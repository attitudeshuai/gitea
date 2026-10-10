// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package setting

import (
	"testing"

	"gitea.dev/modules/test"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadAuditFrom(t *testing.T) {
	for _, tc := range []struct {
		name     string
		cfg      string
		expected AuditRecordOutput
	}{
		{name: "DisabledByDefault", cfg: "", expected: AuditRecordOutputDisabled},
		{name: "Database", cfg: "[audit]\nRECORD_OUTPUT = Database\n", expected: AuditRecordOutputDatabase},
		{name: "Empty", cfg: "[audit]\nRECORD_OUTPUT =\n", expected: AuditRecordOutputDisabled},
		{name: "Invalid", cfg: "[audit]\nRECORD_OUTPUT = nonsense\n", expected: AuditRecordOutputDisabled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defer test.MockVariableValue(&Audit)()

			cfg, err := NewConfigProviderFromData(tc.cfg)
			require.NoError(t, err)
			loadAuditFrom(cfg)

			assert.Equal(t, tc.expected, Audit.RecordOutput)
			assert.Equal(t, tc.expected != AuditRecordOutputDisabled, AuditRecordEnabled())

			// permission-deny defaults: disabled, sample rate 1
			assert.False(t, Audit.PermissionDenyEnabled)
			assert.InEpsilon(t, 1.0, Audit.PermissionDenySampleRate, 0)
		})
	}
}

func TestLoadPermissionDenyAudit(t *testing.T) {
	for _, tc := range []struct {
		name       string
		cfg        string
		enabled    bool
		sampleRate float64
	}{
		{name: "EnabledAll", cfg: "[audit]\nPERMISSION_DENY_ENABLED = true\nPERMISSION_DENY_SAMPLE_RATE = 1\n", enabled: true, sampleRate: 1},
		{name: "SampledHalf", cfg: "[audit]\nPERMISSION_DENY_ENABLED = true\nPERMISSION_DENY_SAMPLE_RATE = 0.5\n", enabled: true, sampleRate: 0.5},
		{name: "SampleZero", cfg: "[audit]\nPERMISSION_DENY_ENABLED = true\nPERMISSION_DENY_SAMPLE_RATE = 0\n", enabled: true, sampleRate: 0},
		{name: "RateClampedHigh", cfg: "[audit]\nPERMISSION_DENY_SAMPLE_RATE = 2\n", enabled: false, sampleRate: 1},
		{name: "RateClampedLow", cfg: "[audit]\nPERMISSION_DENY_SAMPLE_RATE = -1\n", enabled: false, sampleRate: 0},
		{name: "RateNaN", cfg: "[audit]\nPERMISSION_DENY_SAMPLE_RATE = NaN\n", enabled: false, sampleRate: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defer test.MockVariableValue(&Audit)()

			cfg, err := NewConfigProviderFromData(tc.cfg)
			require.NoError(t, err)
			loadAuditFrom(cfg)

			assert.Equal(t, tc.enabled, PermissionDenyEnabled())
			if tc.sampleRate == 0 {
				assert.Empty(t, PermissionDenySampleRate())
			} else {
				assert.InEpsilon(t, tc.sampleRate, PermissionDenySampleRate(), 0)
			}
		})
	}
}
