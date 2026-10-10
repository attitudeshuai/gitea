// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package git

import (
	"testing"

	"gitea.dev/models/db"
	"gitea.dev/models/unittest"
	"gitea.dev/modules/timeutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInsertAndFindHookRuns(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())

	const repoID int64 = 9_900_001
	exitCode0, exitCode7 := 0, 7

	runs := []*HookRun{
		{RepoID: repoID, Phase: "pre-receive", HookName: "alpha", StartedUnix: timeutil.TimeStamp(100), DurationMs: 10, ExitCode: &exitCode0},
		{RepoID: repoID, IsWiki: true, Phase: "update", HookName: "beta", StartedUnix: timeutil.TimeStamp(200), DurationMs: 20, ExitCode: &exitCode7},
		{RepoID: repoID, Phase: "post-receive", HookName: "alpha", StartedUnix: timeutil.TimeStamp(300), DurationMs: 30, IsTimeout: true},
		{RepoID: repoID, Phase: "pre-receive", HookName: "gamma", StartedUnix: timeutil.TimeStamp(400), DurationMs: 0, IsSkipped: true},
	}
	for _, run := range runs {
		require.NoError(t, InsertHookRun(t.Context(), run))
	}

	// invalid arguments
	assert.Error(t, InsertHookRun(t.Context(), &HookRun{RepoID: repoID, Phase: "bad-phase", HookName: "x"}))
	assert.Error(t, InsertHookRun(t.Context(), &HookRun{RepoID: repoID, Phase: "pre-receive", HookName: ""}))
	_, _, err := FindHookRuns(t.Context(), FindHookRunsOptions{RepoID: 0})
	assert.Error(t, err)

	// all records for the repo, newest first
	found, total, err := FindHookRuns(t.Context(), FindHookRunsOptions{RepoID: repoID, ListOptions: db.ListOptionsAll})
	require.NoError(t, err)
	assert.EqualValues(t, 4, total)
	require.Len(t, found, 4)
	assert.Equal(t, "gamma", found[0].HookName)
	assert.Equal(t, "alpha", found[3].HookName)
	assert.Nil(t, found[0].ExitCode) // skipped run has no exit code

	// filter by hook name
	found, total, err = FindHookRuns(t.Context(), FindHookRunsOptions{RepoID: repoID, HookName: "alpha", ListOptions: db.ListOptionsAll})
	require.NoError(t, err)
	assert.EqualValues(t, 2, total)
	for _, run := range found {
		assert.Equal(t, "alpha", run.HookName)
	}

	// filter by phase
	found, total, err = FindHookRuns(t.Context(), FindHookRunsOptions{RepoID: repoID, Phase: "pre-receive", ListOptions: db.ListOptionsAll})
	require.NoError(t, err)
	assert.EqualValues(t, 2, total)
	for _, run := range found {
		assert.Equal(t, "pre-receive", run.Phase)
	}

	// pagination
	found, total, err = FindHookRuns(t.Context(), FindHookRunsOptions{RepoID: repoID, ListOptions: db.ListOptions{Page: 1, PageSize: 2}})
	require.NoError(t, err)
	assert.EqualValues(t, 4, total)
	require.Len(t, found, 2)
	assert.Equal(t, "gamma", found[0].HookName)
	assert.Equal(t, "alpha", found[1].HookName) // post-receive alpha at timestamp 300
}

func TestInsertHookRunPrunesToMostRecent(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())

	const repoID int64 = 9_900_002
	exitCode := 0

	for i := range HookRunKeptPerRepository + 5 {
		run := &HookRun{
			RepoID:      repoID,
			Phase:       "pre-receive",
			HookName:    "loop",
			StartedUnix: timeutil.TimeStamp(1000 + int64(i)),
			ExitCode:    &exitCode,
		}
		require.NoError(t, InsertHookRun(t.Context(), run))
	}

	found, total, err := FindHookRuns(t.Context(), FindHookRunsOptions{RepoID: repoID, ListOptions: db.ListOptionsAll})
	require.NoError(t, err)
	assert.EqualValues(t, HookRunKeptPerRepository, total)
	require.Len(t, found, HookRunKeptPerRepository)

	// the most recent run is kept and the oldest five were pruned
	assert.EqualValues(t, 1000+HookRunKeptPerRepository+4, int64(found[0].StartedUnix))
	assert.EqualValues(t, 1005, int64(found[len(found)-1].StartedUnix))

	// a different repository keeps its own records
	const otherRepoID int64 = 9_900_003
	require.NoError(t, InsertHookRun(t.Context(), &HookRun{RepoID: otherRepoID, Phase: "update", HookName: "solo", StartedUnix: 1}))
	_, otherTotal, err := FindHookRuns(t.Context(), FindHookRunsOptions{RepoID: otherRepoID, ListOptions: db.ListOptionsAll})
	require.NoError(t, err)
	assert.EqualValues(t, 1, otherTotal)
}
