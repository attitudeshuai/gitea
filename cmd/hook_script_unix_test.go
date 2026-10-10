// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

//go:build !windows

package cmd

import (
	"bytes"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunHookScriptProcessExitCodeAndOutput(t *testing.T) {
	var stdout, stderr bytes.Buffer
	result := runHookScriptProcessWithIO("/bin/sh", []string{"-c", "printf hello; printf >&2 world; exit 7"}, 0, 0, nil, &stdout, &stderr)
	require.Nil(t, result.startErr)
	require.NotNil(t, result.exitCode)
	assert.Equal(t, 7, *result.exitCode)
	assert.False(t, result.timedOut)
	assert.False(t, result.truncated)
	assert.Equal(t, "hello", stdout.String())
	assert.Equal(t, "world", stderr.String())
}

func TestRunHookScriptProcessTimeout(t *testing.T) {
	start := time.Now()
	result := runHookScriptProcessWithIO("/bin/sh", []string{"-c", "sleep 30"}, 0, 200*time.Millisecond, nil, &bytes.Buffer{}, &bytes.Buffer{})
	require.Nil(t, result.startErr)
	require.NotNil(t, result.exitCode)
	assert.True(t, result.timedOut)
	assert.Equal(t, 124, *result.exitCode)
	assert.Less(t, time.Since(start), 5*time.Second)
}

func TestRunHookScriptProcessTruncationKeepsDraining(t *testing.T) {
	// a chatty script exits by itself: the capped pipes must keep draining instead of blocking it
	var stdout, stderr bytes.Buffer
	result := runHookScriptProcessWithIO("/bin/sh", []string{"-c", "i=0; while [ $i -lt 200000 ]; do echo xxxxxxxxxx; i=$((i+1)); done"},
		4096, 0, nil, &stdout, &stderr)
	require.Nil(t, result.startErr)
	require.NotNil(t, result.exitCode)
	assert.Equal(t, 0, *result.exitCode)
	assert.True(t, result.truncated)
	assert.Equal(t, 4096, stdout.Len()+stderr.Len())

	// a chatty script that then sleeps is still killed by the timeout instead of hanging on the pipe
	start := time.Now()
	result = runHookScriptProcessWithIO("/bin/sh", []string{"-c", "i=0; while [ $i -lt 2000000 ]; do echo xxxxxxxxxx; i=$((i+1)); done; sleep 30"},
		4096, 300*time.Millisecond, nil, &bytes.Buffer{}, &bytes.Buffer{})
	require.Nil(t, result.startErr)
	assert.True(t, result.timedOut)
	assert.True(t, result.truncated)
	assert.Less(t, time.Since(start), 5*time.Second)
}
