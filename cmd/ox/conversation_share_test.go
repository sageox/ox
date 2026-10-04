package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/conversation/read"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Share-link resolution: `ox conversation show|topics|topic|transcript` accept
// https://<host>.sageox.ai/s/{token} by asking GET /api/v1/shares/{token}/target
// on the endpoint the user is logged in to for that host. These tests keep the
// real auth store (isolated under a temp HOME) and the real API client; only
// the base URL the request lands on is pointed at an httptest server.

const (
	// Recording in internal/conversation/read/testdata/walkthrough.
	shareTestWalkthroughRec = "rec_019ffe10-0000-7000-8000-000000000011"
	shareTestToken          = "rs-walk1"
	shareTestLink           = "https://test.sageox.ai/s/" + shareTestToken
	shareTestEndpoint       = "https://test.sageox.ai"
	shareTestProdEndpoint   = "https://sageox.ai"
)

// isolateConversationAuth reroutes every credential source to an empty temp
// home, so a share link never reaches the developer's real login or a live
// server from a unit test.
func isolateConversationAuth(t *testing.T) {
	t.Helper()
	isolateStatusTestAuth(t)
	t.Setenv("OX_XDG_DISABLE", "")
	t.Setenv(auth.EnvVarToken, "")
	t.Setenv("SAGEOX_ENDPOINT", "")
	// A 401 from the lookup triggers one refresh against the stored
	// endpoint; never let that reach a real server from a unit test.
	origRefresh := shareRefresh
	t.Cleanup(func() { shareRefresh = origRefresh })
	shareRefresh = func(*auth.StoredToken, string) (*auth.StoredToken, error) {
		return nil, errors.New("refresh disabled in tests")
	}
}

// useWalkthroughReader points the command layer at the walkthrough fixture
// and isolates auth.
func useWalkthroughReader(t *testing.T) {
	t.Helper()
	isolateConversationAuth(t)
	orig := openConversationReader
	t.Cleanup(func() { openConversationReader = orig })
	openConversationReader = func() (*read.Reader, *read.Error) {
		return read.New(repoPath("..", "..", "internal", "conversation", "read", "testdata", "walkthrough", "discussions"),
			time.Date(2026, 8, 20, 17, 41, 0, 0, time.UTC)), nil
	}
}

// shareLookupServer records every request it receives.
type shareLookupServer struct {
	*httptest.Server
	mu    sync.Mutex
	reqs  []*http.Request
	auths []string
}

func (s *shareLookupServer) requests() ([]*http.Request, []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*http.Request(nil), s.reqs...), append([]string(nil), s.auths...)
}

// newShareLookupServer serves handler, recording each request, and routes
// every share lookup to it for the test's duration.
func newShareLookupServer(t *testing.T, handler http.HandlerFunc) *shareLookupServer {
	t.Helper()
	s := &shareLookupServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.reqs = append(s.reqs, r.Clone(r.Context()))
		s.auths = append(s.auths, r.Header.Get("Authorization"))
		s.mu.Unlock()
		handler(w, r)
	}))
	t.Cleanup(s.Close)
	origBase := shareLookupBaseURL
	t.Cleanup(func() { shareLookupBaseURL = origBase })
	shareLookupBaseURL = func(string) string { return s.URL }
	return s
}

// saveShareTestLogin stores a live credential for ep.
func saveShareTestLogin(t *testing.T, ep, accessToken string) {
	t.Helper()
	require.NoError(t, auth.SaveTokenForEndpoint(ep, &auth.StoredToken{
		AccessToken:  accessToken,
		RefreshToken: "refresh-" + accessToken,
		ExpiresAt:    time.Now().Add(time.Hour),
		TokenType:    "Bearer",
	}))
}

func writeShareTarget(w http.ResponseWriter, entityType, entityID string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"entity_type":   entityType,
		"entity_id":     entityID,
		"owner_type":    "team",
		"owner_id":      "team_abc",
		"redirect_path": "/team/team_abc/media/recordings/" + entityID,
	})
}

// --- A. Resolution succeeds ---

