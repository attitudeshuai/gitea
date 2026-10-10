// Copyright 2017 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package webhook

import (
	"context"
	"testing"
	"time"
	"uuid"

	"gitea.dev/models/db"
	"gitea.dev/models/unittest"
	"gitea.dev/modules/json"
	"gitea.dev/modules/optional"
	"gitea.dev/modules/setting"
	"gitea.dev/modules/timeutil"
	webhook_module "gitea.dev/modules/webhook"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"xorm.io/builder"
)

func prepareWebhookTestData() error {
	if err := unittest.PrepareTestDatabase(); err != nil {
		return err
	}
	var hooks []*Webhook
	hooks = append(hooks, &Webhook{
		RepoID:      1,
		URL:         "https://www.example.com/url1",
		ContentType: ContentTypeJSON,
		Events:      `{"push_only":true,"send_everything":false,"choose_events":false,"events":{"create":false,"push":true,"pull_request":false}}`,
		IsActive:    true,
	})
	hooks = append(hooks, &Webhook{
		RepoID:      1,
		URL:         "https://www.example.com/url2",
		ContentType: ContentTypeJSON,
		Events:      `{}`,
		IsActive:    false,
	})
	hooks = append(hooks, &Webhook{
		OwnerID:     3,
		RepoID:      3,
		URL:         "https://www.example.com/url3",
		ContentType: ContentTypeJSON,
		Events:      `{"push_only":false,"send_everything":false,"choose_events":false,"events":{"create":false,"push":true,"pull_request":true}}`,
		IsActive:    true,
	})
	hooks = append(hooks, &Webhook{
		OwnerID:     3,
		RepoID:      3,
		URL:         "https://www.example.com/url3",
		ContentType: ContentTypeJSON,
		Events:      `{}`,
	})
	hooks = append(hooks, &Webhook{
		RepoID:      2,
		URL:         "https://www.example.com/url4",
		ContentType: ContentTypeJSON,
		Events:      `{"push_only":true,"branch_filter":"{master,feature*}"}`,
		IsActive:    true,
	})
	hooks = append(hooks, &Webhook{
		URL:             "https://www.example.com/system",
		ContentType:     ContentTypeJSON,
		Events:          `{"push_only":true,"branch_filter":"{master,feature*}"}`,
		IsSystemWebhook: true,
	})
	hooks = append(hooks, &Webhook{
		URL:         "https://www.example.com/default",
		ContentType: ContentTypeJSON,
		Events:      `{"push_only":true,"branch_filter":"{master,feature*}"}`,
	})
	ctx := context.Background()
	if err := db.TruncateBeans(ctx, &Webhook{}); err != nil {
		return err
	}
	if err := db.Insert(ctx, hooks); err != nil {
		return err
	}

	hook, _, _ := db.Get[Webhook](ctx, builder.Eq{"repo_id": 1, "is_active": true})
	var tasks []*HookTask
	tasks = append(tasks, &HookTask{HookID: hook.ID, UUID: uuid.New().String()})
	tasks = append(tasks, &HookTask{HookID: hook.ID, UUID: uuid.New().String()})
	tasks = append(tasks, &HookTask{HookID: hook.ID, UUID: uuid.New().String()})
	if err := db.TruncateBeans(ctx, &HookTask{}); err != nil {
		return err
	}
	return db.Insert(ctx, tasks)
}

func TestWebHookContentType(t *testing.T) {
	assert.Equal(t, "json", ContentTypeJSON.Name())
	assert.Equal(t, "form", ContentTypeForm.Name())
	assert.True(t, IsValidHookContentType("json"))
	assert.True(t, IsValidHookContentType("form"))
	assert.False(t, IsValidHookContentType("invalid"))
}

func TestWebhook_History(t *testing.T) {
	hook := unittest.AssertExistsAndLoadBean(t, &Webhook{RepoID: 1, IsActive: true})
	tasks, err := hook.History(t.Context(), 0)
	require.NoError(t, err)
	require.Len(t, tasks, 3)

	hook = unittest.AssertExistsAndLoadBean(t, &Webhook{RepoID: 1, Events: "{}"})
	tasks, err = hook.History(t.Context(), 0)
	assert.NoError(t, err)
	assert.Empty(t, tasks)
}

