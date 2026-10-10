// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package setting

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateWebhookRetrySettings(t *testing.T) {
	old := Webhook
	t.Cleanup(func() { Webhook = old })

	valid := func() {
		Webhook.RetryMaxAttempts = 3
		Webhook.RetryBackoff = 30 * time.Second
		Webhook.RetryBackoffMax = time.Hour
		Webhook.RetryJitterFactor = 0.2
		Webhook.RetryFailedRetention = 0
	}

	// disabled (0 attempts) ignores every other value
	Webhook.RetryMaxAttempts = 0
	Webhook.RetryBackoff = -time.Second
	Webhook.RetryJitterFactor = 5
	assert.NoError(t, validateWebhookRetrySettings())

	valid()
	assert.NoError(t, validateWebhookRetrySettings())

	valid()
	Webhook.RetryMaxAttempts = -1
	err := validateWebhookRetrySettings()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "RETRY_MAX_ATTEMPTS")

	valid()
	Webhook.RetryBackoff = 0
	err = validateWebhookRetrySettings()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "RETRY_BACKOFF must")

	valid()
	Webhook.RetryBackoff = 2 * time.Hour
	Webhook.RetryBackoffMax = time.Hour
	err = validateWebhookRetrySettings()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "RETRY_BACKOFF")

	valid()
	Webhook.RetryJitterFactor = -0.1
	err = validateWebhookRetrySettings()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "RETRY_JITTER_FACTOR")

	valid()
	Webhook.RetryJitterFactor = 1.1
	err = validateWebhookRetrySettings()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "RETRY_JITTER_FACTOR")

	valid()
	Webhook.RetryFailedRetention = -time.Hour
	err = validateWebhookRetrySettings()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "RETRY_FAILED_RETENTION")
}
