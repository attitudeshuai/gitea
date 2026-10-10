// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package integration

import (
	"net/http"
	"testing"

	audit_model "gitea.dev/models/audit"
	auth_model "gitea.dev/models/auth"
	"gitea.dev/models/db"
	"gitea.dev/models/organization"
	"gitea.dev/modules/setting"
	"gitea.dev/modules/test"
	"gitea.dev/tests"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPermissionDenyUnitLevelGates(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	ctx := t.Context()

	// select-units team in org3: user29 gets code only, issues is not granted
	team := &organization.Team{OrgID: 3, Name: "unit-select29", LowerName: "unit-select29"}
	require.NoError(t, db.Insert(ctx, team))
	require.NoError(t, db.Insert(ctx,
		&organization.OrgUser{OrgID: 3, UID: 29},
		&organization.TeamUser{OrgID: 3, TeamID: team.ID, UID: 29},
		&organization.TeamRepo{OrgID: 3, TeamID: team.ID, RepoID: 3},
		&organization.TeamUnit{OrgID: 3, TeamID: team.ID, Type: 1 /* code */, AccessMode: 1},
	))

	defer test.MockVariableValue(&setting.Audit)()
	setting.Audit.PermissionDenyEnabled = true
	setting.Audit.PermissionDenySampleRate = 1

	before, err := db.GetEngine(ctx).Where("action LIKE ?", "repository:permission:deny:%").Count(&audit_model.Event{})
	require.NoError(t, err)

	// web unit-level gate: assignment passes (code granted), issues page gets denied
	web := loginUserWithPassword(t, "user29", "password")
	resp := web.MakeRequest(t, NewRequest(t, "GET", "/org3/repo3/issues"), http.StatusNotFound)
	assert.Equal(t, "team_unit_not_granted", resp.Header().Get("X-Gitea-Deny-Reason"))
	assert.Equal(t, "team_unit_mode", resp.Header().Get("X-Gitea-Deny-Source"))

	// API unit-level gate: hooks listing guarded by reqAdmin, user29 only holds code read
	token := getUserToken(t, "user29", auth_model.AccessTokenScopeReadRepository)
	req := NewRequest(t, "GET", "/api/v1/repos/org3/repo3/hooks").
		AddTokenAuth(token)
	resp = MakeRequest(t, req, http.StatusForbidden)
	assert.Equal(t, "team_unit_not_granted", resp.Header().Get("X-Gitea-Deny-Reason"))
	assert.Equal(t, "team_unit_mode", resp.Header().Get("X-Gitea-Deny-Source"))

	after, err := db.GetEngine(ctx).Where("action LIKE ?", "repository:permission:deny:%").Count(&audit_model.Event{})
	require.NoError(t, err)
	assert.Equal(t, before+2, after)
}
