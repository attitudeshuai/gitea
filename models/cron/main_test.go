// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package cron_test

import (
	"testing"

	"gitea.dev/models/unittest"

	_ "gitea.dev/models"      // register models
	_ "gitea.dev/models/cron" // register models of cron
)

func TestMain(m *testing.M) {
	unittest.MainTest(m)
}