// TestConversationShareLink_ResolvesAndServesDiscussion: a share link for a
// recording the user can see resolves online and the ORIGINAL subcommand is
// served from the local checkout. Failure prevented: share links stay a dead
// end even when the user is logged in and the server can answer.
func TestConversationShareLink_ResolvesAndServesDiscussion(t *testing.T) {
	useWalkthroughReader(t)
	saveShareTestLogin(t, shareTestEndpoint, "test-env-access")
	srv := newShareLookupServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeShareTarget(w, "recording", shareTestWalkthroughRec)
	})

	stdout, _, err := runConversationInProc(t, "show", shareTestLink)
	require.NoError(t, err, stdout)
	env := decodeConvEnvelope(t, stdout)
	require.True(t, env.Success, stdout)
	var show struct {
		RecordingID string `json:"recording_id"`
		Title       string `json:"title"`
	}
	require.NoError(t, json.Unmarshal(env.Data, &show))
	assert.Equal(t, shareTestWalkthroughRec, show.RecordingID)
	assert.Equal(t, "Settings walkthrough", show.Title)

	// transcript takes the same path (a different subcommand, same resolver).
	stdout, _, err = runConversationInProc(t, "transcript", shareTestLink, "--cues", "1-2")
	require.NoError(t, err, stdout)
	require.True(t, decodeConvEnvelope(t, stdout).Success, stdout)

	reqs, auths := srv.requests()
	require.Len(t, reqs, 2, "one lookup per invocation, no retries")
	for i, r := range reqs {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/api/v1/shares/"+shareTestToken+"/target", r.URL.Path)
		assert.Equal(t, "Bearer test-env-access", auths[i])
	}
}

// TestConversationShareLink_UsesTheLinkHostsLogin: with logins for two
// environments, the credential for the LINK's host is the one sent. Failure
// prevented: a test-environment link authenticating with the production
// credential (or vice versa).
func TestConversationShareLink_UsesTheLinkHostsLogin(t *testing.T) {
	useWalkthroughReader(t)
	saveShareTestLogin(t, shareTestProdEndpoint, "prod-access")
	saveShareTestLogin(t, shareTestEndpoint, "test-env-access")
	srv := newShareLookupServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeShareTarget(w, "recording", shareTestWalkthroughRec)
	})

	stdout, _, err := runConversationInProc(t, "show", shareTestLink)
	require.NoError(t, err, stdout)

	_, auths := srv.requests()
	require.Len(t, auths, 1)
	assert.Equal(t, "Bearer test-env-access", auths[0])
}

// --- B. No request may be made ---

// TestConversationShareLink_NoRequestWithoutMatchingLogin: logged out, or
// logged in only to a DIFFERENT environment than the link names, ox makes no
// request at all — the token is never sent anywhere — and says to log in.
// Failure prevented: a pasted link choosing where a credential goes.
func TestConversationShareLink_NoRequestWithoutMatchingLogin(t *testing.T) {
	tests := []struct {
		name  string
		login string // endpoint to log in to; "" = logged out
	}{
		{name: "logged out"},
		{name: "logged in to a different environment", login: shareTestProdEndpoint},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			useWalkthroughReader(t)
			if tt.login != "" {
				saveShareTestLogin(t, tt.login, "prod-access")
			}
			srv := newShareLookupServer(t, func(w http.ResponseWriter, r *http.Request) {
				writeShareTarget(w, "recording", shareTestWalkthroughRec)
			})

			stdout, _, err := runConversationInProc(t, "show", shareTestLink)
			assert.Equal(t, 2, exitCodeOf(t, err))
			env := decodeConvEnvelope(t, stdout)
			require.NotNil(t, env.Error, stdout)
			assert.Equal(t, read.ErrCodeShareLinkUnresolvable, env.Error.Code)
			assert.Contains(t, env.Error.Message, "not logged in to test.sageox.ai")
			assert.Contains(t, env.Error.Message, "ox login")
			assert.Contains(t, env.Guidance, "recording page URL")

			reqs, _ := srv.requests()
			assert.Empty(t, reqs, "no lookup may be sent without a login for the link's host")
		})
	}
}

