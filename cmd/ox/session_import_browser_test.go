package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/nativeimport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	importBrowserReadyID = "aaaaaaaa-1111-4111-8111-aaaaaaaaaaaa"
	importBrowserOtherID = "bbbbbbbb-2222-4222-8222-bbbbbbbbbbbb"
)

func importBrowserCandidates() []*importCandidate {
	started := time.Date(2026, time.October, 1, 14, 20, 0, 0, time.UTC)
	return []*importCandidate{
		{Session: nativeimport.Session{NativeID: importBrowserReadyID, Agent: nativeimport.AgentCodex, Path: "/private/native-session.jsonl", StartedAt: started, Prompts: 2, Replies: 2}, State: stateReady, Selected: true},
		{Session: nativeimport.Session{NativeID: importBrowserOtherID, Agent: nativeimport.AgentClaude, Path: "/private/other-session.jsonl", StartedAt: started, Prompts: 1, Replies: 1}, State: stateAlreadyImported},
	}
}

func importBrowserTestLoader(_ context.Context, id string) (*importContentPreview, error) {
	return &importContentPreview{
		NativeID: id, OpeningRequest: "Fix <img src=x onerror=alert(1)> without losing data", LastReply: "Done? Check the result.",
		Prompts: []importPromptAnchor{{EntryIndex: 0, Content: "Fix the bug"}},
		Entries: []session.Entry{{Type: session.EntryTypeUser, Content: "Fix the bug"}, {Type: session.EntryTypeAssistant, Content: "Done? Check the result."}},
	}, nil
}

func newImportBrowserTest(t *testing.T, load importPreviewLoader) *importBrowser {
	t.Helper()
	b, err := newImportBrowser("127.0.0.1:4242", "secret", importDestination{Team: "Acme", RepoID: "repo_test", Visibility: "private"}, importBrowserCandidates(), load)
	require.NoError(t, err)
	return b
}

func importBrowserRequest(b *importBrowser, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, b.origin+path, strings.NewReader(body))
	req.Header.Set("X-Import-Token", b.token)
	req.Header.Set("Content-Type", "application/json")
	if method == http.MethodPost {
		req.Header.Set("Origin", b.origin)
	}
	w := httptest.NewRecorder()
	b.handler().ServeHTTP(w, req)
	return w
}

// Prevents another website or a rebinding hostname from reading local sessions
// or changing the set returned to the CLI.
func TestImportBrowserForeignRequestsCannotReadOrSubmit(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, host, origin, token string
		status                                  int
	}{
		{"missing token", "GET", "/api/sessions", "127.0.0.1:4242", "", "", http.StatusForbidden},
		{"query token is not authorization", "GET", "/api/preview?id=" + importBrowserReadyID + "&token=secret", "127.0.0.1:4242", "", "", http.StatusForbidden},
		{"wrong token", "GET", "/api/preview?id=" + importBrowserReadyID, "127.0.0.1:4242", "", "wrong", http.StatusForbidden},
		{"rebinding host", "GET", "/api/sessions", "attacker.example:4242", "", "secret", http.StatusForbidden},
		{"foreign GET origin", "GET", "/api/sessions", "127.0.0.1:4242", "https://attacker.example", "secret", http.StatusForbidden},
		{"missing POST origin", "POST", "/api/selection", "127.0.0.1:4242", "", "secret", http.StatusForbidden},
		{"foreign POST origin", "POST", "/api/cancel", "127.0.0.1:4242", "https://attacker.example", "secret", http.StatusForbidden},
		{"cross origin preflight", "OPTIONS", "/api/selection", "127.0.0.1:4242", "https://attacker.example", "secret", http.StatusMethodNotAllowed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var reads atomic.Int32
			b := newImportBrowserTest(t, func(ctx context.Context, id string) (*importContentPreview, error) {
				reads.Add(1)
				return importBrowserTestLoader(ctx, id)
			})
			req := httptest.NewRequest(tc.method, b.origin+tc.path, strings.NewReader(`{"ids":[]}`))
			req.Host = tc.host
			req.Header.Set("Origin", tc.origin)
			req.Header.Set("X-Import-Token", tc.token)
			w := httptest.NewRecorder()
			b.handler().ServeHTTP(w, req)
			assert.Equal(t, tc.status, w.Code)
			assert.Zero(t, reads.Load())
			assert.Empty(t, w.Header().Get("Access-Control-Allow-Origin"))
			select {
			case result := <-b.result:
				t.Fatalf("rejected request returned selection: %+v", result)
			default:
			}
		})
	}
}

