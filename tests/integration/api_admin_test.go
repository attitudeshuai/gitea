// Copyright 2017 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package integration

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	asymkey_model "gitea.dev/models/asymkey"
	auth_model "gitea.dev/models/auth"
	"gitea.dev/models/db"
	"gitea.dev/models/unittest"
	user_model "gitea.dev/models/user"
	"gitea.dev/modules/glob"
	"gitea.dev/modules/json"
	"gitea.dev/modules/setting"
	api "gitea.dev/modules/structs"
	"gitea.dev/modules/test"
	cron_service "gitea.dev/services/cron"
	"gitea.dev/tests"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAPIAdminCreateAndDeleteSSHKey(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	// user1 is an admin user
	session := loginUser(t, "user1")
	keyOwner := unittest.AssertExistsAndLoadBean(t, &user_model.User{Name: "user2"})

	token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeWriteAdmin)
	urlStr := fmt.Sprintf("/api/v1/admin/users/%s/keys", keyOwner.Name)
	req := NewRequestWithValues(t, "POST", urlStr, map[string]string{
		"key":   "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQC4cn+iXnA4KvcQYSV88vGn0Yi91vG47t1P7okprVmhNTkipNRIHWr6WdCO4VDr/cvsRkuVJAsLO2enwjGWWueOO6BodiBgyAOZ/5t5nJNMCNuLGT5UIo/RI1b0WRQwxEZTRjt6mFNw6lH14wRd8ulsr9toSWBPMOGWoYs1PDeDL0JuTjL+tr1SZi/EyxCngpYszKdXllJEHyI79KQgeD0Vt3pTrkbNVTOEcCNqZePSVmUH8X8Vhugz3bnE0/iE9Pb5fkWO9c4AnM1FgI/8Bvp27Fw2ShryIXuR6kKvUqhVMTuOSDHwu6A8jLE5Owt3GAYugDpDYuwTVNGrHLXKpPzrGGPE/jPmaLCMZcsdkec95dYeU3zKODEm8UQZFhmJmDeWVJ36nGrGZHL4J5aTTaeFUJmmXDaJYiJ+K2/ioKgXqnXvltu0A9R8/LGy4nrTJRr4JMLuJFoUXvGm1gXQ70w2LSpk6yl71RNC0hCtsBe8BP8IhYCM0EP5jh7eCMQZNvM= nocomment\n",
		"title": "test-key",
	}).AddTokenAuth(token)
	resp := MakeRequest(t, req, http.StatusCreated)

	newPublicKey := DecodeJSON(t, resp, &api.PublicKey{})
	unittest.AssertExistsAndLoadBean(t, &asymkey_model.PublicKey{
		ID:          newPublicKey.ID,
		Name:        newPublicKey.Title,
		Fingerprint: newPublicKey.Fingerprint,
		OwnerID:     keyOwner.ID,
	})

	req = NewRequestf(t, "DELETE", "/api/v1/admin/users/%s/keys/%d", keyOwner.Name, newPublicKey.ID).
		AddTokenAuth(token)
	MakeRequest(t, req, http.StatusNoContent)
	unittest.AssertNotExistsBean(t, &asymkey_model.PublicKey{ID: newPublicKey.ID})
}

func TestAPIAdminDeleteMissingSSHKey(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	// user1 is an admin user
	token := getUserToken(t, "user1", auth_model.AccessTokenScopeWriteAdmin)
	req := NewRequestf(t, "DELETE", "/api/v1/admin/users/user1/keys/%d", unittest.NonexistentID).
		AddTokenAuth(token)
	MakeRequest(t, req, http.StatusNotFound)
}

