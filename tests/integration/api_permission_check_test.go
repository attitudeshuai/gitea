// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package integration

import (
	"io"
	"net/http"
	"testing"

	audit_model "gitea.dev/models/audit"
	auth_model "gitea.dev/models/auth"
	"gitea.dev/models/db"
	"gitea.dev/modules/setting"
	api "gitea.dev/modules/structs"
	"gitea.dev/modules/test"
	"gitea.dev/tests"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAPIPermissionCheck(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	t.Run("Self", func(t *testing.T) {
		// anonymous on a public repo
		req := NewRequest(t, "GET", "/api/v1/repos/user2/repo1/permission/check?unit=code")
		resp := MakeRequest(t, req, http.StatusOK)
		var d api.RepoPermissionDecision
		DecodeJSON(t, resp, &d)
		assert.True(t, d.Allowed)
		assert.Equal(t, "read", string(d.Mode))
		assert.Equal(t, "repo_unit_public", d.Source)
		assert.Empty(t, d.DenyReason)
		assert.Equal(t, "code", d.Unit)

		// owner self
		token := getUserToken(t, "user2", auth_model.AccessTokenScopeReadRepository)
		req = NewRequest(t, "GET", "/api/v1/repos/user2/repo1/permission/check?unit=code").
			AddTokenAuth(token)
		resp = MakeRequest(t, req, http.StatusOK)
		DecodeJSON(t, resp, &d)
		assert.True(t, d.Allowed)
		assert.Equal(t, "owner", d.Source)
	})

	t.Run("Other", func(t *testing.T) {
		// site admin queries another user
		adminToken := getUserToken(t, "user1", auth_model.AccessTokenScopeReadRepository)
		req := NewRequest(t, "GET", "/api/v1/repos/user2/repo1/permission/check?unit=code&username=user4").
			AddTokenAuth(adminToken)
		resp := MakeRequest(t, req, http.StatusOK)
		var d api.RepoPermissionDecision
		DecodeJSON(t, resp, &d)
		assert.True(t, d.Allowed)

		// repo owner (repo admin) queries another user
		ownerToken := getUserToken(t, "user2", auth_model.AccessTokenScopeReadRepository)
		req = NewRequest(t, "GET", "/api/v1/repos/user2/repo1/permission/check?unit=code&username=user4").
			AddTokenAuth(ownerToken)
		MakeRequest(t, req, http.StatusOK)

		// a signed non-admin querying someone else: 403 without leaking the target
		otherToken := getUserToken(t, "user4", auth_model.AccessTokenScopeReadRepository)
		req = NewRequest(t, "GET", "/api/v1/repos/user2/repo1/permission/check?unit=code&username=user5").
			AddTokenAuth(otherToken)
		resp = MakeRequest(t, req, http.StatusForbidden)
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		assert.NotContains(t, string(body), "user5")

		// anonymous querying someone else: 403
		req = NewRequest(t, "GET", "/api/v1/repos/user2/repo1/permission/check?unit=code&username=user5")
		MakeRequest(t, req, http.StatusForbidden)
	})

	t.Run("InvalidInputs", func(t *testing.T) {
		// invalid unit: 400
		req := NewRequest(t, "GET", "/api/v1/repos/user2/repo1/permission/check?unit=nonsense")
		MakeRequest(t, req, http.StatusBadRequest)

		// unknown user: 404
		token := getUserToken(t, "user1", auth_model.AccessTokenScopeReadRepository)
		req = NewRequest(t, "GET", "/api/v1/repos/user2/repo1/permission/check?unit=code&username=notauser").
			AddTokenAuth(token)
		MakeRequest(t, req, http.StatusNotFound)
	})

	t.Run("NoAuditWritten", func(t *testing.T) {
		// even with permission-deny auditing on, a query does not record an event
		defer test.MockVariableValue(&setting.Audit)()
		setting.Audit.PermissionDenyEnabled = true

		before, err := db.GetEngine(t.Context()).Where("action LIKE ?", "repository:permission:deny:%").Count(&audit_model.Event{})
		require.NoError(t, err)

		token := getUserToken(t, "user2", auth_model.AccessTokenScopeReadRepository)
		req := NewRequest(t, "GET", "/api/v1/repos/user2/repo1/permission/check?unit=code&username=user4").
			AddTokenAuth(token)
		MakeRequest(t, req, http.StatusOK)

		after, err := db.GetEngine(t.Context()).Where("action LIKE ?", "repository:permission:deny:%").Count(&audit_model.Event{})
		require.NoError(t, err)
		assert.Equal(t, before, after)
	})
}