// Prevents the fast list or HTML document from disclosing unredacted native
// paths/content, and proves loading excerpts does not mutate the full preview.
func TestImportBrowserListsMetadataAndLoadsKnownRedactedContent(t *testing.T) {
	var reads atomic.Int32
	preview, err := importBrowserTestLoader(context.Background(), importBrowserReadyID)
	require.NoError(t, err)
	b := newImportBrowserTest(t, func(_ context.Context, _ string) (*importContentPreview, error) { reads.Add(1); return preview, nil })
	w := importBrowserRequest(b, "GET", "/api/sessions", "")
	require.Equal(t, http.StatusOK, w.Code)
	assert.Zero(t, reads.Load(), "listing must not parse every native session")
	assert.NotContains(t, w.Body.String(), "/private/")
	assert.NotContains(t, w.Body.String(), "onerror")
	var list importBrowserList
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &list))
	require.Len(t, list.Sessions, 2)
	assert.True(t, list.Sessions[0].Selected)
	assert.False(t, list.Sessions[1].Selected)

	w = importBrowserRequest(b, "GET", "/api/preview?id=/etc/passwd", "")
	assert.Equal(t, http.StatusNotFound, w.Code)
	assert.Zero(t, reads.Load())
	w = importBrowserRequest(b, "GET", "/api/preview?id="+importBrowserReadyID+"&excerpt=1", "")
	require.Equal(t, http.StatusOK, w.Code)
	var opening importContentPreview
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &opening))
	assert.Equal(t, preview.OpeningRequest, opening.OpeningRequest)
	assert.Empty(t, opening.Entries)
	assert.Empty(t, opening.Prompts)
	assert.Empty(t, opening.LastReply)
	require.Len(t, preview.Entries, 2)
	w = importBrowserRequest(b, "GET", "/api/preview?id="+importBrowserReadyID, "")
	require.Equal(t, http.StatusOK, w.Code)
	var full importContentPreview
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &full))
	assert.Equal(t, *preview, full)
	assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	assert.Equal(t, "no-referrer", w.Header().Get("Referrer-Policy"))
	assert.Contains(t, w.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'")

	w = importBrowserRequest(b, "GET", "/", "")
	require.Equal(t, http.StatusOK, w.Code)
	assert.NotContains(t, w.Body.String(), "onerror")
	assert.NotContains(t, w.Body.String(), b.token)
	assert.Contains(t, w.Body.String(), `src="/app.js"`)
	assert.Equal(t, http.StatusNotFound, importBrowserRequest(b, "GET", "/unknown", "").Code)
	assert.Equal(t, http.StatusMethodNotAllowed, importBrowserRequest(b, "POST", "/app.js", "").Code)
}

// Prevents a parser failure from leaking native content/path into the browser.
func TestImportBrowserPreviewFailuresAreSafeAndExplicit(t *testing.T) {
	for _, load := range []importPreviewLoader{
		func(context.Context, string) (*importContentPreview, error) {
			return nil, errors.New("secret raw text /private/native-session.jsonl")
		},
		func(context.Context, string) (*importContentPreview, error) { return nil, nil },
		func(context.Context, string) (*importContentPreview, error) {
			return &importContentPreview{NativeID: importBrowserOtherID}, nil
		},
	} {
		b := newImportBrowserTest(t, load)
		w := importBrowserRequest(b, "GET", "/api/preview?id="+importBrowserReadyID, "")
		assert.Equal(t, http.StatusConflict, w.Code)
		assert.Contains(t, w.Body.String(), "could not be previewed")
		assert.NotContains(t, w.Body.String(), "secret raw text")
		assert.NotContains(t, w.Body.String(), "/private/")
	}
}

