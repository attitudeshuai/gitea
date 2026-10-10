// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package cron

import (
	"testing"

	"gitea.dev/models/unittest"

	_ "gitea.dev/models"
	_ "gitea.dev/models/cron"
)

func TestMain(m *testing.M) {
	unittest.MainTest(m)
}
