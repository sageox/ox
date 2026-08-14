package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Session lifecycle signals (started / uploaded / aborted) ---

// TestNotifySessionUploaded_ParsesPRLinkMisses verifies the uploaded notify
// returns the server's repair tasks so the stop path can hand them to the
// agent.
// Failure prevented: server detects a PR missing its SageOx-Session trailer
// but the repair task is silently dropped client-side.
func TestNotifySessionUploaded_ParsesPRLinkMisses(t *testing.T) {
	var gotBody SessionUploadedNotification
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/v1/sessions/ses_x/uploaded", r.URL.Path)
		require.NoError(t, json.NewDecoder(r.Body).Decode(&gotBody))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"pr_link_misses":[{"pr_url":"https://github.com/o/r/pull/7","expected_line":"SageOx-Session: https://sageox.ai/c/ses_x"}]}`))
	}))
	defer srv.Close()

	client := NewRepoClientWithEndpoint(srv.URL)
	misses, err := client.NotifySessionUploaded(SessionUploadedNotification{
		SessionID:   "ses_x",
		RepoID:      "repo_01abc",
		SessionName: "2026-01-01T00-00-user-OxA1b2",
	})
	require.NoError(t, err)
	require.Len(t, misses, 1)
	assert.Equal(t, "https://github.com/o/r/pull/7", misses[0].PRURL)
	assert.Contains(t, misses[0].ExpectedLine, "SageOx-Session:")
	// server binds name↔id from the payload without waiting for ledger ingest
	assert.Equal(t, "2026-01-01T00-00-user-OxA1b2", gotBody.SessionName)
}

// TestNotifySessionUploaded_OldServerGracefulDegradation verifies 404
// (endpoint not deployed) and an empty/non-JSON body are treated as accepted
// with zero misses.
// Failure prevented: retry thrash against old servers, or a nil-parse crash
// on an empty 200 body.
func TestNotifySessionUploaded_OldServerGracefulDegradation(t *testing.T) {
	t.Run("404 endpoint not deployed", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.NotFound(w, r)
		}))
		defer srv.Close()
		misses, err := NewRepoClientWithEndpoint(srv.URL).NotifySessionUploaded(SessionUploadedNotification{SessionID: "ses_x"})
		assert.NoError(t, err)
		assert.Empty(t, misses)
	})
	t.Run("empty 200 body", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()
		misses, err := NewRepoClientWithEndpoint(srv.URL).NotifySessionUploaded(SessionUploadedNotification{SessionID: "ses_x"})
		assert.NoError(t, err)
		assert.Empty(t, misses)
	})
	t.Run("5xx is a retryable error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer srv.Close()
		_, err := NewRepoClientWithEndpoint(srv.URL).NotifySessionUploaded(SessionUploadedNotification{SessionID: "ses_x"})
		assert.Error(t, err, "5xx must surface so doctor can retry (notify_failed)")
	})
}

// TestNotifySessionStartedAndAborted verifies the register-at-start and
// aborted signals hit their routes and degrade gracefully on old servers.
// Failure prevented: /c/ pending pages never registering, or an aborted
// session stuck showing "in progress".
func TestNotifySessionStartedAndAborted(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := NewRepoClientWithEndpoint(srv.URL)
	require.NoError(t, client.NotifySessionStarted(SessionStartedNotification{SessionID: "ses_x", RepoID: "repo_01abc", Branch: "main"}))
	require.NoError(t, client.NotifySessionAborted(SessionAbortedNotification{SessionID: "ses_x"}))
	assert.Equal(t, []string{"/api/v1/sessions/ses_x/started", "/api/v1/sessions/ses_x/aborted"}, paths)

	notFound := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer notFound.Close()
	old := NewRepoClientWithEndpoint(notFound.URL)
	assert.Error(t, old.NotifySessionStarted(SessionStartedNotification{SessionID: "ses_x"}), "a missing start endpoint must leave the link unconfirmed")
	assert.NoError(t, old.NotifySessionAborted(SessionAbortedNotification{SessionID: "ses_x"}), "old server 404 = accepted")
}

// --- Wire-contract golden pinning ---

// TestSessionNotificationWireKeys golden-pins the exact JSON key set each
// session lifecycle sender puts on the wire, for the SessionUploaded/
// Started/Aborted notification structs. The server team is building the
// receiving endpoints against these exact keys in parallel. Decoding the
// captured request body back into the same Go struct (as tests above do
// for other concerns) is tautological for a json-tag rename: the struct
// always knows how to read its own tags back out. Decoding into
// map[string]any instead means a renamed or dropped tag actually changes
// the observed key set, and asserting each value (not just key presence)
// catches a tag *swap* between two fields that a key-set check alone
// would miss.
//
// Every field beyond the first on each struct is `,omitempty`, so every
// fixture below fills in ALL fields with non-zero values — an empty
// field would vanish from the wire either way, masking a rename.
//
// Failure prevented: a silent json-tag rename or field drop on any of
// the three session notification structs ships a payload the server
// can't read, while the rest of this suite stays green because it only
// round-trips through the same struct.
func TestSessionNotificationWireKeys(t *testing.T) {
	tests := []struct {
		name       string
		wantKeys   []string
		wantValues map[string]any
		call       func(c *RepoClient) error
	}{
		{
			name: "uploaded",
			wantKeys: []string{
				"session_id", "repo_id", "session_name", "session_url",
				"linked_prs", "linked_issues", "produced_commits",
			},
			wantValues: map[string]any{
				"session_id":       "ses_u1",
				"repo_id":          "repo_u1",
				"session_name":     "2026-01-01T00-00-user-OxU1",
				"session_url":      "https://sageox.ai/c/ses_u1",
				"linked_prs":       []any{"https://github.com/o/r/pull/1"},
				"linked_issues":    []any{"https://github.com/o/r/issues/2"},
				"produced_commits": []any{"deadbeef"},
			},
			call: func(c *RepoClient) error {
				_, err := c.NotifySessionUploaded(SessionUploadedNotification{
					SessionID:       "ses_u1",
					RepoID:          "repo_u1",
					SessionName:     "2026-01-01T00-00-user-OxU1",
					SessionURL:      "https://sageox.ai/c/ses_u1",
					LinkedPRs:       []string{"https://github.com/o/r/pull/1"},
					LinkedIssues:    []string{"https://github.com/o/r/issues/2"},
					ProducedCommits: []string{"deadbeef"},
				})
				return err
			},
		},
		{
			name: "started",
			wantKeys: []string{
				"session_id", "repo_id", "session_name", "agent_id", "agent_type", "branch", "started_at",
			},
			wantValues: map[string]any{
				"session_id":   "ses_s1",
				"repo_id":      "repo_s1",
				"session_name": "2026-01-01T00-00-user-OxS1",
				"agent_id":     "agent_42",
				"agent_type":   "claude-code",
				"branch":       "dev/feature-x",
				"started_at":   "2026-01-01T00:00:00Z",
			},
			call: func(c *RepoClient) error {
				return c.NotifySessionStarted(SessionStartedNotification{
					SessionID:   "ses_s1",
					RepoID:      "repo_s1",
					SessionName: "2026-01-01T00-00-user-OxS1",
					AgentID:     "agent_42",
					AgentType:   "claude-code",
					Branch:      "dev/feature-x",
					StartedAt:   "2026-01-01T00:00:00Z",
				})
			},
		},
		{
			name:     "aborted",
			wantKeys: []string{"session_id", "repo_id"},
			wantValues: map[string]any{
				"session_id": "ses_a1",
				"repo_id":    "repo_a1",
			},
			call: func(c *RepoClient) error {
				return c.NotifySessionAborted(SessionAbortedNotification{
					SessionID: "ses_a1",
					RepoID:    "repo_a1",
				})
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var rawBody []byte
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b, err := io.ReadAll(r.Body)
				require.NoError(t, err)
				rawBody = b
				w.WriteHeader(http.StatusOK)
			}))
			defer srv.Close()

			require.NoError(t, tt.call(NewRepoClientWithEndpoint(srv.URL)))

			var got map[string]any
			require.NoError(t, json.Unmarshal(rawBody, &got))

			gotKeys := make([]string, 0, len(got))
			for k := range got {
				gotKeys = append(gotKeys, k)
			}
			assert.ElementsMatch(t, tt.wantKeys, gotKeys,
				"wire key set drifted from the struct's declared json tags — the server is coding against these exact keys; a tag rename or dropped field must fail here")

			for key, want := range tt.wantValues {
				assert.Equal(t, want, got[key], "value for key %q did not round-trip onto the wire", key)
			}
		})
	}
}
