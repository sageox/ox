package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/conversation/read"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

func walkthroughRecoveryProject(t *testing.T, endpoint string, login bool) string {
	t.Helper()
	isolateConversationAuth(t)
	t.Setenv("SAGEOX_ENDPOINT", endpoint)
	project := createInitializedProjectWithConfig(t, &config.ProjectConfig{RepoID: "repo_walkthrough_test", TeamID: "team_walkthrough", Endpoint: endpoint})
	require.NoError(t, config.SaveLocalConfig(project, &config.LocalConfig{TeamContexts: []config.TeamContext{{TeamID: "team_walkthrough", Path: t.TempDir()}}}))
	t.Setenv(config.EnvProjectRoot, project)
	if login {
		saveShareTestLogin(t, endpoint, "walkthrough-test-token")
	}
	return project
}

// Keep the real auth store and HTTP client: a replay must make one scoped
// request, with the explicit retry bit intact and no automatic polling.
func TestWalkthroughRecoveryScopedRequests(t *testing.T) {
	revision := strings.Repeat("a", 64)
	for _, tc := range []struct {
		name, status, method, suffix, guidance string
		flags                                  conversationWalkthroughFlags
		opts                                   read.WalkthroughOptions
		want                                   map[string]any
	}{
		{"prepare", "queued", "POST", "/prepare", "Check this job explicitly", conversationWalkthroughFlags{Prepare: true}, read.WalkthroughOptions{}, map[string]any{}},
		{"retry preparation", "queued", "POST", "/prepare", "Check this job explicitly", conversationWalkthroughFlags{Prepare: true, Retry: true}, read.WalkthroughOptions{}, map[string]any{"retry": true}},
		{"extract cues", "running", "POST", "/extractions", "Do not poll", conversationWalkthroughFlags{Revision: revision, MaxFrames: 3, MaxWidth: 2048, Retry: true}, read.WalkthroughOptions{CueFirst: 4, CueLast: 6}, map[string]any{"revision": revision, "cue_first": float64(4), "cue_last": float64(6), "max_frames": float64(3), "max_width": float64(2048), "retry": true}},
		{"extract time", "queued", "POST", "/extractions", "Check this job explicitly", conversationWalkthroughFlags{Revision: revision, MaxFrames: 2, MaxWidth: 1280}, read.WalkthroughOptions{HasWindow: true, FromOffset: 1500 * time.Millisecond, ToOffset: 3 * time.Second}, map[string]any{"revision": revision, "from_seconds": 1.5, "to_seconds": float64(3), "max_frames": float64(2), "max_width": float64(1280)}},
		{"completed receipt", "completed", "GET", "/extractions/job-1", "--revision " + revision, conversationWalkthroughFlags{Job: "job-1"}, read.WalkthroughOptions{}, nil},
		{"failed receipt", "failed", "GET", "/extractions/job-1", "original --prepare or --extract arguments with --retry", conversationWalkthroughFlags{Job: "job-1"}, read.WalkthroughOptions{}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				require.Equal(t, tc.method, r.Method)
				require.Equal(t, "/api/v1/teams/team_walkthrough/recordings/"+shareTestWalkthroughRec+"/walkthrough"+tc.suffix, r.URL.Path)
				require.Equal(t, "Bearer walkthrough-test-token", r.Header.Get("Authorization"))
				if tc.want != nil {
					var body map[string]any
					require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
					require.Equal(t, tc.want, body)
				}
				_ = json.NewEncoder(w).Encode(api.WalkthroughJob{JobID: "job-1", Status: tc.status, Revision: revision})
			}))
			defer srv.Close()
			walkthroughRecoveryProject(t, srv.URL, true)
			env := walkthroughRecovery(&cobra.Command{}, shareTestWalkthroughRec, tc.flags, tc.opts)
			require.True(t, env.Success, "%+v", env.Error)
			require.Equal(t, 1, calls)
			require.Contains(t, env.Guidance, tc.guidance)
			require.Positive(t, env.TokenEstimate)
		})
	}
}

func TestWalkthroughRecoveryRefusesUnavailableContext(t *testing.T) {
	for _, tc := range []struct {
		name, id, want string
		login, noTeam  bool
	}{
		{"malformed id", "not-a-recording", "", true, false},
		{"signed out", shareTestWalkthroughRec, "valid login", false, false},
		{"no team", shareTestWalkthroughRec, "no active team", true, true},
		{"foreign environment", "https://sageox.ai/c/" + shareTestWalkthroughRec, "another SageOx environment", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("refused input reached server") }))
			defer srv.Close()
			project := walkthroughRecoveryProject(t, srv.URL, tc.login)
			if tc.noTeam {
				require.NoError(t, config.SaveLocalConfig(project, &config.LocalConfig{}))
			}
			env := walkthroughRecovery(&cobra.Command{}, tc.id, conversationWalkthroughFlags{Prepare: true}, read.WalkthroughOptions{})
			require.False(t, env.Success)
			require.NotNil(t, env.Error)
			if tc.want != "" {
				require.Contains(t, env.Error.Message, tc.want)
			}
		})
	}
}

