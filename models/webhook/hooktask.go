// Copyright 2017 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package webhook

import (
	"context"
	"errors"
	"time"
	"uuid"

	"gitea.dev/models/db"
	"gitea.dev/modules/json"
	"gitea.dev/modules/log"
	"gitea.dev/modules/optional"
	"gitea.dev/modules/setting"
	"gitea.dev/modules/timeutil"
	webhook_module "gitea.dev/modules/webhook"

	"xorm.io/builder"
)

//   ___ ___                __   ___________              __
//  /   |   \  ____   ____ |  | _\__    ___/____    _____|  | __
// /    ~    \/  _ \ /  _ \|  |/ / |    |  \__  \  /  ___/  |/ /
// \    Y    (  <_> |  <_> )    <  |    |   / __ \_\___ \|    <
//  \___|_  / \____/ \____/|__|_ \ |____|  (____  /____  >__|_ \
//        \/                    \/              \/     \/     \/

// HookRequest represents hook task request information.
type HookRequest struct {
	URL        string            `json:"url"`
	HTTPMethod string            `json:"http_method"`
	Headers    map[string]string `json:"headers"`
	Body       string            `json:"body"`
}

// HookResponse represents hook task response information.
type HookResponse struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
}

// HookTaskStatus is the delivery state of a hook task.
type HookTaskStatus int

const (
	HookTaskStatusPending HookTaskStatus = iota // no delivery attempt yet
	HookTaskStatusSucceed                       // the last attempt succeeded
	HookTaskStatusFailed                        // terminal failure that is never retried
	HookTaskStatusWaiting                       // retryable failure, scheduled again at NextRetryAt
	HookTaskStatusDead                          // automatic retries exhausted
	HookTaskStatusSending                       // an HTTP attempt is in flight
)

// HookTask represents a hook task.
type HookTask struct {
	ID             int64  `xorm:"pk autoincr"`
	HookID         int64  `xorm:"index"`
	UUID           string `xorm:"unique"`
	PayloadContent string `xorm:"LONGTEXT"`
	// PayloadVersion number to allow for smooth version upgrades:
	//  - PayloadVersion 1: PayloadContent contains the JSON as sent to the URL
	//  - PayloadVersion 2: PayloadContent contains the original event
	PayloadVersion int `xorm:"DEFAULT 1"`

	EventType   webhook_module.HookEventType
	IsDelivered bool
	Delivered   timeutil.TimeStampNano

	// History info.
	IsSucceed       bool
	RequestContent  string        `xorm:"LONGTEXT"`
	RequestInfo     *HookRequest  `xorm:"-"`
	ResponseContent string        `xorm:"LONGTEXT"`
	ResponseInfo    *HookResponse `xorm:"-"`

	// Automatic retry state. Retries counts finished automatic redeliveries;
	// operator-triggered redeliveries neither increment nor reset it.
	Status      HookTaskStatus         `xorm:"DEFAULT 0"`
	Retries     int                    `xorm:"DEFAULT 0"`
	NextRetryAt timeutil.TimeStampNano `xorm:"index"` // when a waiting task becomes due, 0 if not scheduled
	LastError   string                 `xorm:"TEXT"`
}

func init() {
	db.RegisterModel(new(HookTask))
}

// BeforeUpdate will be invoked by XORM before updating a record
// representing this object
func (t *HookTask) BeforeUpdate() {
	if t.RequestInfo != nil {
		t.RequestContent = t.simpleMarshalJSON(t.RequestInfo)
	}
	if t.ResponseInfo != nil {
		t.ResponseContent = t.simpleMarshalJSON(t.ResponseInfo)
	}
}

// AfterLoad updates the webhook object upon setting a column
func (t *HookTask) AfterLoad() {
	if len(t.RequestContent) == 0 {
		return
	}

	t.RequestInfo = &HookRequest{}
	if err := json.Unmarshal([]byte(t.RequestContent), t.RequestInfo); err != nil {
		log.Error("Unmarshal RequestContent[%d]: %v", t.ID, err)
	}

	if len(t.ResponseContent) > 0 {
		t.ResponseInfo = &HookResponse{}
		if err := json.Unmarshal([]byte(t.ResponseContent), t.ResponseInfo); err != nil {
			log.Error("Unmarshal ResponseContent[%d]: %v", t.ID, err)
		}
	}
}