func TestWebhook_UpdateEvent(t *testing.T) {
	webhook := unittest.AssertExistsAndLoadBean(t, &Webhook{RepoID: 1, IsActive: true})
	hookEvent := &webhook_module.HookEvent{
		PushOnly:       true,
		SendEverything: false,
		ChooseEvents:   false,
		HookEvents: webhook_module.HookEvents{
			webhook_module.HookEventCreate:      false,
			webhook_module.HookEventPush:        true,
			webhook_module.HookEventPullRequest: false,
		},
	}
	webhook.HookEvent = hookEvent
	assert.NoError(t, webhook.UpdateEvent())
	assert.NotEmpty(t, webhook.Events)
	actualHookEvent := &webhook_module.HookEvent{}
	assert.NoError(t, json.Unmarshal([]byte(webhook.Events), actualHookEvent))
	assert.Equal(t, *hookEvent, *actualHookEvent)
}

func TestWebhook_EventsArray(t *testing.T) {
	assert.Equal(t, []string{
		"create", "delete", "fork", "push",
		"issues", "issue_assign", "issue_label", "issue_milestone", "issue_comment",
		"pull_request", "pull_request_assign", "pull_request_label", "pull_request_milestone",
		"pull_request_comment", "pull_request_review_approved", "pull_request_review_rejected",
		"pull_request_review_comment", "pull_request_sync", "pull_request_review_request", "wiki", "repository", "release",
		"package", "status", "workflow_run", "workflow_job",
	},
		(&Webhook{
			HookEvent: &webhook_module.HookEvent{SendEverything: true},
		}).EventsArray(),
	)

	assert.Equal(t, []string{"push"},
		(&Webhook{
			HookEvent: &webhook_module.HookEvent{PushOnly: true},
		}).EventsArray(),
	)
}

func TestCreateWebhook(t *testing.T) {
	hook := &Webhook{
		RepoID:      3,
		URL:         "https://www.example.com/unit_test",
		ContentType: ContentTypeJSON,
		Events:      `{"push_only":false,"send_everything":false,"choose_events":false,"events":{"create":false,"push":true,"pull_request":true}}`,
	}
	unittest.AssertNotExistsBean(t, hook)
	assert.NoError(t, CreateWebhook(t.Context(), hook))
	unittest.AssertExistsAndLoadBean(t, hook)
}

func TestGetWebhookByRepoID(t *testing.T) {
	hook := unittest.AssertExistsAndLoadBean(t, &Webhook{RepoID: 1, IsActive: true})
	loaded, err := GetWebhookByRepoID(t.Context(), 1, hook.ID)
	assert.NoError(t, err)
	assert.Equal(t, hook.ID, loaded.ID)

	_, err = GetWebhookByRepoID(t.Context(), unittest.NonexistentID, unittest.NonexistentID)
	assert.Error(t, err)
	assert.True(t, IsErrWebhookNotExist(err))
}

func TestGetWebhookByOwnerID(t *testing.T) {
	hook := unittest.AssertExistsAndLoadBean(t, &Webhook{OwnerID: 3})
	loaded, err := GetWebhookByOwnerID(t.Context(), 3, hook.ID)
	require.NoError(t, err)
	require.Equal(t, hook.ID, loaded.ID)

	_, err = GetWebhookByOwnerID(t.Context(), unittest.NonexistentID, unittest.NonexistentID)
	assert.Error(t, err)
	assert.True(t, IsErrWebhookNotExist(err))
}

func TestGetActiveWebhooksByRepoID(t *testing.T) {
	hook := unittest.AssertExistsAndLoadBean(t, &Webhook{RepoID: 1, IsActive: true})
	hooks, err := db.Find[Webhook](t.Context(), ListWebhookOptions{RepoID: 1, IsActive: optional.Some(true)})
	require.NoError(t, err)
	require.Len(t, hooks, 1)
	assert.Equal(t, hook.ID, hooks[0].ID)
	assert.True(t, hooks[0].IsActive)
}

func TestGetWebhooksByRepoID(t *testing.T) {
	hooks, err := db.Find[Webhook](t.Context(), ListWebhookOptions{RepoID: 1})
	require.NoError(t, err)
	require.Len(t, hooks, 2)
	assert.Equal(t, int64(1), hooks[0].RepoID)
	assert.True(t, hooks[0].IsActive)
	assert.Equal(t, int64(1), hooks[1].RepoID)
	assert.False(t, hooks[1].IsActive)
}

