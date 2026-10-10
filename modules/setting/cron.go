// Copyright 2019 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package setting

import (
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"time"
)

// Overlap policies for [cron] OVERLAP_POLICY, used when a task is triggered while its previous run has not finished.
const (
	CronOverlapPolicySkip   = "skip"   // do not start the second run, record it as skipped
	CronOverlapPolicyQueue  = "queue"  // wait for the previous run to finish, then run once
	CronOverlapPolicyReject = "reject" // refuse the trigger and return the conflict reason to the caller
)

// Cron stores the global configuration of the instance-level cron tasks ([cron] section).
var Cron = struct {
	EnableExecutionHistory   bool
	HistoryRetentionCount    int64
	HistoryRetentionDuration time.Duration
	OverlapPolicy            string
	RunTimeout               time.Duration
}{}

func loadCronFrom(rootCfg ConfigProvider) error {
	sec := rootCfg.Section("cron")

	Cron.EnableExecutionHistory = sec.Key("ENABLE_EXECUTION_HISTORY").MustBool(false)

	Cron.OverlapPolicy = sec.Key("OVERLAP_POLICY").MustString(CronOverlapPolicySkip)
	switch Cron.OverlapPolicy {
	case CronOverlapPolicySkip, CronOverlapPolicyQueue, CronOverlapPolicyReject:
	default:
		return fmt.Errorf("loadCronFrom: invalid cron.OVERLAP_POLICY %q, must be one of %q, %q, %q", Cron.OverlapPolicy, CronOverlapPolicySkip, CronOverlapPolicyQueue, CronOverlapPolicyReject)
	}

	// 100 records per task is enough for troubleshooting and keeps the table small.
	Cron.HistoryRetentionCount = sec.Key("HISTORY_RETENTION_COUNT").MustInt64(100)
	if Cron.HistoryRetentionCount < 0 {
		return fmt.Errorf("loadCronFrom: cron.HISTORY_RETENTION_COUNT must not be negative, got %d", Cron.HistoryRetentionCount)
	}

	var err error
	if Cron.HistoryRetentionDuration, err = parseCronDuration(sec, "HISTORY_RETENTION_DURATION", 0); err != nil {
		return err
	}
	if Cron.RunTimeout, err = parseCronDuration(sec, "RUN_TIMEOUT", 0); err != nil {
		return err
	}

	if Cron.EnableExecutionHistory && Cron.HistoryRetentionCount == 0 && Cron.HistoryRetentionDuration == 0 {
		return errors.New("loadCronFrom: cron.ENABLE_EXECUTION_HISTORY requires at least one of cron.HISTORY_RETENTION_COUNT or cron.HISTORY_RETENTION_DURATION to be set, otherwise the history grows unbounded")
	}
	return nil
}

// parseCronDuration parses a duration key. A bare integer means seconds, matching the existing ini duration convention.
func parseCronDuration(sec ConfigSection, keyName string, defaultVal time.Duration) (time.Duration, error) {
	key := sec.Key(keyName)
	if !sec.HasKey(keyName) || key.String() == "" {
		return defaultVal, nil
	}
	raw := key.String()
	if v, err := strconv.ParseInt(raw, 10, 64); err == nil {
		d := time.Duration(v) * time.Second
		if d < 0 {
			return 0, fmt.Errorf("loadCronFrom: cron.%s must not be negative, got %q", keyName, raw)
		}
		return d, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("loadCronFrom: invalid cron.%s %q: %w", keyName, raw, err)
	}
	if d < 0 {
		return 0, fmt.Errorf("loadCronFrom: cron.%s must not be negative, got %q", keyName, raw)
	}
	return d, nil
}

// GetCronSettings maps the cron subsection to the provided config
func GetCronSettings(name string, config any) (any, error) {
	return getCronSettings(CfgProvider, name, config)
}

func getCronSettings(rootCfg ConfigProvider, name string, config any) (any, error) {
	if err := rootCfg.Section("cron." + name).MapTo(config); err != nil {
		return config, err
	}

	typ := reflect.TypeOf(config).Elem()
	val := reflect.ValueOf(config).Elem()

	for i := 0; i < typ.NumField(); i++ {
		field := val.Field(i)
		tpField := typ.Field(i)
		if tpField.Type.Kind() == reflect.Struct && tpField.Anonymous {
			if err := rootCfg.Section("cron." + name).MapTo(field.Addr().Interface()); err != nil {
				return config, err
			}
		}
	}

	return config, nil
}