func (t *HookTask) simpleMarshalJSON(v any) string {
	p, err := json.Marshal(v)
	if err != nil {
		log.Error("Marshal [%d]: %v", t.ID, err)
	}
	return string(p)
}

// HookTasks returns a list of hook tasks by given conditions.
func HookTasks(ctx context.Context, hookID int64, page int) ([]*HookTask, error) {
	tasks := make([]*HookTask, 0, setting.Webhook.PagingNum)
	return tasks, db.GetEngine(ctx).
		Limit(setting.Webhook.PagingNum, (page-1)*setting.Webhook.PagingNum).
		Where("hook_id=?", hookID).
		Desc("id").
		Find(&tasks)
}

// CreateHookTask creates a new hook task,
// it handles conversion from Payload to PayloadContent.
func CreateHookTask(ctx context.Context, t *HookTask) (*HookTask, error) {
	t.UUID = uuid.New().String()
	if t.Delivered == 0 {
		t.Delivered = timeutil.TimeStampNanoNow()
	}
	if t.PayloadVersion == 0 {
		return nil, errors.New("missing HookTask.PayloadVersion")
	}
	return t, db.Insert(ctx, t)
}

func GetHookTaskByID(ctx context.Context, id int64) (*HookTask, error) {
	t := &HookTask{}

	has, err := db.GetEngine(ctx).ID(id).Get(t)
	if err != nil {
		return nil, err
	}
	if !has {
		return nil, ErrHookTaskNotExist{
			TaskID: id,
		}
	}
	return t, nil
}

// UpdateHookTask updates information of hook task.
func UpdateHookTask(ctx context.Context, t *HookTask) error {
	_, err := db.GetEngine(ctx).ID(t.ID).AllCols().Update(t)
	return err
}

// ReplayHookTask copies a hook task to get re-delivered
func ReplayHookTask(ctx context.Context, hookID int64, uuid string) (*HookTask, error) {
	task, exist, err := db.Get[HookTask](ctx, builder.Eq{"hook_id": hookID, "uuid": uuid})
	if err != nil {
		return nil, err
	} else if !exist {
		return nil, ErrHookTaskNotExist{
			HookID: hookID,
			UUID:   uuid,
		}
	}

	return CreateHookTask(ctx, &HookTask{
		HookID:         task.HookID,
		PayloadContent: task.PayloadContent,
		EventType:      task.EventType,
		PayloadVersion: task.PayloadVersion,
	})
}

// FindUndeliveredHookTaskIDs will find the next 100 undelivered hook tasks with ID greater than the provided lowerID
func FindUndeliveredHookTaskIDs(ctx context.Context, lowerID int64) ([]int64, error) {
	const batchSize = 100

	tasks := make([]int64, 0, batchSize)
	return tasks, db.GetEngine(ctx).
		Select("id").
		Table(new(HookTask)).
		Where("is_delivered=?", false).
		And("id > ?", lowerID).
		Asc("id").
		Limit(batchSize).
		Find(&tasks)
}

func MarkTaskDelivered(ctx context.Context, task *HookTask) (bool, error) {
	count, err := db.GetEngine(ctx).ID(task.ID).Where("is_delivered = ?", false).Cols("is_delivered").Update(&HookTask{
		ID:          task.ID,
		IsDelivered: true,
	})

	return count != 0, err
}

// ClaimPendingHookTask atomically claims a task for its first delivery attempt
// when automatic retries are enabled. Returns false if another worker won the race.
func ClaimPendingHookTask(ctx context.Context, task *HookTask, now timeutil.TimeStampNano) (bool, error) {
	count, err := db.GetEngine(ctx).ID(task.ID).Where("is_delivered = ?", false).
		Cols("is_delivered", "status", "delivered").
		Update(&HookTask{IsDelivered: true, Status: HookTaskStatusSending, Delivered: now})
	return count != 0, err
}

// ClaimDueRetryHookTask atomically claims a waiting task whose backoff is due.
func ClaimDueRetryHookTask(ctx context.Context, task *HookTask, now timeutil.TimeStampNano) (bool, error) {
	count, err := db.GetEngine(ctx).ID(task.ID).
		Where("status = ? AND next_retry_at > 0 AND next_retry_at <= ?", HookTaskStatusWaiting, now).
		Cols("status", "delivered").
		Update(&HookTask{Status: HookTaskStatusSending, Delivered: now})
	return count != 0, err
}

