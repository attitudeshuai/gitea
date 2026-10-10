// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package audit

import (
	"context"
	"sort"
	"strings"

	"gitea.dev/models/db"
	"gitea.dev/models/unit"
)

// ReasonCount is the number of denial events of one reason.
type ReasonCount struct {
	Reason string `json:"reason"`
	Count  int64  `json:"count"`
}

// PermissionDenyCountOptions filters denial events for aggregation.
type PermissionDenyCountOptions struct {
	RepoID int64  // zero: every repository
	Since  int64  // unix seconds, inclusive; zero: no lower bound
	Until  int64  // unix seconds, exclusive; zero: no upper bound
	Unit   string // unit machine name; empty: all units
}

const permissionDenyActionPrefix = "repository:permission:deny:"

// CountPermissionDenyEvents aggregates recorded denial events by reason code, highest count first.
func CountPermissionDenyEvents(ctx context.Context, opts *PermissionDenyCountOptions) ([]ReasonCount, error) {
	cond := db.GetEngine(ctx).
		Where("action LIKE ?", permissionDenyActionPrefix+"%").
		And("scope_type = ?", ScopeRepository)
	if opts != nil {
		if opts.RepoID != 0 {
			cond.And("scope_id = ?", opts.RepoID)
		}
		if opts.Since != 0 {
			cond.And("timestamp_unix >= ?", opts.Since)
		}
		if opts.Until != 0 {
			cond.And("timestamp_unix < ?", opts.Until)
		}

		if opts.Unit != "" {
			// unit lives in JSON metadata; normalize the input name, then filter in Go
			unitName := unit.TypeFromName(opts.Unit).Name()
			var events []*Event
			if err := cond.Find(&events); err != nil {
				return nil, err
			}
			counts := map[string]int64{}
			for _, e := range events {
				if DecodeMetadata(e.Metadata)["unit"] == unitName {
					counts[string(e.Action)]++
				}
			}
			return reasonCountsFromMap(counts), nil
		}
	}

	type countRow struct {
		Action string
		Count  int64
	}
	var rows []countRow
	if err := cond.Table("audit_event").Select("action, COUNT(*) AS count").GroupBy("action").Find(&rows); err != nil {
		return nil, err
	}
	result := make([]ReasonCount, 0, len(rows))
	for _, r := range rows {
		result = append(result, ReasonCount{
			Reason: strings.TrimPrefix(r.Action, permissionDenyActionPrefix),
			Count:  r.Count,
		})
	}
	sortReasonCounts(result)
	return result, nil
}

func reasonCountsFromMap(counts map[string]int64) []ReasonCount {
	result := make([]ReasonCount, 0, len(counts))
	for action, n := range counts {
		result = append(result, ReasonCount{
			Reason: strings.TrimPrefix(action, permissionDenyActionPrefix),
			Count:  n,
		})
	}
	sortReasonCounts(result)
	return result
}

func sortReasonCounts(result []ReasonCount) {
	sort.Slice(result, func(i, j int) bool {
		if result[i].Count != result[j].Count {
			return result[i].Count > result[j].Count
		}
		return result[i].Reason < result[j].Reason
	})
}
