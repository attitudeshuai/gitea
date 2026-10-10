// Copyright 2023 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package queue

import (
	"time"

	"gitea.dev/modules/setting"
)

const (
	DegradedPushFastFail = "fast-fail"
	DegradedPushBlock    = "block"
)

type BaseConfig struct {
	ManagedName string
	DataFullDir string // the caller must prepare an absolute path

	ConnStr string
	Length  int

	QueueFullName, SetFullName string

	// metrics and degradation, all zero values mean metrics disabled
	MetricsEnabled          bool
	DegradeWindow           time.Duration
	DegradeFailureThreshold float64
	DegradeMinSamples       int
	DegradePushBehavior     string

	// OnBackendError is called when a backend operation fails but will be retried.
	// It is nil when metrics are disabled so the retry behavior stays unchanged.
	OnBackendError func(err error)
}

func toBaseConfig(managedName string, queueSetting setting.QueueSettings) *BaseConfig {
	baseConfig := &BaseConfig{
		ManagedName: managedName,
		DataFullDir: queueSetting.Datadir,

		ConnStr: queueSetting.ConnStr,
		Length:  queueSetting.Length,

		MetricsEnabled:          queueSetting.MetricsEnabled,
		DegradeWindow:           queueSetting.DegradeWindow,
		DegradeFailureThreshold: queueSetting.DegradeFailureThreshold,
		DegradeMinSamples:       queueSetting.DegradeMinSamples,
		DegradePushBehavior:     queueSetting.DegradePushBehavior,
	}

	// queue name and set name
	baseConfig.QueueFullName = managedName + queueSetting.QueueName
	baseConfig.SetFullName = baseConfig.QueueFullName + queueSetting.SetName
	if baseConfig.SetFullName == baseConfig.QueueFullName {
		baseConfig.SetFullName += "_unique"
	}
	return baseConfig
}