// ClaimStaleSendingHookTask reclaims a sending task whose attempt never wrote
// back a result (e.g. the process crashed mid-delivery).
func ClaimStaleSendingHookTask(ctx context.Context, task *HookTask, now, staleBefore timeutil.TimeStampNano) (bool, error) {
	count, err := db.GetEngine(ctx).ID(task.ID).
		Where("status = ? AND delivered <= ?", HookTaskStatusSending, staleBefore).
		Cols("status", "delivered").
		Update(&HookTask{Status: HookTaskStatusSending, Delivered: now})
	return count != 0, err
}

// FindDueRetryHookTaskIDs finds the next batch of waiting tasks whose backoff is due.
func FindDueRetryHookTaskIDs(ctx context.Context, now timeutil.TimeStampNano, lowerID int64) ([]int64, error) {
	const batchSize = 100

	tasks := make([]int64, 0, batchSize)
	return tasks, db.GetEngine(ctx).
		Select("id").
		Table(new(HookTask)).
		Where("status = ? AND next_retry_at > 0 AND next_retry_at <= ?", HookTaskStatusWaiting, now).
		And("id > ?", lowerID).
		Asc("id").
		Limit(batchSize).
		Find(&tasks)
}

// FindStaleSendingHookTaskIDs finds sending tasks that started before staleBefore.
func FindStaleSendingHookTaskIDs(ctx context.Context, staleBefore timeutil.TimeStampNano, lowerID int64) ([]int64, error) {
	const batchSize = 100

	tasks := make([]int64, 0, batchSize)
	return tasks, db.GetEngine(ctx).
		Select("id").
		Table(new(HookTask)).
		Where("status = ? AND delivered <= ?", HookTaskStatusSending, staleBefore).
		And("id > ?", lowerID).
		Asc("id").
		Limit(batchSize).
		Find(&tasks)
}

// FindHookTasksOptions filters hook tasks for the admin delivery status API.
type FindHookTasksOptions struct {
	db.ListOptions
	HookID    int64
	Status    optional.Option[HookTaskStatus]
	EventType optional.Option[webhook_module.HookEventType]
	Since     timeutil.TimeStampNano // only tasks delivered at or after
	Until     timeutil.TimeStampNano // only tasks delivered at or before
}

func (opts FindHookTasksOptions) ToConds() builder.Cond {
	cond := builder.NewCond()
	if opts.HookID != 0 {
		cond = cond.And(builder.Eq{"hook_id": opts.HookID})
	}
	if opts.Status.Has() {
		cond = cond.And(builder.Eq{"status": opts.Status.Value()})
	}
	if opts.EventType.Has() {
		cond = cond.And(builder.Eq{"event_type": opts.EventType.Value()})
	}
	if opts.Since > 0 {
		cond = cond.And(builder.Gte{"delivered": opts.Since})
	}
	if opts.Until > 0 {
		cond = cond.And(builder.Lte{"delivered": opts.Until})
	}
	return cond
}

func (opts FindHookTasksOptions) ToOrders() string {
	return "id DESC"
}

// FindHookTasks returns hook tasks matching the options together with their total count.
func FindHookTasks(ctx context.Context, opts FindHookTasksOptions) ([]*HookTask, int64, error) {
	return db.FindAndCount[HookTask](ctx, opts)
}

// RequeueDeadHookTasksOptions filters dead tasks for an operator-triggered batch redelivery.
type RequeueDeadHookTasksOptions struct {
	EventType optional.Option[webhook_module.HookEventType]
	Since     timeutil.TimeStampNano
	Until     timeutil.TimeStampNano
}

// RequeueDeadHookTasks resets matching dead tasks to a due waiting state so they
// get redelivered with the same UUID, payload and signature. Only dead rows are
// touched, so repeated or concurrent invocations cannot schedule a task that is
// already queued; the unique queue and the sending claim deduplicate actual sends.
// The automatic retry counter is preserved.
func RequeueDeadHookTasks(ctx context.Context, hookID int64, opts RequeueDeadHookTasksOptions) ([]int64, error) {
	var cond builder.Cond = builder.Eq{"hook_id": hookID, "status": HookTaskStatusDead}
	if opts.EventType.Has() {
		cond = cond.And(builder.Eq{"event_type": opts.EventType.Value()})
	}
	if opts.Since > 0 {
		cond = cond.And(builder.Gte{"delivered": opts.Since})
	}
	if opts.Until > 0 {
		cond = cond.And(builder.Lte{"delivered": opts.Until})
	}

	var taskIDs []int64
	if err := db.WithTx(ctx, func(ctx context.Context) error {
		if err := db.GetEngine(ctx).Table(new(HookTask)).Where(cond).
			Cols("id").Asc("id").Find(&taskIDs); err != nil {
			return err
		}
		if len(taskIDs) == 0 {
			return nil
		}
		_, err := db.GetEngine(ctx).Table(new(HookTask)).Where(cond).
			Cols("status", "next_retry_at").
			Update(&HookTask{Status: HookTaskStatusWaiting, NextRetryAt: timeutil.TimeStampNanoNow()})
		return err
	}); err != nil {
		return nil, err
	}
	return taskIDs, nil
}