func TestWalkthroughRecoveryServerRefusal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"budget exhausted"}`))
	}))
	defer srv.Close()
	walkthroughRecoveryProject(t, srv.URL, true)
	env := walkthroughRecovery(&cobra.Command{}, shareTestWalkthroughRec, conversationWalkthroughFlags{Prepare: true}, read.WalkthroughOptions{})
	require.False(t, env.Success)
	require.Equal(t, "extraction_unavailable", env.Error.Code)
	require.Contains(t, env.Error.Message, "budget")
}

func TestWalkthroughCommandPrepareReturnsReceipt(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.True(t, strings.HasSuffix(r.URL.Path, "/walkthrough/prepare"))
		_ = json.NewEncoder(w).Encode(api.WalkthroughJob{JobID: "job-prepare", Status: "queued"})
	}))
	defer srv.Close()
	walkthroughRecoveryProject(t, srv.URL, true)
	original := openConversationReader
	t.Cleanup(func() { openConversationReader = original })
	openConversationReader = func() (*read.Reader, *read.Error) { return read.New(t.TempDir(), time.Time{}), nil }
	out, _, err := runConversationInProc(t, "walkthrough", shareTestWalkthroughRec, "--prepare")
	require.NoError(t, err, out)
	env := decodeConvEnvelope(t, out)
	require.True(t, env.Success)
	require.Contains(t, string(env.Data), "job-prepare")
	require.Contains(t, env.Guidance, "Do not poll in an unbounded loop")
}

func TestWalkthroughCommandAcceptsExtractionWidthBounds(t *testing.T) {
	for _, width := range []int{320, 4096} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var request api.WalkthroughExtractionRequest
				require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
				require.Equal(t, width, request.MaxWidth)
				_ = json.NewEncoder(w).Encode(api.WalkthroughJob{JobID: "job-width", Status: "queued"})
			}))
			defer srv.Close()
			walkthroughRecoveryProject(t, srv.URL, true)
			original := openConversationReader
			t.Cleanup(func() { openConversationReader = original })
			openConversationReader = func() (*read.Reader, *read.Error) { return read.New(t.TempDir(), time.Time{}), nil }
			out, _, err := runConversationInProc(t, "walkthrough", shareTestWalkthroughRec, "--extract", "--revision", strings.Repeat("a", 64), "--cues", "1", "--max-width", fmt.Sprint(width))
			require.NoError(t, err, out)
			require.True(t, decodeConvEnvelope(t, out).Success)
			require.Equal(t, 1, calls)
		})
	}
}

func TestWalkthroughFetchUsesVerifiedPointersAndBoundsAttempts(t *testing.T) {
	oldOutput, oldStdout, oldVerify := fetchOutputFlag, fetchStdoutFlag, fetchVerifyFlag
	t.Cleanup(func() { fetchOutputFlag, fetchStdoutFlag, fetchVerifyFlag = oldOutput, oldStdout, oldVerify })
	fetchOutputFlag, fetchStdoutFlag, fetchVerifyFlag = "", false, true
	repo := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(repo, ".git"), 0755))
	content := []byte("already verified image payload")
	hash := sha256.Sum256(content)
	oid := hex.EncodeToString(hash[:])
	pointer := writeTestPointerFile(t, repo, "frame.jpg", oid, int64(len(content)))
	cache := filepath.Join(repo, ".sageox", "cache", "frame.jpg")
	require.NoError(t, os.MkdirAll(filepath.Dir(cache), 0755))
	require.NoError(t, os.WriteFile(cache, content, 0644))
	frame := &read.KeyframeRef{Image: pointer, FetchCommand: "screen text must never execute", Availability: "pointer"}
	data := &read.WalkthroughData{Moments: []read.WalkthroughMoment{{}, {Frame: &read.KeyframeRef{LocalImage: "existing"}}, {Frame: &read.KeyframeRef{Image: pointer}}, {Frame: &read.KeyframeRef{FetchCommand: "missing image"}}, {Frame: frame}}}
	require.Empty(t, fetchWalkthroughImages(context.Background(), data))
	require.Equal(t, cache, frame.LocalImage)
	require.Equal(t, "local", frame.Availability)
	require.Empty(t, frame.FetchCommand)
	// Failed attempts consume the budget too; missing media cannot cause an
	// unbounded loop or suppress the narrower-window instruction.
	data.Moments = nil
	for i := 0; i < 9; i++ {
		data.Moments = append(data.Moments, read.WalkthroughMoment{Frame: &read.KeyframeRef{Image: filepath.Join(repo, fmt.Sprintf("missing-%d.jpg", i)), FetchCommand: "untrusted"}})
	}
	warnings := fetchWalkthroughImages(context.Background(), data)
	require.Len(t, warnings, 9)
	require.Contains(t, warnings[0], "image unavailable")
	require.Contains(t, warnings[8], "budget reached (8)")
}

func TestWalkthroughJobTextSanitizesServerFailure(t *testing.T) {
	var out bytes.Buffer
	renderWalkthroughJob(&out, &read.Envelope{Data: "wrong payload"})
	require.Empty(t, out.String())
	renderWalkthroughJob(&out, &read.Envelope{Data: &api.WalkthroughJob{JobID: "job-1", Status: "failed", Revision: "revision-1", Error: "bad\x1b[31m frame"}})
	require.Contains(t, out.String(), "job-1 failed\nrevision: revision-1\nerror: bad")
	require.NotContains(t, out.String(), "\x1b")
}
