// Copyright 2019 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	user_model "gitea.dev/models/user"
	webhook_model "gitea.dev/models/webhook"
	"gitea.dev/modules/egress"
	"gitea.dev/modules/graceful"
	"gitea.dev/modules/log"
	"gitea.dev/modules/process"
	"gitea.dev/modules/queue"
	"gitea.dev/modules/setting"
	"gitea.dev/modules/timeutil"
	"gitea.dev/modules/util"
	webhook_module "gitea.dev/modules/webhook"
)

func newDefaultRequest(ctx context.Context, w *webhook_model.Webhook, t *webhook_model.HookTask) (req *http.Request, body []byte, err error) {
	switch w.HTTPMethod {
	case "", http.MethodPost:
		switch w.ContentType {
		case webhook_model.ContentTypeJSON:
			req, err = http.NewRequest(http.MethodPost, w.URL, strings.NewReader(t.PayloadContent))
			if err != nil {
				return nil, nil, err
			}

			req.Header.Set("Content-Type", "application/json")
		case webhook_model.ContentTypeForm:
			forms := url.Values{
				"payload": []string{t.PayloadContent},
			}

			req, err = http.NewRequest(http.MethodPost, w.URL, strings.NewReader(forms.Encode()))
			if err != nil {
				return nil, nil, err
			}

			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		default:
			return nil, nil, fmt.Errorf("invalid content type: %v", w.ContentType)
		}
	case http.MethodGet:
		u, err := url.Parse(w.URL)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid URL: %w", err)
		}
		vals := u.Query()
		vals["payload"] = []string{t.PayloadContent}
		u.RawQuery = vals.Encode()
		req, err = http.NewRequest(http.MethodGet, u.String(), nil)
		if err != nil {
			return nil, nil, err
		}
	case http.MethodPut:
		switch w.Type {
		case webhook_module.MATRIX: // used when t.Version == 1
			txnID, err := getMatrixTxnID([]byte(t.PayloadContent))
			if err != nil {
				return nil, nil, err
			}
			url := fmt.Sprintf("%s/%s", w.URL, url.PathEscape(txnID))
			req, err = http.NewRequest(http.MethodPut, url, strings.NewReader(t.PayloadContent))
			if err != nil {
				return nil, nil, err
			}
		default:
			return nil, nil, fmt.Errorf("invalid http method: %v", w.HTTPMethod)
		}
	default:
		return nil, nil, fmt.Errorf("invalid http method: %v", w.HTTPMethod)
	}

	body = []byte(t.PayloadContent)
	return req, body, addDefaultHeaders(req, []byte(w.Secret), w, t, body)
}

func addDefaultHeaders(req *http.Request, secret []byte, w *webhook_model.Webhook, t *webhook_model.HookTask, payloadContent []byte) error {
	var signatureSHA1 string
	var signatureSHA256 string
	if len(secret) > 0 {
		sig1 := hmac.New(sha1.New, secret)
		sig256 := hmac.New(sha256.New, secret)
		_, err := io.MultiWriter(sig1, sig256).Write(payloadContent)
		if err != nil {
			// this error should never happen, since the hashes are writing to []byte and always return a nil error.
			return fmt.Errorf("prepareWebhooks.sigWrite: %w", err)
		}
		signatureSHA1 = hex.EncodeToString(sig1.Sum(nil))
		signatureSHA256 = hex.EncodeToString(sig256.Sum(nil))
	}

	event := t.EventType.Event()
	eventType := string(t.EventType)
	targetType := "default"
	if w.IsSystemWebhook {
		targetType = "system"
	} else if w.RepoID != 0 {
		targetType = "repository"
	} else if w.OwnerID != 0 {
		owner, err := user_model.GetUserByID(req.Context(), w.OwnerID)
		if owner != nil && err == nil {
			if owner.IsOrganization() {
				targetType = "organization"
			} else {
				targetType = "user"
			}
		}
	}

	req.Header.Add("X-Gitea-Delivery", t.UUID)
	req.Header.Add("X-Gitea-Event", event)
	req.Header.Add("X-Gitea-Event-Type", eventType)
	req.Header.Add("X-Gitea-Signature", signatureSHA256)
	req.Header.Add("X-Gitea-Hook-Installation-Target-Type", targetType)
	req.Header.Add("X-Gogs-Delivery", t.UUID)
	req.Header.Add("X-Gogs-Event", event)
	req.Header.Add("X-Gogs-Event-Type", eventType)
	req.Header.Add("X-Gogs-Signature", signatureSHA256)
	req.Header.Add("X-Hub-Signature", "sha1="+signatureSHA1)
	req.Header.Add("X-Hub-Signature-256", "sha256="+signatureSHA256)
	req.Header["X-GitHub-Delivery"] = []string{t.UUID}
	req.Header["X-GitHub-Event"] = []string{event}
	req.Header["X-GitHub-Event-Type"] = []string{eventType}
	req.Header["X-GitHub-Hook-Installation-Target-Type"] = []string{targetType}
	return nil
}

