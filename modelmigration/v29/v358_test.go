// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package v29

import (
	"testing"

	"gitea.dev/modelmigration/migrationtest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type oldHookTaskV358 struct {
	ID          int64 `xorm:"pk autoincr"`
	IsDelivered bool
	IsSucceed   bool
}

func (oldHookTaskV358) TableName() string { return "hook_task" }

type newHookTaskV358 struct {
	ID          int64 `xorm:"pk autoincr"`
	IsDelivered bool
	IsSucceed   bool
	Status      int
	Retries     int
	NextRetryAt *int64
	LastError   string
}

func (newHookTaskV358) TableName() string { return "hook_task" }

func TestAddRetryColumnsToHookTask(t *testing.T) {
	x, deferrable := migrationtest.PrepareTestEnv(t, 0, new(oldHookTaskV358))
	defer deferrable()
	if x == nil || t.Failed() {
		return
	}

	_, err := x.Insert(
		&oldHookTaskV358{ID: 1, IsDelivered: false, IsSucceed: false},
		&oldHookTaskV358{ID: 2, IsDelivered: true, IsSucceed: true},
		&oldHookTaskV358{ID: 3, IsDelivered: true, IsSucceed: false},
	)
	require.NoError(t, err)
	require.NoError(t, AddRetryColumnsToHookTask(t.Context(), x))

	tasks := make([]*newHookTaskV358, 0, 3)
	require.NoError(t, x.Table("hook_task").Asc("id").Find(&tasks))
	require.Len(t, tasks, 3)
	assert.Equal(t, 0, tasks[0].Status) // undelivered -> pending
	assert.Equal(t, 1, tasks[1].Status) // delivered and succeeded
	assert.Equal(t, 2, tasks[2].Status) // delivered but failed
	for _, task := range tasks {
		assert.Zero(t, task.Retries)
		assert.Nil(t, task.NextRetryAt)
	}

	// running the migration again must be idempotent
	require.NoError(t, AddRetryColumnsToHookTask(t.Context(), x))
}