// TestConversationShareLink_RedirectNotFollowed: a 3xx from the lookup is not
// followed, so the credential never reaches the redirect target. Failure
// prevented: a misconfigured or hostile proxy relaying the bearer token.
func TestConversationShareLink_RedirectNotFollowed(t *testing.T) {
	useWalkthroughReader(t)
	saveShareTestLogin(t, shareTestEndpoint, "test-env-access")
	var elsewhereHits int
	var mu sync.Mutex
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		elsewhereHits++
		mu.Unlock()
		writeShareTarget(w, "recording", shareTestWalkthroughRec)
	}))
	t.Cleanup(elsewhere.Close)
	newShareLookupServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+r.URL.Path, http.StatusFound)
	})

	stdout, _, err := runConversationInProc(t, "show", shareTestLink)
	assert.Equal(t, 2, exitCodeOf(t, err))
	assert.Equal(t, read.ErrCodeShareLinkUnresolvable, decodeConvEnvelope(t, stdout).Error.Code)
	mu.Lock()
	defer mu.Unlock()
	assert.Zero(t, elsewhereHits, "redirect target must never be contacted")
}

// --- C. Every lookup failure degrades to share_link_unresolvable ---

// TestConversationShareLink_LookupFailuresDegrade: each way the lookup can
// fail lands on share_link_unresolvable (exit 2) with the specific reason and
// the paste-the-recording fallback, after exactly one request. Failure
// prevented: a server without the route reading as "share revoked", a
// revoked share reading as a server outage, or any failure looping.
func TestConversationShareLink_LookupFailuresDegrade(t *testing.T) {
	tests := []struct {
		name       string
		handler    http.HandlerFunc
		wantReason string
	}{
		{
			name: "share not found",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"error":"share not found"}`))
			},
			wantReason: "revoked or expired, or not shared with your team",
		},
		{
			name:       "route missing: 404 empty body",
			handler:    func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNotFound) },
			wantReason: "doesn't support share lookups yet",
		},
		{
			name: "route missing: 404 html",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte("<html>404 page not found</html>"))
			},
			wantReason: "doesn't support share lookups yet",
		},
		{
			name: "route missing: generic json 404",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"error":"not found"}`))
			},
			wantReason: "doesn't support share lookups yet",
		},
		{
			name:       "route missing: 405",
			handler:    func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusMethodNotAllowed) },
			wantReason: "doesn't support share lookups yet",
		},
		{
			name:       "route missing: 501",
			handler:    func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNotImplemented) },
			wantReason: "doesn't support share lookups yet",
		},
		{
			name:       "unauthorized",
			handler:    func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusUnauthorized) },
			wantReason: "rejected your login; run `ox login`",
		},
		{
			name:       "forbidden",
			handler:    func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusForbidden) },
			wantReason: "not available to your account",
		},
		{
			name:       "server error",
			handler:    func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusBadGateway) },
			wantReason: "could not be reached",
		},
		{
			name: "malformed json",
			handler: func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(`{"entity_type":`))
			},
			wantReason: "the share lookup on test.sageox.ai failed",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			useWalkthroughReader(t)
			saveShareTestLogin(t, shareTestEndpoint, "test-env-access")
			srv := newShareLookupServer(t, tt.handler)

			stdout, _, err := runConversationInProc(t, "show", shareTestLink)
			assert.Equal(t, 2, exitCodeOf(t, err))
			env := decodeConvEnvelope(t, stdout)
			require.NotNil(t, env.Error, stdout)
			assert.Equal(t, read.ErrCodeShareLinkUnresolvable, env.Error.Code)
			assert.Contains(t, env.Error.Message, tt.wantReason)
			assert.Contains(t, env.Error.Message, "rec_", "the fallback is always named")
			assert.False(t, env.Error.Retryable)
			reqs, _ := srv.requests()
			assert.Len(t, reqs, 1, "exactly one lookup, no retry loop")
		})
	}
}

// TestConversationShareLink_Timeout: a lookup that never answers is abandoned
// at the timeout and degrades with a timed-out reason. Failure prevented: a
// hung server hanging the command (and the AI coworker waiting on it).
func TestConversationShareLink_Timeout(t *testing.T) {
	useWalkthroughReader(t)
	saveShareTestLogin(t, shareTestEndpoint, "test-env-access")
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	newShareLookupServer(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	})
	origTimeout := shareLookupTimeout
	t.Cleanup(func() { shareLookupTimeout = origTimeout })
	shareLookupTimeout = 100 * time.Millisecond

	start := time.Now()
	stdout, _, err := runConversationInProc(t, "show", shareTestLink)
	assert.Less(t, time.Since(start), 5*time.Second, "the timeout bounds the command")
	assert.Equal(t, 2, exitCodeOf(t, err))
	env := decodeConvEnvelope(t, stdout)
	require.NotNil(t, env.Error, stdout)
	assert.Equal(t, read.ErrCodeShareLinkUnresolvable, env.Error.Code)
	assert.Contains(t, env.Error.Message, "timed out")
}