func TestGetActiveWebhooksByOwnerID(t *testing.T) {
	hooks, err := db.Find[Webhook](t.Context(), ListWebhookOptions{OwnerID: 3, IsActive: optional.Some(true)})
	require.NoError(t, err)
	require.Len(t, hooks, 1)
	assert.Equal(t, int64(3), hooks[0].OwnerID)
	assert.True(t, hooks[0].IsActive)
}

func TestGetWebhooksByOwnerID(t *testing.T) {
	hooks, err := db.Find[Webhook](t.Context(), ListWebhookOptions{OwnerID: 3})
	require.NoError(t, err)
	require.Len(t, hooks, 2)
	assert.Equal(t, int64(3), hooks[0].OwnerID)
	assert.True(t, hooks[0].IsActive)
	assert.Equal(t, int64(3), hooks[1].OwnerID)
	assert.False(t, hooks[1].IsActive)
}

func TestUpdateWebhook(t *testing.T) {
	hook := unittest.AssertExistsAndLoadBean(t, &Webhook{RepoID: 1, Events: `{}`})
	require.False(t, hook.IsActive)
	hook.IsActive = true
	hook.ContentType = ContentTypeForm
	unittest.AssertNotExistsBean(t, hook)
	assert.NoError(t, UpdateWebhook(t.Context(), hook))
	unittest.AssertExistsAndLoadBean(t, hook)
}

func TestDeleteWebhookByRepoID(t *testing.T) {
	hook := unittest.AssertExistsAndLoadBean(t, &Webhook{RepoID: 1, Events: `{}`})
	assert.NoError(t, DeleteWebhookByRepoID(t.Context(), 1, hook.ID))
	unittest.AssertNotExistsBean(t, &Webhook{ID: hook.ID, RepoID: 1})

	err := DeleteWebhookByRepoID(t.Context(), unittest.NonexistentID, unittest.NonexistentID)
	assert.True(t, IsErrWebhookNotExist(err))
}

func TestDeleteWebhookByOwnerID(t *testing.T) {
	hook := unittest.AssertExistsAndLoadBean(t, &Webhook{OwnerID: 3, Events: `{}`})
	assert.NoError(t, DeleteWebhookByOwnerID(t.Context(), 3, hook.ID))
	unittest.AssertNotExistsBean(t, &Webhook{ID: hook.ID, OwnerID: 3})

	err := DeleteWebhookByOwnerID(t.Context(), unittest.NonexistentID, unittest.NonexistentID)
	assert.True(t, IsErrWebhookNotExist(err))
}

func TestHookTasks(t *testing.T) {
	hook := unittest.AssertExistsAndLoadBean(t, &Webhook{RepoID: 1, IsActive: true})
	hookTasks, err := HookTasks(t.Context(), hook.ID, 1)
	assert.NoError(t, err)
	assert.Len(t, hookTasks, 3)
	hookTasks, err = HookTasks(t.Context(), unittest.NonexistentID, 1)
	assert.NoError(t, err)
	assert.Empty(t, hookTasks)
}

func TestCreateHookTask(t *testing.T) {
	hook := unittest.AssertExistsAndLoadBean(t, &Webhook{OwnerID: 3, IsActive: true})
	hookTask := &HookTask{HookID: hook.ID, PayloadVersion: 2}
	unittest.AssertNotExistsBean(t, hookTask)
	_, err := CreateHookTask(t.Context(), hookTask)
	assert.NoError(t, err)
	unittest.AssertExistsAndLoadBean(t, hookTask)
}

func TestUpdateHookTask(t *testing.T) {
	hook := unittest.AssertExistsAndLoadBean(t, &Webhook{OwnerID: 3, IsActive: true})
	hookTask := &HookTask{HookID: hook.ID, PayloadVersion: 2}
	_, err := CreateHookTask(t.Context(), hookTask)
	assert.NoError(t, err)

	hookTask.PayloadContent = "new payload content"
	hookTask.IsDelivered = true
	unittest.AssertNotExistsBean(t, hookTask)
	assert.NoError(t, UpdateHookTask(t.Context(), hookTask))
	unittest.AssertExistsAndLoadBean(t, hookTask)
}