// CleanupHookTaskTable deletes rows from hook_task as needed.
func CleanupHookTaskTable(ctx context.Context, cleanupType HookTaskCleanupType, olderThan time.Duration, numberToKeep int) error {
	log.Trace("Doing: CleanupHookTaskTable")

	// A dedicated retention for failed and dead-letter tasks runs in addition to
	// the configured cleanup plan. Zero means the plan below handles them as before.
	if retention := setting.Webhook.RetryFailedRetention; retention > 0 {
		deleteOlderThan := time.Now().Add(-retention).UnixNano()
		deletes, err := db.GetEngine(ctx).
			Where("status IN (?, ?) and delivered < ?", HookTaskStatusFailed, HookTaskStatusDead, deleteOlderThan).
			Delete(new(HookTask))
		if err != nil {
			return err
		}
		log.Trace("Deleted %d failed or dead rows from hook_task", deletes)
	}

	switch cleanupType {
	case OlderThan:
		deleteOlderThan := time.Now().Add(-olderThan).UnixNano()
		// waiting/sending tasks may still receive an attempt; pending tasks are
		// already excluded by the is_delivered check.
		deletes, err := db.GetEngine(ctx).
			Where("is_delivered = ? and delivered < ? and status NOT IN (?, ?)", true, deleteOlderThan,
				HookTaskStatusWaiting, HookTaskStatusSending).
			Delete(new(HookTask))
		if err != nil {
			return err
		}
		log.Trace("Deleted %d rows from hook_task", deletes)
	case PerWebhook:
		hookIDs := make([]int64, 0, 10)
		err := db.GetEngine(ctx).
			Table("webhook").
			Where("id > 0").
			Cols("id").
			Find(&hookIDs)
		if err != nil {
			return err
		}
		for _, hookID := range hookIDs {
			select {
			case <-ctx.Done():
				return db.ErrCancelledf("Before deleting hook_task records for hook id %d", hookID)
			default:
			}
			if err = deleteDeliveredHookTasksByWebhook(ctx, hookID, numberToKeep); err != nil {
				return err
			}
		}
	}
	log.Trace("Finished: CleanupHookTaskTable")
	return nil
}

func deleteDeliveredHookTasksByWebhook(ctx context.Context, hookID int64, numberDeliveriesToKeep int) error {
	log.Trace("Deleting hook_task rows for webhook %d, keeping the most recent %d deliveries", hookID, numberDeliveriesToKeep)
	deliveryDates := make([]int64, 0, 10)
	err := db.GetEngine(ctx).Table("hook_task").
		Where("hook_task.hook_id = ? AND hook_task.is_delivered = ? AND hook_task.delivered is not null "+
			"AND hook_task.status NOT IN (?, ?)", hookID, true,
			HookTaskStatusWaiting, HookTaskStatusSending).
		Cols("hook_task.delivered").
		Join("INNER", "webhook", "hook_task.hook_id = webhook.id").
		OrderBy("hook_task.delivered desc").
		Limit(1, numberDeliveriesToKeep).
		Find(&deliveryDates)
	if err != nil {
		return err
	}

	if len(deliveryDates) > 0 {
		deletes, err := db.GetEngine(ctx).
			Where("hook_id = ? and is_delivered = ? and delivered <= ? and status NOT IN (?, ?)", hookID, true, deliveryDates[0],
				HookTaskStatusWaiting, HookTaskStatusSending).
			Delete(new(HookTask))
		if err != nil {
			return err
		}
		log.Trace("Deleted %d hook_task rows for webhook %d", deletes, hookID)
	} else {
		log.Trace("No hook_task rows to delete for webhook %d", hookID)
	}

	return nil
}
