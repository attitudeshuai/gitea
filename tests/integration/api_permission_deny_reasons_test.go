// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package integration

import (
	"net/http"
	"testing"

	auth_model "gitea.dev/models/auth"
	"gitea.dev/modules/setting"
	api "gitea.dev/modules/structs"
	"gitea.dev/modules/test"
	"gitea.dev/tests"

	"github.com/stretchr/testify/assert"
)

func countByReason(t *testing.T, counts []api.PermissionDenyReasonCount, reason string) int64 {
	t.Helper()
	for _, c := range counts {
		if c.Reason == reason {
			return c.Count
		}
	}
	return 0
}

func TestAPIPermissionDenyReasons(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	// auditing on, every denial recorded
	defer test.MockVariableValue(&setting.Audit)()
	setting.Audit.PermissionDenyEnabled = true
	setting.Audit.PermissionDenySampleRate = 1

	// two unauthenticated denials on repo2 (web + API), one owner_invisible on repo40
	MakeRequest(t, NewRequest(t, "GET", "/user2/repo2"), http.StatusNotFound)
	MakeRequest(t, NewRequest(t, "GET", "/api/v1/repos/user2/repo2"), http.StatusNotFound)
	MakeRequest(t, NewRequest(t, "GET", "/privated_org/public_repo_on_private_org"), http.StatusNotFound)

	t.Run("RepoScoped", func(t *testing.T) {
		ownerToken := getUserToken(t, "user2", auth_model.AccessTokenScopeReadRepository)
		req := NewRequest(t, "GET", "/api/v1/repos/user2/repo2/permission/deny-reasons").
			AddTokenAuth(ownerToken)
		resp := MakeRequest(t, req, http.StatusOK)
		var counts []api.PermissionDenyReasonCount
		DecodeJSON(t, resp, &counts)
		assert.Equal(t, int64(2), countByReason(t, counts, "unauthenticated"))
		assert.Zero(t, countByReason(t, counts, "owner_invisible"))

		// unit filter: the denials were on code
		req = NewRequest(t, "GET", "/api/v1/repos/user2/repo2/permission/deny-reasons?unit=code").
			AddTokenAuth(ownerToken)
		resp = MakeRequest(t, req, http.StatusOK)
		DecodeJSON(t, resp, &counts)
		assert.Equal(t, int64(2), countByReason(t, counts, "unauthenticated"))

		// unit filter: no actions denials
		req = NewRequest(t, "GET", "/api/v1/repos/user2/repo2/permission/deny-reasons?unit=actions").
			AddTokenAuth(ownerToken)
		resp = MakeRequest(t, req, http.StatusOK)
		DecodeJSON(t, resp, &counts)
		assert.Empty(t, counts)

		// non-admin on a repo they can read: elevation denied
		otherToken := getUserToken(t, "user4", auth_model.AccessTokenScopeReadRepository)
		req = NewRequest(t, "GET", "/api/v1/repos/user2/repo1/permission/deny-reasons").
			AddTokenAuth(otherToken)
		MakeRequest(t, req, http.StatusForbidden)
	})

	t.Run("Global", func(t *testing.T) {
		adminToken := getUserToken(t, "user1", auth_model.AccessTokenScopeReadAdmin)
		req := NewRequest(t, "GET", "/api/v1/admin/permission/deny-reasons").
			AddTokenAuth(adminToken)
		resp := MakeRequest(t, req, http.StatusOK)
		var counts []api.PermissionDenyReasonCount
		DecodeJSON(t, resp, &counts)
		assert.GreaterOrEqual(t, countByReason(t, counts, "unauthenticated"), int64(2))
		assert.GreaterOrEqual(t, countByReason(t, counts, "owner_invisible"), int64(1))

		// since/until window excluding all events
		req = NewRequest(t, "GET", "/api/v1/admin/permission/deny-reasons?since=4000000000").
			AddTokenAuth(adminToken)
		resp = MakeRequest(t, req, http.StatusOK)
		DecodeJSON(t, resp, &counts)
		assert.Empty(t, counts)

		// non-admin on the global endpoint
		otherToken := getUserToken(t, "user2", auth_model.AccessTokenScopeReadRepository)
		req = NewRequest(t, "GET", "/api/v1/admin/permission/deny-reasons").
			AddTokenAuth(otherToken)
		MakeRequest(t, req, http.StatusForbidden)
	})
}
