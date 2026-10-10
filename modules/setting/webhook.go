// Copyright 2019 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package setting

import (
	"fmt"
	"net/url"
	"strconv"
	"time"

	"gitea.dev/modules/log"
)

// Webhook settings
var Webhook = struct {
	QueueLength     int
	DeliverTimeout  int
	SkipTLSVerify   bool
	AllowedHostList string
	Types           []string
	PagingNum       int
	ProxyURL        string
	ProxyURLFixed   *url.URL
	ProxyHosts      []string

	RetryMaxAttempts     int
	RetryBackoff         time.Duration
	RetryBackoffMax      time.Duration
	RetryJitterFactor    float64
	RetryFailedRetention time.Duration
}{
	QueueLength:       1000,
	DeliverTimeout:    5,
	SkipTLSVerify:     false,
	PagingNum:         10,
	ProxyURL:          "",
	ProxyHosts:        []string{},
	RetryBackoff:      30 * time.Second,
	RetryBackoffMax:   time.Hour,
	RetryJitterFactor: 0.2,
}

func loadWebhookFrom(rootCfg ConfigProvider) {
	sec := rootCfg.Section("webhook")
	Webhook.QueueLength = sec.Key("QUEUE_LENGTH").MustInt(1000)
	Webhook.DeliverTimeout = sec.Key("DELIVER_TIMEOUT").MustInt(5)
	Webhook.SkipTLSVerify = sec.Key("SKIP_TLS_VERIFY").MustBool()

	deprecatedSetting(rootCfg, "webhook", "ALLOWED_HOST_LIST", "security", "ALLOWED_HOST_LIST", "v28.0.0")
	Webhook.AllowedHostList = sec.Key("ALLOWED_HOST_LIST").MustString(Security.AllowedHostList)
	if Webhook.AllowedHostList != Security.AllowedHostList {
		checkHostList("[webhook] ALLOWED_HOST_LIST", Webhook.AllowedHostList, false)
	}

	Webhook.Types = []string{"gitea", "gogs", "slack", "discord", "dingtalk", "telegram", "msteams", "feishu", "matrix", "wechatwork", "packagist"}
	Webhook.PagingNum = sec.Key("PAGING_NUM").MustInt(10)
	Webhook.ProxyURL = sec.Key("PROXY_URL").MustString("")
	if Webhook.ProxyURL != "" {
		var err error
		Webhook.ProxyURLFixed, err = url.Parse(Webhook.ProxyURL)
		if err != nil {
			log.Error("Webhook PROXY_URL is not valid")
			Webhook.ProxyURL = ""
		}
	}
	Webhook.ProxyHosts = sec.Key("PROXY_HOSTS").Strings(",")

	loadWebhookRetryFrom(sec)
}

func loadWebhookRetryFrom(sec ConfigSection) {
	Webhook.RetryMaxAttempts = sec.Key("RETRY_MAX_ATTEMPTS").MustInt(0)
	Webhook.RetryBackoff = sec.Key("RETRY_BACKOFF").MustDuration(30 * time.Second)
	Webhook.RetryBackoffMax = sec.Key("RETRY_BACKOFF_MAX").MustDuration(time.Hour)
	Webhook.RetryFailedRetention = sec.Key("RETRY_FAILED_RETENTION").MustDuration(0)

	jitterRaw := sec.Key("RETRY_JITTER_FACTOR").MustString("0.2")
	jitter, err := strconv.ParseFloat(jitterRaw, 64)
	if err != nil {
		log.Fatal("[webhook] RETRY_JITTER_FACTOR must be a valid floating point number: %q", jitterRaw)
	}
	Webhook.RetryJitterFactor = jitter

	if err := validateWebhookRetrySettings(); err != nil {
		log.Fatal("%v", err)
	}
}

// validateWebhookRetrySettings checks the retry settings at startup. A
// RETRY_MAX_ATTEMPTS of 0 disables automatic retries and makes the other keys
// irrelevant. Error messages name the offending configuration key.
func validateWebhookRetrySettings() error {
	if Webhook.RetryMaxAttempts == 0 {
		return nil
	}
	switch {
	case Webhook.RetryMaxAttempts < 0:
		return fmt.Errorf("[webhook] RETRY_MAX_ATTEMPTS must be greater than or equal to 0, got %d", Webhook.RetryMaxAttempts)
	case Webhook.RetryBackoff <= 0:
		return fmt.Errorf("[webhook] RETRY_BACKOFF must be greater than 0, got %s", Webhook.RetryBackoff)
	case Webhook.RetryBackoffMax <= 0:
		return fmt.Errorf("[webhook] RETRY_BACKOFF_MAX must be greater than 0, got %s", Webhook.RetryBackoffMax)
	case Webhook.RetryBackoff > Webhook.RetryBackoffMax:
		return fmt.Errorf("[webhook] RETRY_BACKOFF (%s) must not be greater than RETRY_BACKOFF_MAX (%s)", Webhook.RetryBackoff, Webhook.RetryBackoffMax)
	case Webhook.RetryJitterFactor < 0 || Webhook.RetryJitterFactor > 1:
		return fmt.Errorf("[webhook] RETRY_JITTER_FACTOR must be between 0 and 1, got %g", Webhook.RetryJitterFactor)
	case Webhook.RetryFailedRetention < 0:
		return fmt.Errorf("[webhook] RETRY_FAILED_RETENTION must be greater than or equal to 0, got %s", Webhook.RetryFailedRetention)
	}
	return nil
}