func TestAPIAdminDeleteUnauthorizedKey(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	adminUsername := "user1"
	normalUsername := "user2"
	token := getUserToken(t, adminUsername, auth_model.AccessTokenScopeWriteAdmin)

	urlStr := fmt.Sprintf("/api/v1/admin/users/%s/keys", adminUsername)
	req := NewRequestWithValues(t, "POST", urlStr, map[string]string{
		"key":   "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQC4cn+iXnA4KvcQYSV88vGn0Yi91vG47t1P7okprVmhNTkipNRIHWr6WdCO4VDr/cvsRkuVJAsLO2enwjGWWueOO6BodiBgyAOZ/5t5nJNMCNuLGT5UIo/RI1b0WRQwxEZTRjt6mFNw6lH14wRd8ulsr9toSWBPMOGWoYs1PDeDL0JuTjL+tr1SZi/EyxCngpYszKdXllJEHyI79KQgeD0Vt3pTrkbNVTOEcCNqZePSVmUH8X8Vhugz3bnE0/iE9Pb5fkWO9c4AnM1FgI/8Bvp27Fw2ShryIXuR6kKvUqhVMTuOSDHwu6A8jLE5Owt3GAYugDpDYuwTVNGrHLXKpPzrGGPE/jPmaLCMZcsdkec95dYeU3zKODEm8UQZFhmJmDeWVJ36nGrGZHL4J5aTTaeFUJmmXDaJYiJ+K2/ioKgXqnXvltu0A9R8/LGy4nrTJRr4JMLuJFoUXvGm1gXQ70w2LSpk6yl71RNC0hCtsBe8BP8IhYCM0EP5jh7eCMQZNvM= nocomment\n",
		"title": "test-key",
	}).AddTokenAuth(token)
	resp := MakeRequest(t, req, http.StatusCreated)
	newPublicKey := DecodeJSON(t, resp, &api.PublicKey{})

	token = getUserToken(t, normalUsername, auth_model.AccessTokenScopeAll)
	req = NewRequestf(t, "DELETE", "/api/v1/admin/users/%s/keys/%d", adminUsername, newPublicKey.ID).
		AddTokenAuth(token)
	MakeRequest(t, req, http.StatusForbidden)
}

func TestAPISudoUser(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	adminUsername := "user1"
	normalUsername := "user2"
	token := getUserToken(t, adminUsername, auth_model.AccessTokenScopeReadUser)

	req := NewRequest(t, "GET", "/api/v1/user?sudo="+normalUsername).
		AddTokenAuth(token)
	resp := MakeRequest(t, req, http.StatusOK)
	user := DecodeJSON(t, resp, &api.User{})

	assert.Equal(t, normalUsername, user.UserName)
}

func TestAPISudoUserForbidden(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	adminUsername := "user1"
	normalUsername := "user2"

	token := getUserToken(t, normalUsername, auth_model.AccessTokenScopeReadAdmin)
	req := NewRequest(t, "GET", "/api/v1/user?sudo="+adminUsername).
		AddTokenAuth(token)
	MakeRequest(t, req, http.StatusForbidden)
}

func TestAPIListUsers(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	adminUsername := "user1"
	token := getUserToken(t, adminUsername, auth_model.AccessTokenScopeReadAdmin)

	req := NewRequest(t, "GET", "/api/v1/admin/users").
		AddTokenAuth(token)
	resp := MakeRequest(t, req, http.StatusOK)
	users := DecodeJSON(t, resp, []api.User{})

	found := false
	for _, user := range users {
		if user.UserName == adminUsername {
			found = true
		}
	}
	assert.True(t, found)
	numberOfUsers := unittest.GetCount(t, &user_model.User{}, "type = 0")
	assert.Len(t, users, numberOfUsers)
}

func TestAPIListUsersNotLoggedIn(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	req := NewRequest(t, "GET", "/api/v1/admin/users")
	MakeRequest(t, req, http.StatusUnauthorized)
}

func TestAPIListUsersNonAdmin(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	nonAdminUsername := "user2"
	token := getUserToken(t, nonAdminUsername, auth_model.AccessTokenScopeAll)
	req := NewRequest(t, "GET", "/api/v1/admin/users").
		AddTokenAuth(token)
	MakeRequest(t, req, http.StatusForbidden)
}

