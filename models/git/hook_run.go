// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package git

import (
	"context"
	"fmt"
	"slices"

	"gitea.dev/models/db"
	"gitea.dev/modules/timeutil"
	"gitea.dev/modules/util"

	"xorm.io/builder"
)

// HookRunKeptPerRepository is the number of most recent hook run records kept per repository
const HookRunKeptPerRepository = 200

// Hook phase names, they match the Git server-side hook names
var hookRunPhases = []string{"pre-receive", "update", "post-receive"}

// IsValidHookPhase returns true if the phase is a valid Git server-side hook name
func IsValidHookPhase(phase string) bool {
	return slices.Contains(hookRunPhases, phase)
}

// HookRun records one execution of a custom server-side Git hook script
type HookRun struct {
	ID          int64 `xorm:"pk autoincr"`
	RepoID      int64 `xorm:"INDEX NOT NULL"`
	IsWiki      bool
	Phase       string             `xorm:"VARCHAR(20) NOT NULL"`
	HookName    string             `xorm:"VARCHAR(255) NOT NULL"`
	StartedUnix timeutil.TimeStamp `xorm:"INDEX NOT NULL"`
	DurationMs  int64
	ExitCode    *int `xorm:"NULL"` // nil when the script was skipped and never executed
	IsTimeout   bool
	IsTruncated bool
	IsSkipped   bool
}

// TableName returns the table name for HookRun
func (HookRun) TableName() string {
	return "githook_run"
}

// GetExitCode returns the script exit code, or -1 when the script was skipped and never executed
func (r *HookRun) GetExitCode() int {
	if r.ExitCode == nil {
		return -1
	}
	return *r.ExitCode
}

func init() {
	db.RegisterModel(new(HookRun))
}

// FindHookRunsOptions represents the options for finding hook run records
type FindHookRunsOptions struct {
	db.ListOptions
	RepoID   int64
	HookName string
	Phase    string
}

// ToConds returns the query conditions for finding hook run records
func (opts FindHookRunsOptions) ToConds() builder.Cond {
	var cond builder.Cond = builder.Eq{"repo_id": opts.RepoID}
	if opts.HookName != "" {
		cond = cond.And(builder.Eq{"hook_name": opts.HookName})
	}
	if opts.Phase != "" {
		cond = cond.And(builder.Eq{"phase": opts.Phase})
	}
	return cond
}

// ToOrders returns the ordering for finding hook run records
func (opts FindHookRunsOptions) ToOrders() string {
	return "started_unix DESC, id DESC"
}

// FindHookRuns returns the hook run records for a repository, newest first
func FindHookRuns(ctx context.Context, opts FindHookRunsOptions) ([]*HookRun, int64, error) {
	if opts.RepoID <= 0 {
		return nil, 0, util.NewInvalidArgumentErrorf("FindHookRuns: repo ID is required")
	}
	if opts.Phase != "" && !IsValidHookPhase(opts.Phase) {
		return nil, 0, util.NewInvalidArgumentErrorf("FindHookRuns: invalid hook phase %q", opts.Phase)
	}
	return db.FindAndCount[HookRun](ctx, opts)
}

// FindHookRunHookNames returns the distinct hook script names that have run records in the repository
func FindHookRunHookNames(ctx context.Context, repoID int64) ([]string, error) {
	if repoID <= 0 {
		return nil, util.NewInvalidArgumentErrorf("FindHookRunHookNames: repo ID is required")
	}
	names := make([]string, 0, 10)
	err := db.GetEngine(ctx).Table("githook_run").
		Where("repo_id = ?", repoID).
		Distinct("hook_name").
		OrderBy("hook_name").
		Find(&names)
	if err != nil {
		return nil, fmt.Errorf("FindHookRunHookNames: %w", err)
	}
	return names, nil
}

// InsertHookRun inserts a hook run record and prunes the repository's records to the most recent ones
func InsertHookRun(ctx context.Context, run *HookRun) error {
	if run.RepoID <= 0 {
		return util.NewInvalidArgumentErrorf("InsertHookRun: repo ID is required")
	}
	if !IsValidHookPhase(run.Phase) {
		return util.NewInvalidArgumentErrorf("InsertHookRun: invalid hook phase %q", run.Phase)
	}
	if run.HookName == "" {
		return util.NewInvalidArgumentErrorf("InsertHookRun: hook name is required")
	}
	if _, err := db.GetEngine(ctx).Insert(run); err != nil {
		return fmt.Errorf("InsertHookRun: %w", err)
	}
	return pruneHookRuns(ctx, run.RepoID)
}

func pruneHookRuns(ctx context.Context, repoID int64) error {
	e := db.GetEngine(ctx)
	keepIDs := make([]int64, 0, HookRunKeptPerRepository)
	if err := e.Table("githook_run").
		Where("repo_id = ?", repoID).
		OrderBy("started_unix DESC, id DESC").
		Limit(HookRunKeptPerRepository, 0).
		Select("id").
		Find(&keepIDs); err != nil {
		return fmt.Errorf("pruneHookRuns: %w", err)
	}
	if len(keepIDs) < HookRunKeptPerRepository {
		return nil
	}
	if _, err := e.Where("repo_id = ?", repoID).NotIn("id", keepIDs).Delete(&HookRun{}); err != nil {
		return fmt.Errorf("pruneHookRuns: %w", err)
	}
	return nil
}
