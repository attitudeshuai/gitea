// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package setting

import (
	"math"
	"strings"
	"time"

	"gitea.dev/modules/log"
)

type AuditRecordOutput string

const (
	AuditRecordOutputDisabled AuditRecordOutput = "disabled"
	AuditRecordOutputDatabase AuditRecordOutput = "database"
)

var Audit = struct {
	RecordOutput  AuditRecordOutput `ini:"RECORD_OUTPUT"`
	RetentionDays int64             `ini:"RETENTION_DAYS"`

	PermissionDenyEnabled    bool    `ini:"PERMISSION_DENY_ENABLED"`
	PermissionDenySampleRate float64 `ini:"PERMISSION_DENY_SAMPLE_RATE"`
}{
	RecordOutput:             AuditRecordOutputDisabled,
	RetentionDays:            30,
	PermissionDenySampleRate: 1,
}

func loadAuditFrom(rootCfg ConfigProvider) {
	mustMapSetting(rootCfg, "audit", &Audit)

	Audit.RecordOutput = AuditRecordOutput(strings.ToLower(strings.TrimSpace(string(Audit.RecordOutput))))
	switch Audit.RecordOutput {
	case "":
		Audit.RecordOutput = AuditRecordOutputDisabled
	case AuditRecordOutputDisabled, AuditRecordOutputDatabase:
	default:
		log.Error("Invalid [audit].RECORD_OUTPUT %q, audit records are disabled", Audit.RecordOutput)
		Audit.RecordOutput = AuditRecordOutputDisabled
	}

	if Audit.RetentionDays < 0 {
		Audit.RetentionDays = 0 // keep forever
	}

	// an out-of-range (including NaN) rate is invalid and gets clamped to [0, 1]
	if math.IsNaN(Audit.PermissionDenySampleRate) {
		log.Error("Invalid [audit].PERMISSION_DENY_SAMPLE_RATE, reset to 0")
		Audit.PermissionDenySampleRate = 0
	} else if Audit.PermissionDenySampleRate < 0 || Audit.PermissionDenySampleRate > 1 {
		log.Error("Invalid [audit].PERMISSION_DENY_SAMPLE_RATE, clamped to [0, 1]")
		Audit.PermissionDenySampleRate = max(0, min(1, Audit.PermissionDenySampleRate))
	}
}

// AuditRetentionPeriod is the age at which recorded events are pruned, zero meaning they are kept forever.
func AuditRetentionPeriod() time.Duration {
	return time.Duration(Audit.RetentionDays) * 24 * time.Hour
}

// AuditRecordEnabled reports whether audit events are recorded at all.
func AuditRecordEnabled() bool {
	return Audit.RecordOutput != AuditRecordOutputDisabled
}

// PermissionDenyEnabled reports whether denied repository requests carry a reason and get audited.
func PermissionDenyEnabled() bool {
	return Audit.PermissionDenyEnabled
}

// PermissionDenySampleRate is the fraction of denied requests that get audited, 0..1.
func PermissionDenySampleRate() float64 {
	return Audit.PermissionDenySampleRate
}