func TestCleanupHookTaskTable_PerWebhook_DeletesDelivered(t *testing.T) {
	hook := &Webhook{RepoID: 3, URL: "https://www.example.com/cleanup1", ContentType: ContentTypeJSON, Events: `{"push_only":true}`}
	require.NoError(t, db.Insert(t.Context(), hook))
	hookTask := &HookTask{
		HookID:         hook.ID,
		IsDelivered:    true,
		Delivered:      timeutil.TimeStampNanoNow(),
		PayloadVersion: 2,
	}
	unittest.AssertNotExistsBean(t, hookTask)
	_, err := CreateHookTask(t.Context(), hookTask)
	assert.NoError(t, err)
	unittest.AssertExistsAndLoadBean(t, hookTask)

	assert.NoError(t, CleanupHookTaskTable(t.Context(), PerWebhook, 168*time.Hour, 0))
	unittest.AssertNotExistsBean(t, hookTask)
}

func TestCleanupHookTaskTable_PerWebhook_LeavesUndelivered(t *testing.T) {
	hook := &Webhook{RepoID: 3, URL: "https://www.example.com/cleanup2", ContentType: ContentTypeJSON, Events: `{"push_only":true}`}
	require.NoError(t, db.Insert(t.Context(), hook))
	hookTask := &HookTask{
		HookID:         hook.ID,
		IsDelivered:    false,
		PayloadVersion: 2,
	}
	unittest.AssertNotExistsBean(t, hookTask)
	_, err := CreateHookTask(t.Context(), hookTask)
	assert.NoError(t, err)
	unittest.AssertExistsAndLoadBean(t, hookTask)

	assert.NoError(t, CleanupHookTaskTable(t.Context(), PerWebhook, 168*time.Hour, 0))
	unittest.AssertExistsAndLoadBean(t, hookTask)
}

func TestCleanupHookTaskTable_PerWebhook_LeavesMostRecentTask(t *testing.T) {
	hook := &Webhook{RepoID: 3, URL: "https://www.example.com/cleanup3", ContentType: ContentTypeJSON, Events: `{"push_only":true}`}
	require.NoError(t, db.Insert(t.Context(), hook))
	hookTask := &HookTask{
		HookID:         hook.ID,
		IsDelivered:    true,
		Delivered:      timeutil.TimeStampNanoNow(),
		PayloadVersion: 2,
	}
	unittest.AssertNotExistsBean(t, hookTask)
	_, err := CreateHookTask(t.Context(), hookTask)
	assert.NoError(t, err)
	unittest.AssertExistsAndLoadBean(t, hookTask)

	assert.NoError(t, CleanupHookTaskTable(t.Context(), PerWebhook, 168*time.Hour, 1))
	unittest.AssertExistsAndLoadBean(t, hookTask)
}

func TestCleanupHookTaskTable_OlderThan_DeletesDelivered(t *testing.T) {
	hook := &Webhook{RepoID: 3, URL: "https://www.example.com/cleanup4", ContentType: ContentTypeJSON, Events: `{"push_only":true}`}
	require.NoError(t, db.Insert(t.Context(), hook))
	hookTask := &HookTask{
		HookID:         hook.ID,
		IsDelivered:    true,
		Delivered:      timeutil.TimeStampNano(time.Now().AddDate(0, 0, -8).UnixNano()),
		PayloadVersion: 2,
	}
	unittest.AssertNotExistsBean(t, hookTask)
	_, err := CreateHookTask(t.Context(), hookTask)
	assert.NoError(t, err)
	unittest.AssertExistsAndLoadBean(t, hookTask)

	assert.NoError(t, CleanupHookTaskTable(t.Context(), OlderThan, 168*time.Hour, 0))
	unittest.AssertNotExistsBean(t, hookTask)
}

func TestCleanupHookTaskTable_OlderThan_LeavesUndelivered(t *testing.T) {
	hook := &Webhook{RepoID: 3, URL: "https://www.example.com/cleanup5", ContentType: ContentTypeJSON, Events: `{"push_only":true}`}
	require.NoError(t, db.Insert(t.Context(), hook))
	hookTask := &HookTask{
		HookID:         hook.ID,
		IsDelivered:    false,
		PayloadVersion: 2,
	}
	unittest.AssertNotExistsBean(t, hookTask)
	_, err := CreateHookTask(t.Context(), hookTask)
	assert.NoError(t, err)
	unittest.AssertExistsAndLoadBean(t, hookTask)

	assert.NoError(t, CleanupHookTaskTable(t.Context(), OlderThan, 168*time.Hour, 0))
	unittest.AssertExistsAndLoadBean(t, hookTask)
}