func TestAPICreateUserInvalidEmail(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	adminUsername := "user1"
	token := getUserToken(t, adminUsername, auth_model.AccessTokenScopeWriteAdmin)
	req := NewRequestWithValues(t, "POST", "/api/v1/admin/users", map[string]string{
		"email":                "invalid_email@domain.com\r\n",
		"full_name":            "invalid user",
		"login_name":           "invalidUser",
		"must_change_password": "true",
		"password":             "password",
		"send_notify":          "true",
		"source_id":            "0",
		"username":             "invalidUser",
	}).AddTokenAuth(token)
	MakeRequest(t, req, http.StatusUnprocessableEntity)
}

func TestAPICreateAndDeleteUser(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	adminUsername := "user1"
	token := getUserToken(t, adminUsername, auth_model.AccessTokenScopeWriteAdmin)

	req := NewRequestWithValues(
		t,
		"POST",
		"/api/v1/admin/users",
		map[string]string{
			"email":                "deleteme@domain.com",
			"full_name":            "delete me",
			"login_name":           "deleteme",
			"must_change_password": "true",
			"password":             "password",
			"send_notify":          "true",
			"source_id":            "0",
			"username":             "deleteme",
		},
	).AddTokenAuth(token)
	MakeRequest(t, req, http.StatusCreated)

	req = NewRequest(t, "DELETE", "/api/v1/admin/users/deleteme").
		AddTokenAuth(token)
	MakeRequest(t, req, http.StatusNoContent)
}

func TestAPIEditUser(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	adminUsername := "user1"
	token := getUserToken(t, adminUsername, auth_model.AccessTokenScopeWriteAdmin)
	urlStr := "/api/v1/admin/users/" + "user2"

	fullNameToChange := "Full Name User 2"
	req := NewRequestWithValues(t, "PATCH", urlStr, map[string]string{
		// required
		"login_name": "user2",
		"source_id":  "0",
		// to change
		"full_name": fullNameToChange,
	}).AddTokenAuth(token)
	MakeRequest(t, req, http.StatusOK)
	user2 := unittest.AssertExistsAndLoadBean(t, &user_model.User{LoginName: "user2"})
	assert.Equal(t, fullNameToChange, user2.FullName)

	req = NewRequestWithJSON(t, "PATCH", urlStr, api.EditUserOption{SourceID: 0, Email: new("")}).AddTokenAuth(token)
	resp := MakeRequest(t, req, http.StatusBadRequest)

	errMap := make(map[string]any)
	json.Unmarshal(resp.Body.Bytes(), &errMap)
	assert.Equal(t, "email address is invalid: ", errMap["message"])

	user2 = unittest.AssertExistsAndLoadBean(t, &user_model.User{LoginName: "user2"})
	assert.False(t, user2.IsRestricted)
	req = NewRequestWithJSON(t, "PATCH", urlStr, api.EditUserOption{Restricted: new(true)}).AddTokenAuth(token)
	MakeRequest(t, req, http.StatusOK)
	user2 = unittest.AssertExistsAndLoadBean(t, &user_model.User{LoginName: "user2"})
	assert.True(t, user2.IsRestricted)
}

func TestAPICreateRepoForUser(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	adminUsername := "user1"
	token := getUserToken(t, adminUsername, auth_model.AccessTokenScopeWriteAdmin)

	req := NewRequestWithJSON(
		t,
		"POST",
		fmt.Sprintf("/api/v1/admin/users/%s/repos", adminUsername),
		&api.CreateRepoOption{
			Name: "admincreatedrepo",
		},
	).AddTokenAuth(token)
	MakeRequest(t, req, http.StatusCreated)
}

