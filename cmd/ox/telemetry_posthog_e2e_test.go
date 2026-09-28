//go:build !short

package main

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/testguard"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPostHog_ReleaseBuildReportsCommandsUnderOneInstallID runs a real ox
// binary built with the ldflags goreleaser uses and watches what reaches
// PostHog.
//
// Failure prevented: a release that sends nothing (ldflags naming the wrong
// variables, main not recognizing the sender, the sender never starting),
// that reports one install under a new ID each run, that ignores
// DO_NOT_TRACK, or that reports commands another ox started.
func TestPostHog_ReleaseBuildReportsCommandsUnderOneInstallID(t *testing.T) {
	if testing.Short() {
		t.Skip("short: builds and runs a real ox binary")
	}
	batches := make(chan map[string]any, 8)
	srv := testguard.SafeMockServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if r.URL.Path == "/batch/" && json.NewDecoder(r.Body).Decode(&body) == nil {
			batches <- body
		}
		w.WriteHeader(http.StatusOK)
	}))

	bin := filepath.Join(t.TempDir(), "ox")
	build := exec.Command("go", "build", "-o", bin, "-ldflags",
		"-X github.com/sageox/ox/internal/telemetry.postHogKey=phc_e2e "+
			"-X github.com/sageox/ox/internal/telemetry.postHogHost="+srv.URL,
		"./cmd/ox")
	build.Dir = findModuleRoot(t)
	build.Env = append(os.Environ(), "CGO_ENABLED=0") // safe: go build only, no credentials
	out, err := build.CombinedOutput()
	require.NoError(t, err, "%s", out)

	home := t.TempDir()
	env := []string{
		"HOME=" + home,
		"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
		"XDG_DATA_HOME=" + filepath.Join(home, ".local", "share"),
		"XDG_CACHE_HOME=" + filepath.Join(home, ".cache"),
		"XDG_STATE_HOME=" + filepath.Join(home, ".local", "state"),
		// Every SageOx API call, v1 telemetry included, stays on this server.
		"SAGEOX_ENDPOINT=" + srv.URL,
		// testguard opts every ox run out of telemetry; this test opts back in.
		"DO_NOT_TRACK=",
	}
	workDir := t.TempDir()
	nextEvent := func() map[string]any {
		t.Helper()
		select {
		case body := <-batches:
			batch, ok := body["batch"].([]any)
			require.True(t, ok, "body: %v", body)
			require.Len(t, batch, 1)
			return batch[0].(map[string]any)
		case <-time.After(15 * time.Second):
			t.Fatal("no event reached PostHog")
			return nil
		}
	}

	output, code, _ := testguard.RunOx(t, bin, workDir, env, "version")
	require.Equal(t, 0, code, output)
	first := nextEvent()
	output, code, _ = testguard.RunOx(t, bin, workDir, env, "version", "--json")
	require.Equal(t, 0, code, output)
	second := nextEvent()

	installID, err := os.ReadFile(filepath.Join(home, ".config", "sageox", "client_id"))
	require.NoError(t, err)
	assert.Equal(t, strings.TrimSpace(string(installID)), first["distinct_id"])
	assert.Equal(t, first["distinct_id"], second["distinct_id"], "one install, one ID")
	assert.Equal(t, "ox command run", second["event"])
	props, ok := second["properties"].(map[string]any)
	require.True(t, ok, "event: %v", second)
	assert.Equal(t, "version", props["command"])
	assert.Equal(t, []any{"json"}, props["flags"])
	assert.Equal(t, true, props["success"])
	assert.Equal(t, false, props["$process_person_profile"])
	assert.Equal(t, true, props["$geoip_disable"])

	// Neither an opted-out user nor an ox started by another ox (a hook's
	// prime, sync starting the daemon) reports anything.
	for _, quiet := range []string{"DO_NOT_TRACK=1", envStartedByOx + "=1"} {
		output, code, _ = testguard.RunOx(t, bin, workDir, append(env, quiet), "version")
		require.Equal(t, 0, code, output)
	}
	select {
	case body := <-batches:
		t.Fatalf("an event reached PostHog after DO_NOT_TRACK=1 or %s=1: %v", envStartedByOx, body)
	case <-time.After(3 * time.Second):
	}
}
