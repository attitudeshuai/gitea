// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package cron

import (
	"context"

	user_model "gitea.dev/models/user"
)

// testTaskRegister is the subset of testing.T used by RegisterTaskForTest.
type testTaskRegister interface {
	Helper()
	Cleanup(func())
}

// RegisterTaskForTest registers a disabled, controllable task without a schedule or locale key.
// It is meant for integration tests that need to drive deterministic overlaps and run history.
func RegisterTaskForTest(t testTaskRegister, name string, fun func(context.Context) error) *Task {
	t.Helper()
	task := &Task{
		Name:   name,
		config: &BaseConfig{},
		fun: func(ctx context.Context, _ *user_model.User, _ Config) error {
			return fun(ctx)
		},
	}
	lock.Lock()
	tasks = append(tasks, task)
	tasksMap[name] = task
	lock.Unlock()

	t.Cleanup(func() {
		lock.Lock()
		for i, item := range tasks {
			if item == task {
				tasks = append(tasks[:i], tasks[i+1:]...)
				break
			}
		}
		delete(tasksMap, name)
		lock.Unlock()
	})
	return task
}