func TestAPIRenameUser(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	adminUsername := "user1"
	token := getUserToken(t, adminUsername, auth_model.AccessTokenScopeWriteAdmin)
	urlStr := fmt.Sprintf("/api/v1/admin/users/%s/rename", "user2")
	req := NewRequestWithValues(t, "POST", urlStr, map[string]string{
		// required
		"new_name": "User2",
	}).AddTokenAuth(token)
	MakeRequest(t, req, http.StatusNoContent)

	urlStr = fmt.Sprintf("/api/v1/admin/users/%s/rename", "User2")
	req = NewRequestWithValues(t, "POST", urlStr, map[string]string{
		// required
		"new_name": "User2-2-2",
	}).AddTokenAuth(token)
	MakeRequest(t, req, http.StatusNoContent)

	req = NewRequestWithValues(t, "POST", urlStr, map[string]string{
		// required
		"new_name": "user1",
	}).AddTokenAuth(token)
	// the old user name still be used by with a redirect
	MakeRequest(t, req, http.StatusTemporaryRedirect)

	urlStr = fmt.Sprintf("/api/v1/admin/users/%s/rename", "User2-2-2")
	req = NewRequestWithValues(t, "POST", urlStr, map[string]string{
		// required
		"new_name": "user1",
	}).AddTokenAuth(token)
	MakeRequest(t, req, http.StatusUnprocessableEntity)

	req = NewRequestWithValues(t, "POST", urlStr, map[string]string{
		// required
		"new_name": "user2",
	}).AddTokenAuth(token)
	MakeRequest(t, req, http.StatusNoContent)
}

func TestAPICron(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	// user1 is an admin user
	session := loginUser(t, "user1")

	t.Run("List", func(t *testing.T) {
		defer tests.PrintCurrentTest(t)()

		token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeReadAdmin)

		req := NewRequest(t, "GET", "/api/v1/admin/cron").
			AddTokenAuth(token)
		resp := MakeRequest(t, req, http.StatusOK)

		assert.Equal(t, "30", resp.Header().Get("X-Total-Count"))

		// With execution history disabled (the default) only the original five keys are present.
		rawCrons := DecodeJSON(t, resp, []map[string]any{})
		assert.Len(t, rawCrons, 30)
		for _, raw := range rawCrons {
			assert.Contains(t, raw, "name")
			assert.Contains(t, raw, "schedule")
			assert.Contains(t, raw, "next")
			assert.Contains(t, raw, "prev")
			assert.Contains(t, raw, "exec_times")
			assert.NotContains(t, raw, "running")
			assert.NotContains(t, raw, "consecutive_failures")
			assert.NotContains(t, raw, "last_skipped")
		}

		// The runs endpoint does not exist when history is disabled.
		MakeRequest(t, NewRequest(t, "GET", "/api/v1/admin/cron/archive_cleanup/runs").AddTokenAuth(token), http.StatusNotFound)
	})

	t.Run("Execute", func(t *testing.T) {
		defer tests.PrintCurrentTest(t)()

		now := time.Now()
		token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeWriteAdmin)
		// Archive cleanup is harmless, because in the test environment there are none
		// and is thus an NOOP operation and therefore doesn't interfere with any other
		// tests.
		req := NewRequest(t, "POST", "/api/v1/admin/cron/archive_cleanup").
			AddTokenAuth(token)
		MakeRequest(t, req, http.StatusNoContent)

		// Check for the latest run time for this cron, to ensure it has been run.
		req = NewRequest(t, "GET", "/api/v1/admin/cron").
			AddTokenAuth(token)
		resp := MakeRequest(t, req, http.StatusOK)

		crons := DecodeJSON(t, resp, []api.Cron{})

		for _, cron := range crons {
			if cron.Name == "archive_cleanup" {
				assert.True(t, now.Before(cron.Prev))
			}
		}
	})
}