// prepareHookRequest creates the [http.Request] (depending on the webhook type)
// and records the request information, including the masked Authorization header.
func prepareHookRequest(ctx context.Context, w *webhook_model.Webhook, t *webhook_model.HookTask) (*http.Request, error) {
	newRequest := webhookRequesters[w.Type]
	if t.PayloadVersion == 1 || newRequest == nil {
		newRequest = newDefaultRequest
	}

	req, body, err := newRequest(ctx, w, t)
	if err != nil {
		return nil, fmt.Errorf("cannot create http request for webhook %s[%d %s]: %w", w.Type, w.ID, w.URL, err)
	}

	// Record delivery information.
	t.RequestInfo = &webhook_model.HookRequest{
		URL:        req.URL.String(),
		HTTPMethod: req.Method,
		Headers:    map[string]string{},
		Body:       string(body),
	}
	for k, vals := range req.Header {
		t.RequestInfo.Headers[k] = strings.Join(vals, ",")
	}

	// Add Authorization Header
	authorization, err := w.HeaderAuthorization()
	if err != nil {
		return nil, fmt.Errorf("cannot get Authorization header for webhook %s[%d %s]: %w", w.Type, w.ID, w.URL, err)
	}
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
		t.RequestInfo.Headers["Authorization"] = "******"
	}
	return req, nil
}

// sendHookRequest performs the HTTP request and records the response information.
func sendHookRequest(ctx context.Context, w *webhook_model.Webhook, t *webhook_model.HookTask, req *http.Request) error {
	resp, err := webhookHTTPClient.Do(req.WithContext(ctx))
	if err != nil {
		t.ResponseInfo.Body = fmt.Sprintf("Delivery: %v", err)
		return fmt.Errorf("unable to deliver webhook task[%d] in %s due to error in http client: %w", t.ID, w.URL, err)
	}
	defer resp.Body.Close()

	// Status code is 20x can be seen as succeed.
	t.IsSucceed = resp.StatusCode/100 == 2
	t.ResponseInfo.Status = resp.StatusCode
	for k, vals := range resp.Header {
		t.ResponseInfo.Headers[k] = strings.Join(vals, ",")
	}

	p, err := util.ReadWithLimit(resp.Body, 1024*1024)
	if err != nil {
		t.ResponseInfo.Body = fmt.Sprintf("read body: %s", err)
		return fmt.Errorf("unable to deliver webhook task[%d] in %s as unable to read response body: %w", t.ID, w.URL, err)
	}
	t.ResponseInfo.Body = string(p)
	return nil
}

// Deliver creates the [http.Request] (depending on the webhook type), sends it
// and records the status and response.
func Deliver(ctx context.Context, t *webhook_model.HookTask) error {
	w, err := webhook_model.GetWebhookByID(ctx, t.HookID)
	if err != nil {
		return err
	}

	defer func() {
		err := recover()
		if err == nil {
			return
		}
		// There was a panic whilst delivering a hook...
		log.Error("PANIC whilst trying to deliver webhook task[%d] to webhook %s Panic: %v\nStacktrace: %s", t.ID, w.URL, err, log.Stack(2))
	}()

	if retryEnabled() {
		return deliverWithRetry(ctx, w, t)
	}
	return deliverLegacy(ctx, w, t)
}