// --- D. The server's answer is untrusted ---

// TestConversationShareLink_NonRecordingEntity: a share for something other
// than a recording gets its own typed error naming the type. Failure
// prevented: a shared doc reported as "unresolvable", sending the user to
// look for a recording that does not exist.
func TestConversationShareLink_NonRecordingEntity(t *testing.T) {
	useWalkthroughReader(t)
	saveShareTestLogin(t, shareTestEndpoint, "test-env-access")
	newShareLookupServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeShareTarget(w, "document", "doc_123")
	})

	stdout, _, err := runConversationInProc(t, "show", shareTestLink)
	assert.Equal(t, 2, exitCodeOf(t, err))
	env := decodeConvEnvelope(t, stdout)
	require.NotNil(t, env.Error, stdout)
	assert.Equal(t, read.ErrCodeShareLinkNotDiscussion, env.Error.Code)
	assert.Contains(t, env.Error.Message, "for a document, not a discussion")
	assert.NotEmpty(t, env.Guidance)
}

// TestConversationShareLink_MalformedEntityID: a recording id from the server
// must be a strict bare rec_/cnv_ UUIDv7 — never a URI or a link, which the
// pasted-id parser would otherwise also accept. Failure prevented: the server
// response steering the reader at another host's link or a crafted citation.
func TestConversationShareLink_MalformedEntityID(t *testing.T) {
	for _, entityID := range []string{
		"rec_not-a-uuid",
		"",
		"sageox://cnv_019ffe10-0000-7000-8000-000000000011",
		"https://evil.sageox.ai/c/" + shareTestWalkthroughRec,
		shareTestWalkthroughRec + "/../x",
	} {
		t.Run(entityID, func(t *testing.T) {
			useWalkthroughReader(t)
			saveShareTestLogin(t, shareTestEndpoint, "test-env-access")
			newShareLookupServer(t, func(w http.ResponseWriter, r *http.Request) {
				writeShareTarget(w, "recording", entityID)
			})

			stdout, _, err := runConversationInProc(t, "show", shareTestLink)
			assert.Equal(t, 2, exitCodeOf(t, err))
			env := decodeConvEnvelope(t, stdout)
			require.NotNil(t, env.Error, stdout)
			assert.Equal(t, read.ErrCodeShareLinkUnresolvable, env.Error.Code)
			assert.Contains(t, env.Error.Message, "recording id ox does not recognize")
			assert.False(t, strings.Contains(stdout, "evil"), "untrusted server output must not be echoed")
		})
	}
}

// TestConversationShareLink_401RefreshesOnce: a lookup answered 401 is
// retried once with a refreshed credential. Failure prevented: a token
// rotated server-side since the local expiry check failing every share link
// until the user re-logs.
func TestConversationShareLink_401RefreshesOnce(t *testing.T) {
	useWalkthroughReader(t)
	saveShareTestLogin(t, shareTestEndpoint, "tok-stale")
	var refreshes atomic.Int32
	shareRefresh = func(tok *auth.StoredToken, ep string) (*auth.StoredToken, error) {
		refreshes.Add(1)
		assert.Equal(t, "tok-stale", tok.AccessToken)
		return &auth.StoredToken{AccessToken: "tok-fresh"}, nil
	}
	srv := newShareLookupServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok-fresh" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		writeShareTarget(w, "recording", shareTestWalkthroughRec)
	})

	stdout, _, err := runConversationInProc(t, "show", shareTestLink)
	require.NoError(t, err, stdout)
	assert.True(t, decodeConvEnvelope(t, stdout).Success, stdout)
	assert.Equal(t, int32(1), refreshes.Load())
	_, auths := srv.requests()
	assert.Equal(t, []string{"Bearer tok-stale", "Bearer tok-fresh"}, auths)
}