func TestAPICronHistory(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	session := loginUser(t, "user1")
	writeToken := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeWriteAdmin)
	readToken := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeReadAdmin)

	defer test.MockVariableValue(&setting.Cron.EnableExecutionHistory, true)()
	defer test.MockVariableValue(&setting.Cron.HistoryRetentionCount, int64(100))()
	defer test.MockVariableValue(&setting.Cron.HistoryRetentionDuration, time.Duration(0))()
	defer test.MockVariableValue(&setting.Cron.RunTimeout, time.Duration(0))()

	_, err := db.GetEngine(t.Context()).Exec("DELETE FROM cron_task_run")
	assert.NoError(t, err)

	waitForRunStatus := func(tb testing.TB, taskName, status string) api.CronTaskRun {
		tb.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			req := NewRequest(tb, "GET", "/api/v1/admin/cron/"+taskName+"/runs").AddTokenAuth(readToken)
			runs := DecodeJSON(tb, MakeRequest(tb, req, http.StatusOK), []api.CronTaskRun{})
			for _, run := range runs {
				if run.Status == status {
					return run
				}
			}
			time.Sleep(10 * time.Millisecond)
		}
		tb.Fatalf("run with status %q of task %q did not appear", status, taskName)
		return api.CronTaskRun{}
	}
	findListedCron := func(tb testing.TB, taskName string) api.Cron {
		tb.Helper()
		req := NewRequest(tb, "GET", "/api/v1/admin/cron?limit=100").AddTokenAuth(readToken)
		for _, c := range DecodeJSON(tb, MakeRequest(tb, req, http.StatusOK), []api.Cron{}) {
			if c.Name == taskName {
				return c
			}
		}
		tb.Fatalf("task %q not listed", taskName)
		return api.Cron{}
	}

	t.Run("Skip", func(t *testing.T) {
		defer test.MockVariableValue(&setting.Cron.OverlapPolicy, setting.CronOverlapPolicySkip)()

		entered := make(chan struct{}, 1)
		release := make(chan struct{})
		taskName := "test_cron_history_skip"
		cron_service.RegisterTaskForTest(t, taskName, func(ctx context.Context) error {
			entered <- struct{}{}
			<-release
			return nil
		})

		// The API runs synchronously, so issue the blocking first trigger in the background.
		firstStatus := make(chan int, 1)
		go func() {
			resp := MakeRequest(t, NewRequest(t, "POST", "/api/v1/admin/cron/"+taskName).AddTokenAuth(writeToken), http.StatusNoContent)
			firstStatus <- resp.Code
		}()
		<-entered

		// A colliding manual trigger is accepted (204) and recorded as skipped.
		MakeRequest(t, NewRequest(t, "POST", "/api/v1/admin/cron/"+taskName).AddTokenAuth(writeToken), http.StatusNoContent)
		skipped := waitForRunStatus(t, taskName, "skipped")
		assert.Equal(t, "overlap_skipped", skipped.ErrorCategory)
		assert.Equal(t, "manual", skipped.TriggerType)
		assert.NotEmpty(t, skipped.Message)

		// The list shows the run in progress and the last skip reason.
		listed := findListedCron(t, taskName)
		require.NotNil(t, listed.Running)
		assert.True(t, *listed.Running)
		require.NotNil(t, listed.LastSkipped)
		assert.NotEmpty(t, listed.LastSkipped.Reason)

		// The history page is rendered with the skip row.
		doc := NewHTMLParser(t, session.MakeRequest(t,
			NewRequest(t, "GET", "/-/admin/monitor/cron/"+taskName), http.StatusOK).Body)
		assert.Positive(t, doc.Find("td:contains('Skipped')").Length())

		close(release)
		assert.Equal(t, http.StatusNoContent, <-firstStatus)
		successRun := waitForRunStatus(t, taskName, "success")
		assert.Equal(t, "manual", successRun.TriggerType)
		assert.Equal(t, int64(1), successRun.DoerID) // user1's id
		assert.NotNil(t, successRun.Finished)

		listed = findListedCron(t, taskName)
		require.NotNil(t, listed.Running)
		assert.False(t, *listed.Running)
	})

	t.Run("Reject", func(t *testing.T) {
		defer test.MockVariableValue(&setting.Cron.OverlapPolicy, setting.CronOverlapPolicyReject)()

		entered := make(chan struct{}, 1)
		release := make(chan struct{})
		taskName := "test_cron_history_reject"
		cron_service.RegisterTaskForTest(t, taskName, func(ctx context.Context) error {
			entered <- struct{}{}
			<-release
			return nil
		})

		firstStatus := make(chan int, 1)
		go func() {
			resp := MakeRequest(t, NewRequest(t, "POST", "/api/v1/admin/cron/"+taskName).AddTokenAuth(writeToken), http.StatusNoContent)
			firstStatus <- resp.Code
		}()
		<-entered

		// The colliding API trigger is refused with a conflict status.
		resp := MakeRequest(t, NewRequest(t, "POST", "/api/v1/admin/cron/"+taskName).AddTokenAuth(writeToken), http.StatusConflict)
		apiErr := DecodeJSON(t, resp, map[string]any{})
		assert.Contains(t, apiErr["message"], "already running")

		// The colliding web trigger shows an error flash, not a "started" flash.
		session.MakeRequest(t, NewRequestWithValues(t, "POST", "/-/admin", map[string]string{
			"op":   taskName,
			"from": "monitor",
		}), http.StatusSeeOther)
		assert.Contains(t, session.GetCookieFlashMessage().ErrorMsg, "was not started")

		// Only the single accepted running row exists, no rejected/skipped record.
		runsReq := NewRequest(t, "GET", "/api/v1/admin/cron/"+taskName+"/runs").AddTokenAuth(readToken)
		runs := DecodeJSON(t, MakeRequest(t, runsReq, http.StatusOK), []api.CronTaskRun{})
		assert.Len(t, runs, 1)
		assert.Equal(t, "running", runs[0].Status)

		close(release)
		assert.Equal(t, http.StatusNoContent, <-firstStatus)
		waitForRunStatus(t, taskName, "success")
	})

	// Unknown tasks yield 404 on the runs endpoint.
	req := NewRequest(t, "GET", "/api/v1/admin/cron/no_such_task/runs").AddTokenAuth(readToken)
	MakeRequest(t, req, http.StatusNotFound)

	// The list page carries the new columns while the history endpoint is enabled.
	doc := NewHTMLParser(t, session.MakeRequest(t,
		NewRequest(t, "GET", "/-/admin/monitor/cron"), http.StatusOK).Body)
	assert.Positive(t, doc.Find("th:contains('Consecutive Failures')").Length())
	assert.Positive(t, doc.Find("th:contains('Last Skipped')").Length())
}