// deliverLegacy is the single-attempt delivery path used when automatic retries
// are disabled. Its behavior must stay identical to the historical behavior.
func deliverLegacy(ctx context.Context, w *webhook_model.Webhook, t *webhook_model.HookTask) error {
	t.IsDelivered = true

	req, err := prepareHookRequest(ctx, w, t)
	if err != nil {
		return err
	}

	t.ResponseInfo = &webhook_model.HookResponse{
		Headers: map[string]string{},
	}

	// OK We're now ready to attempt to deliver the task - we must double check that it
	// has not been delivered in the meantime
	updated, err := webhook_model.MarkTaskDelivered(ctx, t)
	if err != nil {
		log.Error("MarkTaskDelivered[%d]: %v", t.ID, err)
		return fmt.Errorf("unable to mark task[%d] delivered in the db: %w", t.ID, err)
	}
	if !updated {
		// This webhook task has already been attempted to be delivered or is in the process of being delivered
		log.Trace("Webhook Task[%d] already delivered", t.ID)
		return nil
	}

	// All code from this point will update the hook task
	defer func() {
		t.Delivered = timeutil.TimeStampNanoNow()
		if t.IsSucceed {
			t.Status = webhook_model.HookTaskStatusSucceed
			log.Trace("Hook delivered: %s", t.UUID)
		} else if !w.IsActive {
			t.Status = webhook_model.HookTaskStatusFailed
			log.Trace("Hook delivery skipped as webhook is inactive: %s", t.UUID)
		} else {
			t.Status = webhook_model.HookTaskStatusFailed
			log.Trace("Hook delivery failed: %s", t.UUID)
		}

		if err := webhook_model.UpdateHookTask(ctx, t); err != nil {
			log.Error("UpdateHookTask [%d]: %v", t.ID, err)
		}

		// Update webhook last delivery status.
		if t.IsSucceed {
			w.LastStatus = webhook_module.HookStatusSucceed
		} else {
			w.LastStatus = webhook_module.HookStatusFail
		}
		if err = webhook_model.UpdateWebhookLastStatus(ctx, w); err != nil {
			log.Error("UpdateWebhookLastStatus: %v", err)
			return
		}
	}()

	if setting.DisableWebhooks {
		return fmt.Errorf("webhook task skipped (webhooks disabled): [%d]", t.ID)
	}

	if !w.IsActive {
		log.Trace("Webhook %s in Webhook Task[%d] is not active", w.URL, t.ID)
		return nil
	}

	return sendHookRequest(ctx, w, t, req)
}

// deliverWithRetry delivers one attempt of a task in the retry state machine.
// The attempt is claimed in the database first, so concurrent workers or several
// instances can never send the same attempt twice.
func deliverWithRetry(ctx context.Context, w *webhook_model.Webhook, t *webhook_model.HookTask) error {
	now := timeutil.TimeStampNanoNow()
	claimed, err := claimHookTask(ctx, t, now)
	if err != nil {
		log.Error("claimHookTask[%d]: %v", t.ID, err)
		return fmt.Errorf("unable to claim hook task[%d] for delivery: %w", t.ID, err)
	}
	if !claimed {
		log.Trace("Webhook Task[%d] is not due for delivery", t.ID)
		return nil
	}
	t.IsDelivered = true
	t.Status = webhook_model.HookTaskStatusSending

	// attemptErr is nil only after a 2xx response. retryable decides between
	// waiting/dead and the permanent failed state.
	var attemptErr error
	var retryable bool

	defer func() {
		t.Delivered = timeutil.TimeStampNanoNow()
		if attemptErr == nil {
			t.IsSucceed = true
			t.Status = webhook_model.HookTaskStatusSucceed
			t.NextRetryAt = 0
			t.LastError = ""
			log.Trace("Hook delivered: %s", t.UUID)
		} else {
			t.IsSucceed = false
			t.LastError = attemptErr.Error()
			if retryable && t.Retries < setting.Webhook.RetryMaxAttempts {
				t.Retries++
				t.Status = webhook_model.HookTaskStatusWaiting
				t.NextRetryAt = t.Delivered + timeutil.TimeStampNano(nextRetryDelay(t.Retries))
				log.Trace("Hook delivery failed, retry %d/%d scheduled for %s: %v", t.Retries, setting.Webhook.RetryMaxAttempts, t.UUID, attemptErr)
			} else {
				t.NextRetryAt = 0
				if retryable {
					t.Status = webhook_model.HookTaskStatusDead
					log.Trace("Hook delivery dead after %d retries: %s", t.Retries, t.UUID)
				} else {
					t.Status = webhook_model.HookTaskStatusFailed
					if !w.IsActive {
						log.Trace("Hook delivery skipped as webhook is inactive: %s", t.UUID)
					} else {
						log.Trace("Hook delivery failed permanently: %s: %v", t.UUID, attemptErr)
					}
				}
			}
		}

		if err := webhook_model.UpdateHookTask(ctx, t); err != nil {
			log.Error("UpdateHookTask [%d]: %v", t.ID, err)
		}

		// Update webhook last delivery status.
		if t.IsSucceed {
			w.LastStatus = webhook_module.HookStatusSucceed
		} else {
			w.LastStatus = webhook_module.HookStatusFail
		}
		if err = webhook_model.UpdateWebhookLastStatus(ctx, w); err != nil {
			log.Error("UpdateWebhookLastStatus: %v", err)
			return
		}
	}()

	req, err := prepareHookRequest(ctx, w, t)
	if err != nil {
		attemptErr = err // the request itself is invalid, never retry
		return err
	}

	t.ResponseInfo = &webhook_model.HookResponse{
		Headers: map[string]string{},
	}

	if setting.DisableWebhooks {
		attemptErr = fmt.Errorf("webhook task skipped (webhooks disabled): [%d]", t.ID)
		return attemptErr
	}

	if !w.IsActive {
		log.Trace("Webhook %s in Webhook Task[%d] is not active", w.URL, t.ID)
		attemptErr = errors.New("webhook is inactive")
		return nil
	}

	sendErr := sendHookRequest(ctx, w, t, req)
	if sendErr != nil {
		// Any transport-level failure (connection refused/reset, timeout, DNS,
		// redirect errors, egress policy denial) is worth retrying.
		attemptErr = sendErr
		retryable = true
		return sendErr
	}
	if !t.IsSucceed {
		retryable, attemptErr = classifyHookTaskStatus(t.ResponseInfo.Status)
	}
	return nil
}

