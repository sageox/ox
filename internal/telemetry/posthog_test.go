package telemetry

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/internal/selfexec"
	"github.com/sageox/ox/internal/version"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The detached sender inherits these from the test that started it: package
// variables set in the test process don't reach a new process.
const (
	testPostHogKeyEnv  = "OX_TEST_POSTHOG_KEY"
	testPostHogHostEnv = "OX_TEST_POSTHOG_HOST"
)

// TestMain lets this test binary stand in for ox as the detached sender:
// sendTo makes it postHogExecutable, and CapturePostHog starts it with
// PostHogSenderArg.
func TestMain(m *testing.M) {
	if len(os.Args) == 3 && os.Args[1] == PostHogSenderArg {
		postHogKey = os.Getenv(testPostHogKeyEnv)
		postHogHost = os.Getenv(testPostHogHostEnv)
		RunPostHogSender(os.Args[2])
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// setPostHog swaps the build settings and sender binary for one test.
func setPostHog(t *testing.T, key, host string, exe func() (string, error)) {
	t.Helper()
	oldKey, oldHost, oldExe := postHogKey, postHogHost, postHogExecutable
	postHogKey, postHogHost, postHogExecutable = key, host, exe
	t.Cleanup(func() { postHogKey, postHogHost, postHogExecutable = oldKey, oldHost, oldExe })
}

// isolateConfig gives the test its own install ID file.
func isolateConfig(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("OX_XDG_DISABLE", "")
	t.Setenv("SAGEOX_CLIENT_ID", "")
}

// sendTo configures a build that sends to host, with this test binary as the
// sender process.
func sendTo(t *testing.T, host string) {
	t.Helper()
	isolateConfig(t)
	t.Setenv(testPostHogKeyEnv, "phc_test")
	t.Setenv(testPostHogHostEnv, host)
	setPostHog(t, "phc_test", host, os.Executable)
}

func mustNotStart(t *testing.T) func() (string, error) {
	return func() (string, error) {
		t.Error("a sender process was started")
		return "", os.ErrInvalid
	}
}

// Failure prevented: the command waits on PostHog (a slow or blocked network
// adds seconds to every command), or the event never leaves the machine.
func TestCapturePostHog_DeliversFromADetachedProcessWithoutWaiting(t *testing.T) {
	type request struct {
		path string
		body map[string]any
	}
	arrived := make(chan request, 1)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		arrived <- request{r.URL.Path, body}
		<-release // PostHog has not answered yet
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })
	sendTo(t, srv.URL)

	CapturePostHog("ox command run", map[string]any{"command": "status"})

	var got request
	select {
	case got = <-arrived:
		// CapturePostHog already returned while PostHog is still holding the
		// request open: the command never waited for the response.
	case <-time.After(15 * time.Second):
		t.Fatal("the detached sender never posted the event")
	}
	installID, err := config.InstallID()
	require.NoError(t, err)
	assert.Equal(t, "/batch/", got.path)
	assert.Equal(t, "phc_test", got.body["api_key"])
	batch, ok := got.body["batch"].([]any)
	require.True(t, ok, "body: %v", got.body)
	require.Len(t, batch, 1)
	event := batch[0].(map[string]any)
	assert.Equal(t, "ox command run", event["event"])
	assert.Equal(t, installID, event["distinct_id"])
	assert.NotEmpty(t, event["timestamp"])
	assert.Equal(t, map[string]any{
		"command":                 "status",
		"$process_person_profile": false,
		"$geoip_disable":          true,
		"$lib":                    "ox-cli",
		"$lib_version":            version.Version,
		"os":                      runtime.GOOS,
		"arch":                    runtime.GOARCH,
	}, event["properties"])
}

// Failure prevented: a dev build, go install, or test run reports usage.
func TestCapturePostHog_BuildWithoutAKeyStartsNothing(t *testing.T) {
	isolateConfig(t)
	setPostHog(t, "", "https://posthog.test", mustNotStart(t))

	CapturePostHog("ox command run", map[string]any{})
}

// Failure prevented: an install whose ID cannot be saved sends a new ID on
// every run and is counted as many installs.
func TestCapturePostHog_UnsavedInstallIDStartsNothing(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	require.NoError(t, os.WriteFile(blocker, nil, 0o600))
	t.Setenv("XDG_CONFIG_HOME", blocker)
	t.Setenv("OX_XDG_DISABLE", "")
	t.Setenv("SAGEOX_CLIENT_ID", "")
	setPostHog(t, "phc_test", "https://posthog.test", mustNotStart(t))

	CapturePostHog("ox command run", map[string]any{})
}