func TestAPICreateUser_NotAllowedEmailDomain(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	defer test.MockVariableValue(&setting.Service.EmailDomainAllowList, []glob.Glob{glob.MustCompile("example.org")})()

	adminUsername := "user1"
	token := getUserToken(t, adminUsername, auth_model.AccessTokenScopeWriteAdmin)

	req := NewRequestWithValues(t, "POST", "/api/v1/admin/users", map[string]string{
		"email":                "allowedUser1@example1.org",
		"login_name":           "allowedUser1",
		"username":             "allowedUser1",
		"password":             "allowedUser1_pass",
		"must_change_password": "true",
	}).AddTokenAuth(token)
	resp := MakeRequest(t, req, http.StatusCreated)
	assert.Equal(t, "the domain of user email allowedUser1@example1.org conflicts with EMAIL_DOMAIN_ALLOWLIST or EMAIL_DOMAIN_BLOCKLIST", resp.Header().Get("X-Gitea-Warning"))

	req = NewRequest(t, "DELETE", "/api/v1/admin/users/allowedUser1").AddTokenAuth(token)
	MakeRequest(t, req, http.StatusNoContent)
}

func TestAPIEditUser_NotAllowedEmailDomain(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	defer test.MockVariableValue(&setting.Service.EmailDomainAllowList, []glob.Glob{glob.MustCompile("example.org")})()

	adminUsername := "user1"
	token := getUserToken(t, adminUsername, auth_model.AccessTokenScopeWriteAdmin)
	urlStr := "/api/v1/admin/users/user2"

	req := NewRequestWithJSON(t, "PATCH", urlStr, api.EditUserOption{Email: new("user2@example1.com")}).AddTokenAuth(token)
	resp := MakeRequest(t, req, http.StatusBadRequest)
	errMap := make(map[string]string)
	assert.NoError(t, json.Unmarshal(resp.Body.Bytes(), &errMap))
	assert.Equal(t, "email domain is not allowed: user2@example1.com", errMap["message"])

	req = NewRequestWithJSON(t, "PATCH", urlStr, api.EditUserOption{Email: new("user2@example.org")}).AddTokenAuth(token)
	MakeRequest(t, req, http.StatusOK)
}