// classifyHookTaskStatus maps a receiver response status to the retry decision:
// 408/429 and 5xx are temporary, every other non-2xx status is an explicit rejection.
func classifyHookTaskStatus(status int) (retryable bool, err error) {
	if status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || status >= 500 {
		return true, fmt.Errorf("received non-successful HTTP status %d", status)
	}
	return false, fmt.Errorf("received non-retryable HTTP status %d", status)
}

var webhookHTTPClient *http.Client

// Init starts the hooks delivery thread
func Init() error {
	timeout := time.Duration(setting.Webhook.DeliverTimeout) * time.Second

	transport := egress.NewWebhookPolicy().NewHTTPTransport()
	transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: setting.Webhook.SkipTLSVerify}
	webhookHTTPClient = &http.Client{Timeout: timeout, Transport: transport}

	hookQueue = queue.CreateUniqueQueue(graceful.GetManager().ShutdownContext(), "webhook_sender", handler)
	if hookQueue == nil {
		return errors.New("unable to create webhook_sender queue")
	}
	go graceful.GetManager().RunWithCancel(hookQueue)

	go graceful.GetManager().RunWithShutdownContext(populateWebhookSendingQueue)

	if retryEnabled() {
		go graceful.GetManager().RunWithShutdownContext(populateRetrySendingQueue)
		go graceful.GetManager().RunWithShutdownContext(retryScheduleLoop)
	}

	return nil
}

func enqueueHookTaskBatches(ctx context.Context, processName string, find func(lowerID int64) ([]int64, error)) {
	ctx, _, finished := process.GetManager().AddContext(ctx, processName)
	defer finished()

	lowerID := int64(0)
	for {
		taskIDs, err := find(lowerID)
		if err != nil {
			log.Error("Unable to populate webhook queue as %s failed: %v", processName, err)
			return
		}
		if len(taskIDs) == 0 {
			return
		}
		lowerID = taskIDs[len(taskIDs)-1]

		for _, taskID := range taskIDs {
			select {
			case <-ctx.Done():
				log.Warn("Shutdown before Webhook Sending queue finishing being populated")
				return
			default:
			}
			if err := enqueueHookTask(taskID); err != nil {
				log.Error("Unable to push HookTask[%d] to the Webhook Sending queue: %v", taskID, err)
			}
		}
	}
}

func populateWebhookSendingQueue(ctx context.Context) {
	enqueueHookTaskBatches(ctx, "Webhook: Populate sending queue", func(lowerID int64) ([]int64, error) {
		return webhook_model.FindUndeliveredHookTaskIDs(ctx, lowerID)
	})
}