func TestCleanupHookTaskTable_OlderThan_LeavesTaskEarlierThanAgeToDelete(t *testing.T) {
	hook := &Webhook{RepoID: 3, URL: "https://www.example.com/cleanup6", ContentType: ContentTypeJSON, Events: `{"push_only":true}`}
	require.NoError(t, db.Insert(t.Context(), hook))
	hookTask := &HookTask{
		HookID:         hook.ID,
		IsDelivered:    true,
		Delivered:      timeutil.TimeStampNano(time.Now().AddDate(0, 0, -6).UnixNano()),
		PayloadVersion: 2,
	}
	unittest.AssertNotExistsBean(t, hookTask)
	_, err := CreateHookTask(t.Context(), hookTask)
	assert.NoError(t, err)
	unittest.AssertExistsAndLoadBean(t, hookTask)

	assert.NoError(t, CleanupHookTaskTable(t.Context(), OlderThan, 168*time.Hour, 0))
	unittest.AssertExistsAndLoadBean(t, hookTask)
}

func createRetryHookTask(t *testing.T, hookID int64, event webhook_module.HookEventType, status HookTaskStatus,
	retries int, delivered timeutil.TimeStampNano,
) *HookTask {
	t.Helper()
	task := &HookTask{
		HookID:         hookID,
		EventType:      event,
		IsDelivered:    true,
		Delivered:      delivered,
		Status:         status,
		Retries:        retries,
		PayloadVersion: 2,
	}
	if status == HookTaskStatusWaiting {
		task.NextRetryAt = delivered
	}
	_, err := CreateHookTask(t.Context(), task)
	require.NoError(t, err)
	return task
}

func TestFindHookTasks(t *testing.T) {
	assert.NoError(t, unittest.PrepareTestDatabase())
	hook := &Webhook{RepoID: 3, URL: "https://www.example.com/retry-query", ContentType: ContentTypeJSON, Events: `{"push_only":true}`}
	require.NoError(t, db.Insert(t.Context(), hook))

	old := timeutil.TimeStampNano(time.Now().Add(-2 * time.Hour).UnixNano())
	now := timeutil.TimeStampNanoNow()
	deadPush := createRetryHookTask(t, hook.ID, webhook_module.HookEventPush, HookTaskStatusDead, 3, old)
	deadCreate := createRetryHookTask(t, hook.ID, webhook_module.HookEventCreate, HookTaskStatusDead, 1, old)
	succeedPush := createRetryHookTask(t, hook.ID, webhook_module.HookEventPush, HookTaskStatusSucceed, 0, now)

	tasks, total, err := FindHookTasks(t.Context(), FindHookTasksOptions{
		ListOptions: db.ListOptions{ListAll: true},
		HookID:      hook.ID,
	})
	require.NoError(t, err)
	assert.EqualValues(t, 3, total)
	assert.Len(t, tasks, 3)

	tasks, total, err = FindHookTasks(t.Context(), FindHookTasksOptions{
		ListOptions: db.ListOptions{ListAll: true},
		HookID:      hook.ID,
		Status:      optional.Some(HookTaskStatusDead),
	})
	require.NoError(t, err)
	assert.EqualValues(t, 2, total)
	assert.ElementsMatch(t, []int64{deadPush.ID, deadCreate.ID}, []int64{tasks[0].ID, tasks[1].ID})

	tasks, _, err = FindHookTasks(t.Context(), FindHookTasksOptions{
		ListOptions: db.ListOptions{ListAll: true},
		HookID:      hook.ID,
		Status:      optional.Some(HookTaskStatusDead),
		EventType:   optional.Some(webhook_module.HookEventPush),
	})
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	assert.Equal(t, deadPush.ID, tasks[0].ID)

	tasks, _, err = FindHookTasks(t.Context(), FindHookTasksOptions{
		ListOptions: db.ListOptions{ListAll: true},
		HookID:      hook.ID,
		Since:       now,
	})
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	assert.Equal(t, succeedPush.ID, tasks[0].ID)
}

