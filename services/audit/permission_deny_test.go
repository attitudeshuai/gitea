// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package audit

import (
	"testing"

	audit_model "gitea.dev/models/audit"
	"gitea.dev/models/db"
	access_model "gitea.dev/models/perm/access"
	repo_model "gitea.dev/models/repo"
	"gitea.dev/models/unit"
	"gitea.dev/models/unittest"
	user_model "gitea.dev/models/user"
	"gitea.dev/modules/setting"
	"gitea.dev/modules/test"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func countPermissionDenyEvents(t *testing.T) int64 {
	t.Helper()
	n, err := db.GetEngine(t.Context()).Where("action LIKE ?", "repository:permission:deny:%").Count(&audit_model.Event{})
	require.NoError(t, err)
	return n
}

// assertRecorded asserts RecordPermissionDeny calls added exactly delta events.
func assertRecorded(t *testing.T, delta int, calls func()) {
	t.Helper()
	before := countPermissionDenyEvents(t)
	calls()
	assert.Equal(t, int64(delta), countPermissionDenyEvents(t)-before)
}

func TestRecordPermissionDeny(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())

	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 39})
	doer := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 4})
	decision := &access_model.Decision{
		Unit:       unit.TypeCode,
		DenyReason: access_model.DenyReasonOrgMembershipRequired,
		Source:     access_model.DecisionSourceTeamMember,
	}

	t.Run("Disabled", func(t *testing.T) {
		defer test.MockVariableValue(&setting.Audit)()

		assertRecorded(t, 0, func() {
			RecordPermissionDeny(t.Context(), repo, doer, decision)
		})
	})

	t.Run("SampleRateZero", func(t *testing.T) {
		defer test.MockVariableValue(&setting.Audit)()
		setting.Audit.PermissionDenyEnabled = true
		setting.Audit.PermissionDenySampleRate = 0

		assertRecorded(t, 0, func() {
			RecordPermissionDeny(t.Context(), repo, doer, decision)
		})
	})

	t.Run("SampleRateOne", func(t *testing.T) {
		defer test.MockVariableValue(&setting.Audit)()
		setting.Audit.PermissionDenyEnabled = true
		setting.Audit.PermissionDenySampleRate = 1

		var e *audit_model.Event
		assertRecorded(t, 1, func() {
			RecordPermissionDeny(t.Context(), repo, doer, decision)
			events, _, err := FindEvents(t.Context(), &audit_model.EventSearchOptions{
				ActionPrefix: "repository:permission:deny",
				Sort:         audit_model.SortTimestampDesc,
			})
			require.NoError(t, err)
			require.NotEmpty(t, events)
			e = events[0]
		})
		assert.Equal(t, audit_model.RepositoryPermissionDenyOrgMembershipRequired, e.Action)
		assert.Equal(t, audit_model.ScopeRepository, e.ScopeType)
		assert.Equal(t, repo.ID, e.ScopeID)
		assert.Equal(t, doer.ID, e.ActorID)
		assert.NotZero(t, e.TimestampUnix)
		meta := audit_model.DecodeMetadata(e.Metadata)
		assert.Equal(t, "code", meta["unit"])
		assert.Equal(t, "team_member", meta["source"])
	})

	t.Run("SampleRateHalf", func(t *testing.T) {
		defer test.MockVariableValue(&setting.Audit)()
		setting.Audit.PermissionDenyEnabled = true
		setting.Audit.PermissionDenySampleRate = 0.5

		values := []float64{0.4, 0.6, 0.4} // below rate: sampled, above: skipped, below: sampled
		i := 0
		defer test.MockVariableValue(&randomFloat64, func() float64 {
			v := values[i]
			i++
			return v
		})()

		assertRecorded(t, 2, func() {
			for range 3 {
				RecordPermissionDeny(t.Context(), repo, doer, decision)
			}
		})
	})

	t.Run("UnknownReason", func(t *testing.T) {
		defer test.MockVariableValue(&setting.Audit)()
		setting.Audit.PermissionDenyEnabled = true

		unknown := *decision
		unknown.DenyReason = "not_a_reason"
		assertRecorded(t, 0, func() {
			RecordPermissionDeny(t.Context(), repo, doer, &unknown)
		})
	})

	t.Run("AnonymousActor", func(t *testing.T) {
		defer test.MockVariableValue(&setting.Audit)()
		setting.Audit.PermissionDenyEnabled = true
		setting.Audit.PermissionDenySampleRate = 1

		anonDecision := &access_model.Decision{
			Unit:       unit.TypeCode,
			DenyReason: access_model.DenyReasonUnauthenticated,
			Source:     access_model.DecisionSourceRepoUnitPublic,
		}
		assertRecorded(t, 1, func() {
			RecordPermissionDeny(t.Context(), repo, nil, anonDecision)
		})
	})
}
