//go:build slow

package main

import (
	"context"
	"encoding/json"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/paths"
	"github.com/sageox/ox/internal/testguard"
	localtrace "github.com/sageox/ox/internal/trace"
	"github.com/stretchr/testify/require"
)

// This compiled CLI gate exercises actual detached spawn, Cobra registration,
// private process state, persistent opt-in, capture, and disable/purge together.
func TestTraceCompiledBinaryGateAndLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("short: builds and executes ox binary")
	}
	_, source, _, ok := runtime.Caller(0)
	require.True(t, ok)
	root := filepath.Dir(filepath.Dir(filepath.Dir(source)))
	binary := testguard.BuildOxBinary(t, root)
	isolated := t.TempDir()
	user := "trace-test-" + filepath.Base(filepath.Dir(isolated))
	t.Setenv("USER", user)
	env := []string{
		"HOME=" + isolated,
		"OX_XDG_DISABLE=",
		"FEATURE_TRACE=",
		"XDG_STATE_HOME=" + filepath.Join(isolated, "state"),
		"USER=" + user,
		"XDG_CONFIG_HOME=" + filepath.Join(isolated, "config"),
		"XDG_CACHE_HOME=" + filepath.Join(isolated, "cache"),
		"XDG_DATA_HOME=" + filepath.Join(isolated, "data"),
		"XDG_RUNTIME_DIR=" + filepath.Join(isolated, "runtime"),
		"CLAUDE_CONFIG_DIR=" + filepath.Join(isolated, "claude"),
	}
	on := append(append([]string{}, env...), "FEATURE_TRACE=1")
	run := func(vars []string, args ...string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		out, err := testguard.OxCmdContext(t, ctx, binary, isolated, vars, args...).CombinedOutput()
		return string(out), err
	}
	out, err := run(env, "session", "trace", "status", "--json")
	require.Error(t, err, out)
	require.Contains(t, out, "unknown command")
	out, err = run(on, "session", "trace", "--help")
	require.NoError(t, err, out)
	require.Contains(t, out, "enable")

	// Generated public reference docs omit the pilot even with the flag enabled.
	docs := filepath.Join(isolated, "docs")
	out, err = run(on, "docs", "--output", docs)
	require.NoError(t, err, out)
	require.NoError(t, filepath.WalkDir(docs, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(docs, path)
		require.NoError(t, err)
		require.False(t, strings.Contains(filepath.ToSlash(rel), "session/trace"), "experimental reference leaked: %s", rel)
		return nil
	}))
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	require.NoError(t, err)
	port := listener.Addr().(*net.TCPAddr).Port
	require.NoError(t, listener.Close())
	t.Cleanup(func() {
		cleanupOut, cleanupErr := run(on, "session", "trace", "disable", "--purge")
		if cleanupErr != nil {
			t.Errorf("cleanup receiver: %v: %s", cleanupErr, cleanupOut)
		}
		_ = os.RemoveAll(filepath.Dir(paths.TraceLogPath()))
	})
	// Two independent CLI processes race their initial detached startup.
	type launchResult struct {
		output string
		err    error
	}
	launches := make(chan launchResult, 2)
	for range 2 {
		go func() {
			output, launchErr := run(on, "session", "trace", "enable", "--port", strconv.Itoa(port), "--json")
			launches <- launchResult{output, launchErr}
		}()
	}
	for range 2 {
		result := <-launches
		require.NoError(t, result.err, result.output)
		out = result.output
	}
	var status sessionTraceStatus
	require.NoError(t, json.Unmarshal([]byte(out), &status), out)
	require.True(t, status.Enabled)
	require.True(t, status.Running)
	first, err := localtrace.Health(context.Background(), port)
	require.NoError(t, err)

	// A second real CLI launch retains the receiver's process identity.
	out, err = run(on, "session", "trace", "enable", "--port", strconv.Itoa(port), "--json")
	require.NoError(t, err, out)
	second, err := localtrace.Health(context.Background(), port)
	require.NoError(t, err)
	require.Equal(t, first, second)

	// Removing FEATURE_TRACE affects the command surface, not a running receiver.
	out, err = run(env, "session", "trace", "status", "--json")
	require.Error(t, err, out)
	require.Contains(t, out, "unknown command")
	_, err = localtrace.Health(context.Background(), port)
	require.NoError(t, err)
	const sessionID = "a1111111-1111-4111-8111-111111111111"
	payload := `{"resourceSpans":[{"resource":{},"scopeSpans":[{"scope":{},"spans":[{"name":"cli-test","attributes":[{"key":"session.id","value":{"stringValue":"` + sessionID + `"}}]}]}]}]}`
	client := &http.Client{Timeout: time.Second, Transport: &http.Transport{Proxy: nil}}
	response, err := client.Post("http://127.0.0.1:"+strconv.Itoa(port)+"/v1/traces", "application/json", strings.NewReader(payload))
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	require.Equal(t, http.StatusOK, response.StatusCode)
	out, err = run(on, "session", "trace", "status", "--json")
	require.NoError(t, err, out)
	require.NoError(t, json.Unmarshal([]byte(out), &status), out)
	require.Equal(t, 1, status.Sessions)
	require.Positive(t, status.Bytes)
	require.NotNil(t, status.LastReceiptAt)
	out, err = run(on, "session", "trace", "disable", "--purge", "--json")
	require.NoError(t, err, out)
	_, err = localtrace.Health(context.Background(), port)
	require.Error(t, err)
	_, err = os.Stat(status.SpoolPath)
	require.ErrorIs(t, err, fs.ErrNotExist)
}