func TestClaimRetryHookTasks(t *testing.T) {
	assert.NoError(t, unittest.PrepareTestDatabase())
	now := timeutil.TimeStampNanoNow()

	// first claim
	pending := &HookTask{HookID: 1, EventType: webhook_module.HookEventPush, PayloadVersion: 2}
	_, err := CreateHookTask(t.Context(), pending)
	require.NoError(t, err)
	claimed, err := ClaimPendingHookTask(t.Context(), pending, now)
	require.NoError(t, err)
	assert.True(t, claimed)
	claimed, err = ClaimPendingHookTask(t.Context(), pending, now)
	require.NoError(t, err)
	assert.False(t, claimed)
	pending = unittest.AssertExistsAndLoadBean(t, &HookTask{ID: pending.ID})
	assert.Equal(t, HookTaskStatusSending, pending.Status)
	assert.True(t, pending.IsDelivered)

	// due retry claim
	waiting := &HookTask{
		HookID: 1, EventType: webhook_module.HookEventPush, IsDelivered: true,
		Status: HookTaskStatusWaiting, NextRetryAt: now - 1, Delivered: now - 10, PayloadVersion: 2,
	}
	_, err = CreateHookTask(t.Context(), waiting)
	require.NoError(t, err)
	claimed, err = ClaimDueRetryHookTask(t.Context(), waiting, now)
	require.NoError(t, err)
	assert.True(t, claimed)
	assert.True(t, waiting.IsDelivered, "retry CAS must not touch is_delivered")

	// future-due task cannot be claimed
	future := &HookTask{
		HookID: 1, EventType: webhook_module.HookEventPush, IsDelivered: true,
		Status: HookTaskStatusWaiting, NextRetryAt: now + 1000, Delivered: now, PayloadVersion: 2,
	}
	_, err = CreateHookTask(t.Context(), future)
	require.NoError(t, err)
	claimed, err = ClaimDueRetryHookTask(t.Context(), future, now)
	require.NoError(t, err)
	assert.False(t, claimed)

	// only a stale sending task can be reclaimed
	stale := &HookTask{
		HookID: 1, EventType: webhook_module.HookEventPush, IsDelivered: true,
		Status: HookTaskStatusSending, Delivered: now - 1000, PayloadVersion: 2,
	}
	_, err = CreateHookTask(t.Context(), stale)
	require.NoError(t, err)
	claimed, err = ClaimStaleSendingHookTask(t.Context(), stale, now, now-100)
	require.NoError(t, err)
	assert.True(t, claimed)
	recent := &HookTask{
		HookID: 1, EventType: webhook_module.HookEventPush, IsDelivered: true,
		Status: HookTaskStatusSending, Delivered: now, PayloadVersion: 2,
	}
	_, err = CreateHookTask(t.Context(), recent)
	require.NoError(t, err)
	claimed, err = ClaimStaleSendingHookTask(t.Context(), recent, now, now-100)
	require.NoError(t, err)
	assert.False(t, claimed)
}

func TestRequeueDeadHookTasks(t *testing.T) {
	assert.NoError(t, unittest.PrepareTestDatabase())
	hook := &Webhook{RepoID: 3, URL: "https://www.example.com/retry-requeue", ContentType: ContentTypeJSON, Events: `{"push_only":true}`}
	require.NoError(t, db.Insert(t.Context(), hook))
	old := timeutil.TimeStampNanoNow() - 1000
	deadPush := createRetryHookTask(t, hook.ID, webhook_module.HookEventPush, HookTaskStatusDead, 3, old)
	deadCreate := createRetryHookTask(t, hook.ID, webhook_module.HookEventCreate, HookTaskStatusDead, 1, old)
	createRetryHookTask(t, hook.ID, webhook_module.HookEventPush, HookTaskStatusSucceed, 0, old)

	ids, err := RequeueDeadHookTasks(t.Context(), hook.ID, RequeueDeadHookTasksOptions{
		EventType: optional.Some(webhook_module.HookEventPush),
	})
	require.NoError(t, err)
	require.Equal(t, []int64{deadPush.ID}, ids)
	row := unittest.AssertExistsAndLoadBean(t, &HookTask{ID: deadPush.ID})
	assert.Equal(t, HookTaskStatusWaiting, row.Status)
	assert.Equal(t, 3, row.Retries, "automatic retry counter must be preserved")
	assert.Positive(t, int64(row.NextRetryAt))

	// the create event is untouched and a second call has nothing to requeue
	unittest.AssertExistsAndLoadBean(t, &HookTask{ID: deadCreate.ID, Status: HookTaskStatusDead})
	ids, err = RequeueDeadHookTasks(t.Context(), hook.ID, RequeueDeadHookTasksOptions{})
	require.NoError(t, err)
	require.Equal(t, []int64{deadCreate.ID}, ids)
	ids, err = RequeueDeadHookTasks(t.Context(), hook.ID, RequeueDeadHookTasksOptions{})
	require.NoError(t, err)
	assert.Empty(t, ids)
}

