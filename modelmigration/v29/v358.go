// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package v29

import (
	"context"

	"gitea.dev/modelmigration/base"
)

// AddCronTaskRunTable stores the execution history of instance-level cron tasks.
func AddCronTaskRunTable(_ context.Context, x base.EngineMigration) error {
	type CronTaskRun struct {
		ID            int64  `xorm:"pk autoincr"`
		TaskName      string `xorm:"INDEX NOT NULL"`
		TriggerType   string
		DoerID        int64
		DoerName      string
		Status        string `xorm:"INDEX"`
		ErrorCategory string
		StartedUnix   int64
		FinishedUnix  int64
		DurationMs    int64
		Message       string `xorm:"TEXT"`
	}

	return x.Sync(new(CronTaskRun))
}
