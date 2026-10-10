// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package cmd

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gitea.dev/modules/json"
	"gitea.dev/modules/setting"
	"gitea.dev/modules/test"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHookScriptBudget(t *testing.T) {
	now := time.Unix(1000, 0)

	cases := []struct {
		name    string
		state   hookScriptState
		timeout time.Duration
		skip    bool
	}{
		{"no limits", hookScriptState{}, 0, false},
		{"per script only", hookScriptState{ScriptTimeout: 5}, 5 * time.Second, false},
		{"remaining only", hookScriptState{DeadlineUnix: 1002}, 2 * time.Second, false},
		{"remaining is shorter", hookScriptState{ScriptTimeout: 5, DeadlineUnix: 1002}, 2 * time.Second, false},
		{"per script is shorter", hookScriptState{ScriptTimeout: 1, DeadlineUnix: 1005}, time.Second, false},
		{"deadline reached", hookScriptState{ScriptTimeout: 5, DeadlineUnix: 1000}, 0, true},
		{"deadline passed", hookScriptState{ScriptTimeout: 5, DeadlineUnix: 999}, 0, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			timeout, skip := hookScriptBudget(&c.state, now)
			assert.Equal(t, c.skip, skip)
			if !skip {
				assert.Equal(t, c.timeout, timeout)
			}
		})
	}
}

func TestHookLimitedWriter(t *testing.T) {
	// unlimited output forwards everything
	var out bytes.Buffer
	limit := &hookOutputLimit{maxBytes: 0}
	w := &hookLimitedWriter{limit: limit, out: &out}
	n, err := w.Write([]byte("hello"))
	assert.NoError(t, err)
	assert.Equal(t, 5, n)
	assert.Equal(t, "hello", out.String())
	assert.False(t, limit.truncated)

	// the byte cap is shared between stdout and stderr
	var stdout, stderr bytes.Buffer
	limit = &hookOutputLimit{maxBytes: 10}
	stdoutW := &hookLimitedWriter{limit: limit, out: &stdout}
	stderrW := &hookLimitedWriter{limit: limit, out: &stderr}

	assertWrite := func(w *hookLimitedWriter, p []byte) {
		t.Helper()
		n, err := w.Write(p)
		assert.NoError(t, err)
		assert.Equal(t, len(p), n) // excess bytes are still "consumed" so pipes never block
	}
	assertWrite(stdoutW, []byte("aaaaaa"))
	assertWrite(stderrW, []byte("bbbbbb"))
	assertWrite(stdoutW, []byte("cccccc"))
	assert.Equal(t, 10, stdout.Len()+stderr.Len())
	assert.True(t, limit.truncated)
}

func TestLoadHookScriptStateFromFile(t *testing.T) {
	// a previously cached state file is read directly without contacting the internal API
	statePath := filepath.Join(t.TempDir(), "state")
	require.NoError(t, os.WriteFile(statePath, []byte(`{"script_timeout":4,"total_timeout":9,"max_output_size":42,"deadline_unix":123}`), 0o600))
	state := loadHookScriptState(context.Background(), statePath, "owner", "repo")
	assert.Equal(t, 4, state.ScriptTimeout)
	assert.Equal(t, 9, state.TotalTimeout)
	assert.EqualValues(t, 42, state.MaxOutputSize)
	assert.EqualValues(t, 123, state.DeadlineUnix)

	// an empty file (freshly created by mktemp) with the settings API failing falls back to
	// unlimited execution instead of blocking the push
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"err":"temporary outage"}`))
	}))
	defer server.Close()
	defer test.MockVariableValue(&setting.InternalToken, "test-token")()
	defer test.MockVariableValue(&setting.LocalURL, server.URL+"/")()

	emptyPath := filepath.Join(t.TempDir(), "empty-state")
	require.NoError(t, os.WriteFile(emptyPath, nil, 0o600))
	fallback := loadHookScriptState(context.Background(), emptyPath, "owner", "repo")
	assert.Equal(t, 0, fallback.ScriptTimeout)
	assert.Equal(t, 0, fallback.TotalTimeout)
	assert.EqualValues(t, 0, fallback.MaxOutputSize)
	assert.EqualValues(t, 0, fallback.DeadlineUnix)
}

func TestHookScriptStateMarshalRoundTrip(t *testing.T) {
	original := &hookScriptState{ScriptTimeout: 3, TotalTimeout: 12, MaxOutputSize: 4096, DeadlineUnix: 456}
	data, err := json.Marshal(original)
	require.NoError(t, err)
	var decoded hookScriptState
	require.NoError(t, json.Unmarshal(data, &decoded))
	assert.Equal(t, original, &decoded)
}

type hookScriptFailingWriter struct{}

func (*hookScriptFailingWriter) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }

func TestHookLimitedWriterBrokenPipeKeepsDraining(t *testing.T) {
	limit := &hookOutputLimit{maxBytes: 10}
	broken := &hookLimitedWriter{limit: limit, out: &hookScriptFailingWriter{}}

	// the write error is swallowed so io.Copy keeps draining the child process pipe
	n, err := broken.Write([]byte("abc"))
	assert.NoError(t, err)
	assert.Equal(t, 3, n)
	n, err = broken.Write([]byte("defgh"))
	assert.NoError(t, err)
	assert.Equal(t, 5, n)

	// the other stream is unaffected and still forwards within the shared cap
	var healthy bytes.Buffer
	good := &hookLimitedWriter{limit: limit, out: &healthy}
	nn, err := good.Write([]byte("xy"))
	assert.NoError(t, err)
	assert.Equal(t, 2, nn)
	assert.Equal(t, "xy", healthy.String())
}