func TestCleanupHookTaskTable_RetryStates(t *testing.T) {
	assert.NoError(t, unittest.PrepareTestDatabase())
	hook := &Webhook{RepoID: 3, URL: "https://www.example.com/retry-cleanup", ContentType: ContentTypeJSON, Events: `{"push_only":true}`}
	require.NoError(t, db.Insert(t.Context(), hook))

	old := timeutil.TimeStampNano(time.Now().Add(-8 * 24 * time.Hour).UnixNano())
	recent := timeutil.TimeStampNanoNow()
	oldDead := createRetryHookTask(t, hook.ID, webhook_module.HookEventPush, HookTaskStatusDead, 3, old)
	oldWaiting := createRetryHookTask(t, hook.ID, webhook_module.HookEventPush, HookTaskStatusWaiting, 1, old)
	oldSucceed := createRetryHookTask(t, hook.ID, webhook_module.HookEventPush, HookTaskStatusSucceed, 0, old)
	recentFailed := createRetryHookTask(t, hook.ID, webhook_module.HookEventPush, HookTaskStatusFailed, 0, recent)

	// the standard plan must not delete tasks that are still being retried
	require.NoError(t, CleanupHookTaskTable(t.Context(), OlderThan, time.Hour, 0))
	unittest.AssertExistsAndLoadBean(t, &HookTask{ID: oldWaiting.ID})
	unittest.AssertNotExistsBean(t, &HookTask{ID: oldDead.ID})
	unittest.AssertNotExistsBean(t, &HookTask{ID: oldSucceed.ID})
	unittest.AssertExistsAndLoadBean(t, &HookTask{ID: recentFailed.ID})
}

func TestCleanupHookTaskTable_FailedRetention(t *testing.T) {
	assert.NoError(t, unittest.PrepareTestDatabase())
	oldRetention := setting.Webhook.RetryFailedRetention
	setting.Webhook.RetryFailedRetention = 48 * time.Hour
	t.Cleanup(func() { setting.Webhook.RetryFailedRetention = oldRetention })

	hook := &Webhook{RepoID: 3, URL: "https://www.example.com/retry-retention", ContentType: ContentTypeJSON, Events: `{"push_only":true}`}
	require.NoError(t, db.Insert(t.Context(), hook))

	old := timeutil.TimeStampNano(time.Now().Add(-8 * 24 * time.Hour).UnixNano())
	recent := timeutil.TimeStampNanoNow()
	oldDead := createRetryHookTask(t, hook.ID, webhook_module.HookEventPush, HookTaskStatusDead, 3, old)
	oldFailed := createRetryHookTask(t, hook.ID, webhook_module.HookEventPush, HookTaskStatusFailed, 0, old)
	oldWaiting := createRetryHookTask(t, hook.ID, webhook_module.HookEventPush, HookTaskStatusWaiting, 1, old)
	oldSucceed := createRetryHookTask(t, hook.ID, webhook_module.HookEventPush, HookTaskStatusSucceed, 0, old)
	recentDead := createRetryHookTask(t, hook.ID, webhook_module.HookEventCreate, HookTaskStatusDead, 3, recent)

	// a very long general plan, only the failed/dead retention should delete
	require.NoError(t, CleanupHookTaskTable(t.Context(), OlderThan, 365*24*time.Hour, 0))
	unittest.AssertNotExistsBean(t, &HookTask{ID: oldDead.ID})
	unittest.AssertNotExistsBean(t, &HookTask{ID: oldFailed.ID})
	unittest.AssertExistsAndLoadBean(t, &HookTask{ID: oldWaiting.ID})
	unittest.AssertExistsAndLoadBean(t, &HookTask{ID: oldSucceed.ID})
	unittest.AssertExistsAndLoadBean(t, &HookTask{ID: recentDead.ID})
}
