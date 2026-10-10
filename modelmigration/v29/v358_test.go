// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package v29

import (
	"testing"

	"gitea.dev/modelmigration/migrationtest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAddCronTaskRunTable(t *testing.T) {
	type CronTaskRun struct {
		ID       int64 `xorm:"pk autoincr"`
		TaskName string
		Status   string
	}

	x, deferrable := migrationtest.PrepareTestEnv(t, 0)
	defer deferrable()
	if x == nil || t.Failed() {
		return
	}

	require.NoError(t, AddCronTaskRunTable(t.Context(), x))
	require.NoError(t, AddCronTaskRunTable(t.Context(), x)) // idempotent

	_, err := x.Insert(&CronTaskRun{TaskName: "archive_cleanup", Status: "success"})
	require.NoError(t, err)

	rows := make([]*CronTaskRun, 0)
	require.NoError(t, x.Where("task_name = ?", "archive_cleanup").Find(&rows))
	if assert.Len(t, rows, 1) {
		assert.Equal(t, "success", rows[0].Status)
	}
}