// Prevents manipulated browser payloads from widening the terminal's selection.
func TestImportBrowserOnlyReturnsUniqueReadySessions(t *testing.T) {
	for _, body := range []string{
		`{"ids":["/etc/passwd"]}`,
		`{"ids":["` + importBrowserOtherID + `"]}`,
		`{"ids":["` + importBrowserReadyID + `","` + importBrowserReadyID + `"]}`,
		`{"ids":[],"path":"/etc/passwd"}`,
		`{"ids":[]} {"ids":[]}`,
		strings.Repeat(" ", 1024*1024) + `{"ids":[]}`,
		`not JSON`,
	} {
		b := newImportBrowserTest(t, importBrowserTestLoader)
		w := importBrowserRequest(b, "POST", "/api/selection", body)
		assert.Equal(t, http.StatusBadRequest, w.Code)
		select {
		case result := <-b.result:
			t.Fatalf("invalid body returned selection: %+v", result)
		default:
		}
	}
	for _, ids := range [][]string{{importBrowserReadyID}, {}} {
		b := newImportBrowserTest(t, importBrowserTestLoader)
		body, err := json.Marshal(map[string]any{"ids": ids})
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, importBrowserRequest(b, "POST", "/api/selection", string(body)).Code)
		result := <-b.result
		assert.Equal(t, ids, result.IDs)
		assert.False(t, result.Canceled)
		assert.Equal(t, http.StatusConflict, importBrowserRequest(b, "POST", "/api/cancel", "{}").Code)
	}
}

// Prevents form posts, invalid startup state, and cancellation from becoming an
// implicit import or leaving an unauthenticated server running.
func TestImportBrowserRejectsFormsAndInvalidSetup(t *testing.T) {
	b := newImportBrowserTest(t, importBrowserTestLoader)
	req := httptest.NewRequest("POST", b.origin+"/api/selection", strings.NewReader("ids="+importBrowserReadyID))
	req.Header.Set("X-Import-Token", b.token)
	req.Header.Set("Origin", b.origin)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	b.handler().ServeHTTP(w, req)
	assert.Equal(t, http.StatusUnsupportedMediaType, w.Code)
	cands := importBrowserCandidates()
	_, err := newImportBrowser(b.host, "", importDestination{}, cands, importBrowserTestLoader)
	assert.Error(t, err)
	_, err = newImportBrowser(b.host, "secret", importDestination{}, cands, nil)
	assert.Error(t, err)
	cands[1].Session.NativeID = cands[0].Session.NativeID
	_, err = newImportBrowser(b.host, "secret", importDestination{}, cands, importBrowserTestLoader)
	assert.ErrorContains(t, err, "multiple sessions")
	b = newImportBrowserTest(t, importBrowserTestLoader)
	require.Equal(t, http.StatusOK, importBrowserRequest(b, "POST", "/api/cancel", "{}").Code)
	assert.True(t, (<-b.result).Canceled)
}

// Exercises the actual ephemeral listener/open/callback/shutdown lifecycle;
// selection never calls an upload and the capability lives in the fragment.
func TestImportBrowserListenerReturnsSelectionAndCloses(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var base string
	result, err := runImportBrowserWithOpen(ctx, importDestination{}, importBrowserCandidates(), importBrowserTestLoader, func(link string) error {
		u, err := url.Parse(link)
		require.NoError(t, err)
		assert.Empty(t, u.RawQuery)
		assert.Equal(t, "127.0.0.1", u.Hostname())
		values, err := url.ParseQuery(u.Fragment)
		require.NoError(t, err)
		require.Len(t, values.Get("token"), 64)
		base = u.Scheme + "://" + u.Host
		body := bytes.NewBufferString(`{"ids":["` + importBrowserReadyID + `"]}`)
		req, err := http.NewRequestWithContext(ctx, "POST", base+"/api/selection", body)
		require.NoError(t, err)
		req.Header.Set("X-Import-Token", values.Get("token"))
		req.Header.Set("Origin", base)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		payload, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Contains(t, string(payload), `"ok":true`)
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, []string{importBrowserReadyID}, result.IDs)
	assert.False(t, result.Canceled)
	_, err = http.Get(base)
	assert.Error(t, err, "local reader must be gone before terminal confirmation")
}

// Prevents a browser launch failure or Ctrl-C from leaking a local listener.
func TestImportBrowserLaunchFailureAndCancellationCloseListener(t *testing.T) {
	for _, launchFailure := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		var base string
		result, err := runImportBrowserWithOpen(ctx, importDestination{}, importBrowserCandidates(), importBrowserTestLoader, func(link string) error {
			u, parseErr := url.Parse(link)
			require.NoError(t, parseErr)
			base = u.Scheme + "://" + u.Host
			if launchFailure {
				return errors.New("browser failed")
			}
			cancel()
			return nil
		})
		cancel()
		assert.Error(t, err)
		assert.Empty(t, result.IDs)
		_, getErr := http.Get(base)
		assert.Error(t, getErr)
	}
}
