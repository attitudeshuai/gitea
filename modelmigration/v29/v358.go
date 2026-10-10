// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package v29

import (
	"context"

	"gitea.dev/modelmigration/base"

	"xorm.io/xorm"
)

// AddRetryColumnsToHookTask adds the automatic-retry state machine columns to hook_task:
// status (0 pending, 1 succeed, 2 failed, 3 waiting, 4 dead, 5 sending),
// retries, next_retry_at (indexed, scanned by the retry scheduler) and last_error.
func AddRetryColumnsToHookTask(_ context.Context, x base.EngineMigration) error {
	type HookTask struct {
		Status      int    `xorm:"DEFAULT 0"`
		Retries     int    `xorm:"DEFAULT 0"`
		NextRetryAt *int64 `xorm:"INDEX"`
		LastError   string `xorm:"TEXT"`
	}

	if _, err := x.SyncWithOptions(xorm.SyncOptions{
		IgnoreDropIndices: true,
		IgnoreConstrains:  true,
	}, new(HookTask)); err != nil {
		return err
	}

	// Backfill status from the legacy is_delivered/is_succeed pair. Previously
	// undelivered rows keep the default status 0 (pending).
	if _, err := x.Table("hook_task").
		Where("is_delivered = ? AND is_succeed = ?", true, true).
		Update(map[string]any{"status": 1}); err != nil {
		return err
	}
	_, err := x.Table("hook_task").
		Where("is_delivered = ? AND is_succeed = ?", true, false).
		Update(map[string]any{"status": 2})
	return err
}
