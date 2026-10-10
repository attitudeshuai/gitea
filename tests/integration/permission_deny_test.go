// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package integration

import (
	"net/http"
	"testing"

	audit_model "gitea.dev/models/audit"
	auth_model "gitea.dev/models/auth"
	"gitea.dev/models/db"
	"gitea.dev/modules/setting"
	"gitea.dev/modules/test"
	"gitea.dev/tests"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func denyEventCount(t *testing.T) int64 {
	t.Helper()
	n, err := db.GetEngine(t.Context()).Where("action LIKE ?", "repository:permission:deny:%").Count(&audit_model.Event{})
	require.NoError(t, err)
	return n
}

func TestPermissionDenyHeadersAndAudit(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	t.Run("Disabled", func(t *testing.T) {
		defer test.MockVariableValue(&setting.Audit)()
		require.False(t, setting.PermissionDenyEnabled())

		// web visibility gate: anonymous private repo
		req := NewRequest(t, "GET", "/user2/repo2")
		resp := MakeRequest(t, req, http.StatusNotFound)
		assert.Empty(t, resp.Header().Get("X-Gitea-Deny-Reason"))
		assert.Empty(t, resp.Header().Get("X-Gitea-Deny-Source"))

		// API visibility gate
		req = NewRequest(t, "GET", "/api/v1/repos/user2/repo2")
		resp = MakeRequest(t, req, http.StatusNotFound)
		assert.Empty(t, resp.Header().Get("X-Gitea-Deny-Reason"))

		assert.Zero(t, denyEventCount(t))
	})

	t.Run("Enabled", func(t *testing.T) {
		defer test.MockVariableValue(&setting.Audit)()
		setting.Audit.PermissionDenyEnabled = true
		setting.Audit.PermissionDenySampleRate = 1

		before := denyEventCount(t)

		// anonymous private repo: unauthenticated
		req := NewRequest(t, "GET", "/user2/repo2")
		resp := MakeRequest(t, req, http.StatusNotFound)
		assert.Equal(t, "unauthenticated", resp.Header().Get("X-Gitea-Deny-Reason"))
		assert.Equal(t, "repo_unit_public", resp.Header().Get("X-Gitea-Deny-Source"))

		// anonymous public repo on a private owner: owner invisible
		req = NewRequest(t, "GET", "/privated_org/public_repo_on_private_org")
		resp = MakeRequest(t, req, http.StatusNotFound)
		assert.Equal(t, "owner_invisible", resp.Header().Get("X-Gitea-Deny-Reason"))
		assert.Equal(t, "owner", resp.Header().Get("X-Gitea-Deny-Source"))

		// API visibility gate: anonymous private repo
		req = NewRequest(t, "GET", "/api/v1/repos/user2/repo2")
		resp = MakeRequest(t, req, http.StatusNotFound)
		assert.Equal(t, "unauthenticated", resp.Header().Get("X-Gitea-Deny-Reason"))
		assert.Equal(t, "repo_unit_public", resp.Header().Get("X-Gitea-Deny-Source"))

		// API total gate: user5 is a non-member of public org3, private repo3 rejects at assignment
		token := getUserToken(t, "user5", auth_model.AccessTokenScopeReadRepository)
		req = NewRequest(t, "GET", "/api/v1/repos/org3/repo3/keys").
			AddTokenAuth(token)
		resp = MakeRequest(t, req, http.StatusNotFound)
		assert.Equal(t, "org_membership_required", resp.Header().Get("X-Gitea-Deny-Reason"))
		assert.Equal(t, "team_member", resp.Header().Get("X-Gitea-Deny-Source"))

		// four denials, rate 1: four audit events
		assert.Equal(t, before+4, denyEventCount(t))
	})

	t.Run("EnabledSampleZero", func(t *testing.T) {
		defer test.MockVariableValue(&setting.Audit)()
		setting.Audit.PermissionDenyEnabled = true
		setting.Audit.PermissionDenySampleRate = 0

		before := denyEventCount(t)

		// headers still attached; sampling only suppresses persistence
		req := NewRequest(t, "GET", "/user2/repo2")
		resp := MakeRequest(t, req, http.StatusNotFound)
		assert.Equal(t, "unauthenticated", resp.Header().Get("X-Gitea-Deny-Reason"))
		assert.Equal(t, before, denyEventCount(t))
	})
}