func TestRunPostHogSender_PostsNothingItCannotSend(t *testing.T) {
	var posts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { posts.Add(1) }))
	t.Cleanup(srv.Close)

	tests := []struct {
		name, key, host, event string
	}{
		{"build without a key", "", srv.URL, `{"event":"x"}`},
		{"event that is not JSON", "phc_test", srv.URL, `{"event":`},
		{"host that is not a URL", "phc_test", "http://bad host", `{"event":"x"}`},
		{"host that refuses connections", "phc_test", "http://127.0.0.1:1", `{"event":"x"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setPostHog(t, tt.key, tt.host, mustNotStart(t))
			RunPostHogSender(tt.event)
		})
	}
	assert.Zero(t, posts.Load())
}

// Failure prevented: a release ships with PostHog silently off, because a
// build lacks the ldflags or they name a variable that was renamed. Update
// the names here if postHogKey or postHogHost change.
func TestReleaseBuildsSetThePostHogSettings(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", ".config", "goreleaser.yml"))
	require.NoError(t, err)
	yml := strings.ReplaceAll(string(data), "\r\n", "\n") // a Windows checkout with autocrlf

	var oxBuilds int
	for _, build := range strings.Split(yml, "\n  - id: ")[1:] {
		if !strings.Contains(build, "main: ./cmd/ox\n") {
			continue
		}
		oxBuilds++
		id, _, _ := strings.Cut(build, "\n")
		assert.Contains(t, build, "-X 'github.com/sageox/ox/internal/telemetry.postHogKey=", id)
		assert.Contains(t, build, "-X 'github.com/sageox/ox/internal/telemetry.postHogHost=", id)
	}
	assert.Positive(t, oxBuilds, "no ox builds found in goreleaser.yml")
}

// Failure prevented: on a network that silently drops packets to PostHog,
// senders from a burst of commands pile up, each waiting out its timeout,
// until they exhaust memory or the process limit.
func TestRunPostHogSender_DropsTheEventWhenEverySlotIsBusy(t *testing.T) {
	var posts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { posts.Add(1) }))
	t.Cleanup(srv.Close)
	setPostHog(t, "phc_test", srv.URL, mustNotStart(t))
	tmp := t.TempDir()
	for _, v := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(v, tmp)
	}

	// Hold every slot, as senders stuck on a dead network would.
	release := make(chan struct{})
	var taken, done sync.WaitGroup
	for slot := range postHogSenderSlots {
		taken.Add(1)
		done.Add(1)
		go func() {
			defer done.Done()
			slotPath := filepath.Join(os.TempDir(), fmt.Sprintf("ox-posthog-sender-%d", slot))
			_ = fileutil.WithFileLockTimeout(context.Background(), slotPath, time.Second, func() error {
				taken.Done()
				<-release
				return nil
			})
		}()
	}
	taken.Wait()

	RunPostHogSender(`{"event":"ox command run"}`)
	assert.Zero(t, posts.Load(), "with every slot busy the event is dropped")

	close(release)
	done.Wait()
	RunPostHogSender(`{"event":"ox command run"}`)
	assert.Equal(t, int32(1), posts.Load(), "a free slot sends again")
}

// Failure prevented: a test run with a key set starts real senders, each of
// them a copy of the test binary (selfexec.Path exists to refuse exactly this).
func TestCapturePostHog_UnderGoTestTheDefaultSenderRefuses(t *testing.T) {
	isolateConfig(t)
	var refused error
	setPostHog(t, "phc_test", "https://posthog.test", func() (string, error) {
		path, err := selfexec.Path()
		refused = err
		return path, err
	})

	CapturePostHog("ox command run", map[string]any{})

	assert.ErrorIs(t, refused, selfexec.ErrUnderTest)
}

// Failure prevented: an event that cannot be encoded starts a sender with an
// empty or truncated payload.
func TestCapturePostHog_UnencodableEventStartsNothing(t *testing.T) {
	isolateConfig(t)
	setPostHog(t, "phc_test", "https://posthog.test", mustNotStart(t))

	CapturePostHog("ox command run", map[string]any{"bad": func() {}})
}

// Failure prevented: an opt-out is ignored, or its documented precedence
// (environment before the saved setting) is reversed.
func TestEnabled_EnvironmentThenSavedSetting(t *testing.T) {
	tests := []struct {
		name, doNotTrack, sageoxTelemetry, saved string
		want                                     bool
	}{
		{"nothing set", "", "", "", true},
		{"DO_NOT_TRACK=1", "1", "", "", false},
		{"SAGEOX_TELEMETRY=FALSE", "", "FALSE", "", false},
		{"saved off", "", "", "telemetry_enabled: false\n", false},
		{"saved on, DO_NOT_TRACK=1 wins", "1", "", "telemetry_enabled: true\n", false},
		{"unreadable setting", "", "", "telemetry_enabled: [\n", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("DO_NOT_TRACK", tt.doNotTrack)
			t.Setenv("SAGEOX_TELEMETRY", tt.sageoxTelemetry)
			cfgPath := filepath.Join(t.TempDir(), "config.yaml")
			if tt.saved != "" {
				require.NoError(t, os.WriteFile(cfgPath, []byte(tt.saved), 0o600))
			}
			t.Setenv(config.EnvUserConfig, cfgPath)

			assert.Equal(t, tt.want, Enabled())
		})
	}
}
