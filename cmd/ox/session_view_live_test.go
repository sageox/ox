package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/auth"
	"github.com/spf13/cobra"
)

// traced counts liveViewTrace calls in the current test.
var traced int

const testRepo = "repo_0190aaaa"
const testSesID = "ses_0190a1b2c3d47e5f8a9b0c1d2e3f4a5b"

func TestParseLiveViewRef(t *testing.T) {
	tests := []struct {
		name     string
		raw      string
		wantOK   bool
		wantID   string
		wantHost string
	}{
		{"bare ses", testSesID, true, testSesID, ""},
		{"bare cnv", "cnv_0190a1b2c3d47e5f8a9b0c1d2e3f4a5b", true, "cnv_0190a1b2c3d47e5f8a9b0c1d2e3f4a5b", ""},
		{"link", "https://sageox.ai/c/" + testSesID, true, testSesID, "sageox.ai"},
		{"link trailing slash", "https://sageox.ai/c/" + testSesID + "/", true, testSesID, "sageox.ai"},
		{"link query and fragment", "https://sageox.ai/c/" + testSesID + "?x=1#top", true, testSesID, "sageox.ai"},
		{"test env and surrounding space", "  https://test.sageox.ai/c/" + testSesID + "\n", true, testSesID, "test.sageox.ai"},
		{"app prefix normalized", "https://app.sageox.ai/c/" + testSesID, true, testSesID, "sageox.ai"},
		{"foreign host", "https://evil.example/c/" + testSesID, false, "", ""},
		{"lookalike host", "https://sageox.ai.evil.example/c/" + testSesID, false, "", ""},
		{"wrong path", "https://sageox.ai/s/" + testSesID, false, "", ""},
		{"extra segment", "https://sageox.ai/c/" + testSesID + "/x", false, "", ""},
		{"rec link is not a session", "https://sageox.ai/c/rec_0190a1b2c3d47e5f8a9b0c1d2e3f4a5b", false, "", ""},
		{"path traversal", "ses_../../etc", false, "", ""},
		{"slash in id", "ses_abc/def123", false, "", ""},
		{"query in bare id", "ses_abcdef?x=1", false, "", ""},
		{"too short", "ses_ab", false, "", ""},
		{"v1 name", "2026-01-06T14-32-ryan", false, "", ""},
		{"empty", "", false, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ref, ok := parseLiveViewRef(tt.raw)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v (ref %+v)", ok, tt.wantOK, ref)
			}
			if ok && (ref.ID != tt.wantID || ref.Host != tt.wantHost) {
				t.Fatalf("ref = %+v, want id %q host %q", ref, tt.wantID, tt.wantHost)
			}
		})
	}
}

func TestLooksLikeLiveViewArg(t *testing.T) {
	for raw, want := range map[string]bool{
		testSesID: true, "cnv_x": true, "https://sageox.ai/c/ses_zz": true,
		"2026-01-06T14-32-ryan": false, "rec_abc": false,
	} {
		if got := looksLikeLiveViewArg(raw); got != want {
			t.Errorf("looksLikeLiveViewArg(%q) = %v, want %v", raw, got, want)
		}
	}
}

// fakeAgentView serves canned responses in order and records each request URL.
type fakeAgentView struct {
	mu        sync.Mutex
	responses []map[string]any
	status    int
	// throttle: first N requests answer 429 with Retry-After: 2
	throttle int
	queries  []string
	auth     []string
	paths    []string
}

func (f *fakeAgentView) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.queries = append(f.queries, r.URL.RawQuery)
		f.auth = append(f.auth, r.Header.Get("Authorization"))
		f.paths = append(f.paths, r.URL.Path)
		if len(f.queries) <= f.throttle {
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		if f.status != 0 {
			w.WriteHeader(f.status)
			return
		}
		i := len(f.queries) - 1 - f.throttle
		if i >= len(f.responses) {
			i = len(f.responses) - 1
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(f.responses[i])
	})
}

// newLiveViewCmd builds a command with the live-view flags and points the
// client seam at srv; waits are recorded, never slept.
func newLiveViewCmd(t *testing.T, srv *httptest.Server, args ...string) (*cobra.Command, *bytes.Buffer, *int) {
	t.Helper()
	cmd := &cobra.Command{Use: "view"}
	cmd.Flags().String("since", "", "")
	cmd.Flags().Int("budget", 0, "")
	cmd.Flags().String("layer", "", "")
	cmd.Flags().Bool("json", false, "")
	cmd.Flags().Bool("follow", false, "")
	if err := cmd.ParseFlags(args); err != nil {
		t.Fatal(err)
	}
	out := &bytes.Buffer{}
	cmd.SetOut(out)
	cmd.SetContext(context.Background())

	origClient, origWait := liveViewNewClient, liveViewWait
	waits := 0
	liveViewNewClient = func(context.Context, liveViewRef) (liveViewFetcher, string, error) {
		return api.NewRepoClientWithEndpoint(srv.URL).WithAuthToken("tok").WithTimeout(5 * time.Second), "https://sageox.ai", nil
	}
	liveViewWait = func(context.Context, time.Duration) error { waits++; return nil }
	origProj := liveViewProject
	liveViewProject = func() (string, string) { return testRepo, "/proj" }
	origTrace := liveViewTrace
	liveViewTrace = func(string, *api.AgentView, string, string) { traced++ }
	traced = 0
	t.Cleanup(func() {
		liveViewNewClient, liveViewWait, liveViewProject, liveViewTrace = origClient, origWait, origProj, origTrace
	})
	return cmd, out, &waits
}

func liveResp(status string, live bool, since, md string) map[string]any {
	return map[string]any{"session_id": testSesID, "status": status, "live": live, "next_since": since, "markdown": md, "future_field": 1, "team_match": true}
}

func TestLiveView_PrintsMarkdownAndPassesQuery(t *testing.T) {
	f := &fakeAgentView{responses: []map[string]any{liveResp("live", true, "c1", "Status: LIVE (still updating). x\n\n## Turn 1\n")}}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()
	cmd, out, _ := newLiveViewCmd(t, srv, "--since", "cur0", "--budget", "5000", "--layer", "clyr_abc")

	if err := runLiveSessionView(cmd, liveViewRef{ID: testSesID}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.String(), "Status: LIVE") || !strings.Contains(out.String(), "## Turn 1") {
		t.Fatalf("output = %q", out.String())
	}
	if f.paths[0] != "/api/v1/sessions/"+testSesID+"/agent-view" {
		t.Fatalf("path = %q", f.paths[0])
	}
	if f.auth[0] != "Bearer tok" {
		t.Fatalf("auth = %q", f.auth[0])
	}
	for _, want := range []string{"since=cur0", "budget=5000", "layer=clyr_abc"} {
		if !strings.Contains(f.queries[0], want) {
			t.Errorf("query %q missing %q", f.queries[0], want)
		}
	}
}

func TestLiveView_JSON(t *testing.T) {
	f := &fakeAgentView{responses: []map[string]any{liveResp("finished", false, "", "Status: FINISHED at t. 3 turns.\n")}}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()
	cmd, out, _ := newLiveViewCmd(t, srv, "--json")

	if err := runLiveSessionView(cmd, liveViewRef{ID: testSesID}); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("not json: %v\n%s", err, out.String())
	}
	if got["status"] != "finished" || got["future_field"] == nil {
		t.Fatalf("json = %v", got)
	}
}

func TestLiveView_FollowStopsWhenStatusLeavesLive(t *testing.T) {
	f := &fakeAgentView{responses: []map[string]any{
		liveResp("live", true, "c1", "Status: LIVE (still updating). 1 turn\n\n## Turn 1\nfirst\n"),
		liveResp("live", true, "c1", "Status: LIVE (still updating). 1 turn\n"), // nothing new
		liveResp("live", true, "c2", "Status: LIVE (still updating). 2 turns\n\n## Turn 2\nsecond\n"),
		liveResp("finished", false, "c3", "Status: FINISHED at t. 2 turns.\n"),
	}}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()
	cmd, out, waits := newLiveViewCmd(t, srv, "--follow")

	if err := runLiveSessionView(cmd, liveViewRef{ID: testSesID}); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if strings.Count(got, "first") != 1 || strings.Count(got, "second") != 1 {
		t.Fatalf("each turn must print once:\n%s", got)
	}
	if strings.Count(got, "Status: LIVE") != 1 {
		t.Fatalf("live status line printed on polls:\n%s", got)
	}
	if !strings.HasSuffix(strings.TrimSpace(got), "Status: FINISHED at t. 2 turns.") {
		t.Fatalf("missing closing status:\n%s", got)
	}
	if *waits != 3 || len(f.queries) != 4 {
		t.Fatalf("waits=%d requests=%d", *waits, len(f.queries))
	}
	// polls resume from the previous next_since
	if !strings.Contains(f.queries[1], "since=c1") || !strings.Contains(f.queries[3], "since=c2") {
		t.Fatalf("queries = %v", f.queries)
	}
}

func TestLiveView_FollowCtrlCExitsCleanly(t *testing.T) {
	f := &fakeAgentView{responses: []map[string]any{liveResp("live", true, "c1", "Status: LIVE. x\n")}}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()
	cmd, out, _ := newLiveViewCmd(t, srv, "--follow")
	liveViewWait = func(context.Context, time.Duration) error { return context.Canceled }

	if err := runLiveSessionView(cmd, liveViewRef{ID: testSesID}); err != nil {
		t.Fatalf("ctrl-c must not be an error: %v", err)
	}
	if !strings.Contains(out.String(), "Status: LIVE") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestLiveView_Errors(t *testing.T) {
	tests := []struct {
		status int
		want   string
	}{
		{http.StatusNotFound, "not found, or you do not have access"},
		{http.StatusUnauthorized, "ox login"},
	}
	for _, tt := range tests {
		f := &fakeAgentView{status: tt.status}
		srv := httptest.NewServer(f.handler())
		cmd, _, _ := newLiveViewCmd(t, srv)
		err := runLiveSessionView(cmd, liveViewRef{ID: testSesID})
		srv.Close()
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("status %d: err = %v, want containing %q", tt.status, err, tt.want)
		}
	}
}

func TestLiveView_FlagValidation(t *testing.T) {
	srv := httptest.NewServer((&fakeAgentView{responses: []map[string]any{liveResp("live", true, "", "x")}}).handler())
	defer srv.Close()
	for _, args := range [][]string{{"--layer", "bogus"}, {"--follow", "--layer", "clyr_a"}, {"--budget", "-1"}} {
		cmd, _, _ := newLiveViewCmd(t, srv, args...)
		if err := runLiveSessionView(cmd, liveViewRef{ID: testSesID}); err == nil {
			t.Errorf("args %v: expected error", args)
		}
	}
}

func TestLiveView_TerminalEscapesPrintInert(t *testing.T) {
	evil := "Status: FINISHED at t. 1 turns.\n\x1b]8;;https://evil.example\x07click\x1b]8;;\x07\n\x1b]0;pwned\x07title\n\x1b[2J\x1b[Hclear\n\x9b31mred\x00bell\x07\tkeep-tab\r\nnext"
	f := &fakeAgentView{responses: []map[string]any{liveResp("finished", false, "", evil)}}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()

	cmd, out, _ := newLiveViewCmd(t, srv)
	if err := runLiveSessionView(cmd, liveViewRef{ID: testSesID}); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, bad := range []string{"\x1b", "\x07", "\x00", "\x9b", "\r", "evil.example", "pwned", "[2J"} {
		if strings.Contains(got, bad) {
			t.Errorf("output still contains %q:\n%q", bad, got)
		}
	}
	for _, keep := range []string{"click", "title", "clear", "red", "\tkeep-tab", "\nnext"} {
		if !strings.Contains(got, keep) {
			t.Errorf("output lost %q:\n%q", keep, got)
		}
	}

	// --json stays byte-exact (the escapes are JSON-escaped on the wire)
	cmd, out, _ = newLiveViewCmd(t, srv, "--json")
	if err := runLiveSessionView(cmd, liveViewRef{ID: testSesID}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `\u001b]8;;https://evil.example`) {
		t.Errorf("--json was altered: %s", out.String())
	}
}

func TestLiveView_ContextRepoSentAndOmitted(t *testing.T) {
	for _, tc := range []struct {
		repo string
		want bool
	}{{testRepo, true}, {"", false}} {
		f := &fakeAgentView{responses: []map[string]any{
			liveResp("live", true, "c1", "Status: LIVE. a\n"),
			liveResp("finished", false, "c2", "Status: FINISHED at t. 1 turns.\n"),
		}}
		srv := httptest.NewServer(f.handler())
		cmd, _, _ := newLiveViewCmd(t, srv, "--follow")
		repo := tc.repo
		liveViewProject = func() (string, string) { return repo, "/proj" }
		if err := runLiveSessionView(cmd, liveViewRef{ID: testSesID}); err != nil {
			t.Fatal(err)
		}
		srv.Close()
		if len(f.queries) != 2 {
			t.Fatalf("requests = %d", len(f.queries))
		}
		for _, q := range f.queries { // every request, including follow polls
			if got := strings.Contains(q, "context_repo="+testRepo); got != tc.want {
				t.Errorf("repo %q: query %q context_repo present = %v, want %v", tc.repo, q, got, tc.want)
			}
			if !tc.want && strings.Contains(q, "context_repo") {
				t.Errorf("context_repo sent without a project: %q", q)
			}
		}
	}
}

func withheldResp(status string, live bool) map[string]any {
	return map[string]any{
		"session_id": testSesID, "status": status, "live": live, "team_match": false,
		"title": "SECRET-TITLE", "markdown": "", "notice": "Server says: another team. Open it in the browser.",
	}
}

func TestLiveView_WithheldPrintsNoTitleOrContent(t *testing.T) {
	for _, mode := range []string{"text", "json", "follow"} {
		t.Run(mode, func(t *testing.T) {
			f := &fakeAgentView{responses: []map[string]any{withheldResp("live", true), withheldResp("finished", false)}}
			srv := httptest.NewServer(f.handler())
			defer srv.Close()
			var args []string
			switch mode {
			case "json":
				args = []string{"--json"}
			case "follow":
				args = []string{"--follow"}
			}
			cmd, out, _ := newLiveViewCmd(t, srv, args...)
			if err := runLiveSessionView(cmd, liveViewRef{ID: testSesID}); err != nil {
				t.Fatal(err)
			}
			got := out.String()
			for _, bad := range []string{"SECRET-TITLE", "title", "Title", "markdown"} {
				if strings.Contains(got, bad) {
					t.Fatalf("leaked %q:\n%s", bad, got)
				}
			}
			if !strings.Contains(got, "https://sageox.ai/c/"+testSesID) || !strings.Contains(got, "Server says: another team") {
				t.Fatalf("missing link or server notice:\n%s", got)
			}
			if mode == "json" {
				var m map[string]any
				if err := json.Unmarshal(out.Bytes(), &m); err != nil {
					t.Fatalf("not json: %v", err)
				}
			}
			if mode == "follow" && !strings.Contains(got, "FINISHED") {
				t.Fatalf("follow did not report the status change:\n%s", got)
			}
		})
	}
}

func TestLiveView_RateLimitRetriesOnce(t *testing.T) {
	f := &fakeAgentView{throttle: 1, responses: []map[string]any{liveResp("finished", false, "", "Status: FINISHED at t. 1 turns.\nbody\n")}}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()
	cmd, out, waits := newLiveViewCmd(t, srv)
	var waited []time.Duration
	liveViewWait = func(_ context.Context, d time.Duration) error { waited = append(waited, d); return nil }
	_ = waits
	if err := runLiveSessionView(cmd, liveViewRef{ID: testSesID}); err != nil {
		t.Fatal(err)
	}
	if len(f.queries) != 2 || len(waited) != 1 || waited[0] != 2*time.Second {
		t.Fatalf("requests=%d waited=%v", len(f.queries), waited)
	}
	if !strings.Contains(out.String(), "body") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestLiveView_FollowSurvivesPersistentRateLimit(t *testing.T) {
	// every attempt of the first poll is a 429, then the session finishes
	f := &fakeAgentView{throttle: maxFetchAttempts, responses: []map[string]any{liveResp("finished", false, "", "Status: FINISHED at t. 1 turns.\nbody\n")}}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()
	cmd, out, _ := newLiveViewCmd(t, srv, "--follow")
	if err := runLiveSessionView(cmd, liveViewRef{ID: testSesID}); err != nil {
		t.Fatalf("follow must keep going: %v", err)
	}
	if !strings.Contains(out.String(), "body") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestLiveView_RateLimitWithoutFollowReportsAfterRetries(t *testing.T) {
	f := &fakeAgentView{throttle: 9, responses: []map[string]any{liveResp("live", true, "", "x")}}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()
	cmd, _, _ := newLiveViewCmd(t, srv)
	err := runLiveSessionView(cmd, liveViewRef{ID: testSesID})
	if err == nil || !strings.Contains(err.Error(), "rate limited") || len(f.queries) != maxFetchAttempts {
		t.Fatalf("err=%v requests=%d", err, len(f.queries))
	}
}

func TestLiveView_MissingOrNullTeamMatchIsWithheld(t *testing.T) {
	for _, mode := range []string{"absent", "null"} {
		for _, args := range [][]string{nil, {"--json"}} {
			resp := liveResp("finished", false, "", "Status: FINISHED at t. 1 turns.\nSECRET-BODY\n")
			resp["title"] = "SECRET-TITLE"
			if mode == "absent" {
				delete(resp, "team_match")
			} else {
				resp["team_match"] = nil
			}
			srv := httptest.NewServer((&fakeAgentView{responses: []map[string]any{resp}}).handler())
			cmd, out, _ := newLiveViewCmd(t, srv, args...)
			if err := runLiveSessionView(cmd, liveViewRef{ID: testSesID}); err != nil {
				t.Fatal(err)
			}
			srv.Close()
			got := out.String()
			for _, bad := range []string{"SECRET-BODY", "SECRET-TITLE", "markdown"} {
				if strings.Contains(got, bad) {
					t.Errorf("%s %v leaked %q:\n%s", mode, args, bad, got)
				}
			}
			if !strings.Contains(got, "/c/"+testSesID) {
				t.Errorf("%s %v missing link:\n%s", mode, args, got)
			}
		}
	}
}

func TestLiveView_WithheldNeverCopiesMarkdown(t *testing.T) {
	for _, tm := range []any{false, nil, "absent"} {
		resp := liveResp("finished", false, "", "Status: FINISHED — PRIVATE-TITLE\nPRIVATE-BODY\n")
		switch tm {
		case "absent":
			delete(resp, "team_match")
		default:
			resp["team_match"] = tm
		}
		for _, args := range [][]string{nil, {"--follow"}} {
			srv := httptest.NewServer((&fakeAgentView{responses: []map[string]any{resp}}).handler())
			cmd, out, _ := newLiveViewCmd(t, srv, args...)
			if err := runLiveSessionView(cmd, liveViewRef{ID: testSesID}); err != nil {
				t.Fatal(err)
			}
			srv.Close()
			if got := out.String(); strings.Contains(got, "PRIVATE") || !strings.Contains(got, "Status: FINISHED.") {
				t.Errorf("team_match=%v %v:\n%s", tm, args, got)
			}
			if traced != 0 {
				t.Errorf("link-only output must not be traced")
			}
		}
	}
}

func TestLiveView_LinkUsesRequestEndpoint(t *testing.T) {
	srv := httptest.NewServer((&fakeAgentView{responses: []map[string]any{withheldResp("finished", false)}}).handler())
	defer srv.Close()
	for _, mode := range []string{"text", "json"} {
		var args []string
		if mode == "json" {
			args = []string{"--json"}
		}
		cmd, out, _ := newLiveViewCmd(t, srv, args...)
		liveViewNewClient = func(context.Context, liveViewRef) (liveViewFetcher, string, error) {
			return api.NewRepoClientWithEndpoint(srv.URL).WithAuthToken("tok"), "https://test.sageox.ai", nil
		}
		if err := runLiveSessionView(cmd, liveViewRef{ID: testSesID, Host: "test.sageox.ai"}); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "https://test.sageox.ai/c/"+testSesID) || strings.Contains(out.String(), "https://sageox.ai/c/") {
			t.Errorf("%s link not on the request endpoint:\n%s", mode, out.String())
		}
	}
}

func TestLiveView_ContentIsTracedOnce(t *testing.T) {
	f := &fakeAgentView{responses: []map[string]any{
		liveResp("live", true, "c1", "Status: LIVE. a\n"),
		liveResp("finished", false, "c2", "Status: FINISHED at t. 1 turns.\n"),
	}}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()
	cmd, _, _ := newLiveViewCmd(t, srv, "--follow")
	if err := runLiveSessionView(cmd, liveViewRef{ID: testSesID}); err != nil {
		t.Fatal(err)
	}
	if traced != 1 {
		t.Fatalf("traced = %d, want 1", traced)
	}
}

func TestLiveView_FollowPagesTruncatedFinishedSession(t *testing.T) {
	p1 := liveResp("finished", false, "p2", "Status: FINISHED at t. 3 turns.\n## Turn 1\none\n")
	p1["truncated"] = true
	p2 := liveResp("finished", false, "p3", "Status: FINISHED at t. 3 turns.\n## Turn 2\ntwo\n")
	p2["truncated"] = true
	p3 := liveResp("finished", false, "", "Status: FINISHED at t. 3 turns.\n## Turn 3\nthree\n")
	f := &fakeAgentView{responses: []map[string]any{p1, p2, p3}}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()
	cmd, out, waits := newLiveViewCmd(t, srv, "--follow")
	if err := runLiveSessionView(cmd, liveViewRef{ID: testSesID}); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{"one", "two", "three"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q:\n%s", want, got)
		}
	}
	if len(f.queries) != 3 || *waits != 0 || !strings.Contains(f.queries[1], "since=p2") || !strings.Contains(f.queries[2], "since=p3") {
		t.Fatalf("queries=%v waits=%d", f.queries, *waits)
	}
}

func TestLiveView_RejectsLocalOnlyFlags(t *testing.T) {
	for _, flag := range []string{"metadata", "context", "input", "limit", "latest", "text"} {
		cmd := &cobra.Command{Use: "view"}
		cmd.Flags().Bool("metadata", false, "")
		cmd.Flags().Bool("context", false, "")
		cmd.Flags().String("input", "", "")
		cmd.Flags().Int("limit", 0, "")
		cmd.Flags().Bool("latest", false, "")
		cmd.Flags().Bool("text", false, "")
		arg := "true"
		switch flag {
		case "input":
			arg = "x.jsonl"
		case "limit":
			arg = "5"
		}
		if err := cmd.Flags().Set(flag, arg); err != nil {
			t.Fatal(err)
		}
		err := runLiveSessionView(cmd, liveViewRef{ID: testSesID})
		if err == nil || !strings.Contains(err.Error(), "--"+flag) || !strings.Contains(err.Error(), "local sessions only") {
			t.Errorf("--%s: err = %v", flag, err)
		}
	}
}

func TestFetchWithRetry_Policy(t *testing.T) {
	tests := []struct {
		name         string
		statuses     []int // per attempt; 200 ends the sequence
		wantRequests int
		wantErr      bool
	}{
		{"401 settled", []int{401}, 1, true},
		{"403 settled", []int{403}, 1, true},
		{"404 settled", []int{404}, 1, true},
		{"410 settled", []int{410}, 1, true},
		{"400 settled", []int{400}, 1, true},
		{"500 then ok", []int{500, 200}, 2, false},
		{"502 502 ok", []int{502, 502, 200}, 3, false},
		{"always 500 is bounded", []int{500, 500, 500, 500, 500}, maxFetchAttempts, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				st := tt.statuses[min(n, len(tt.statuses)-1)]
				n++
				if st != 200 {
					w.WriteHeader(st)
					return
				}
				_ = json.NewEncoder(w).Encode(liveResp("live", true, "", "x"))
			}))
			defer srv.Close()
			cmd, _, _ := newLiveViewCmd(t, srv)
			_ = cmd
			client := api.NewRepoClientWithEndpoint(srv.URL).WithAuthToken("tok")
			_, err := fetchWithRetry(context.Background(), client, testSesID, api.SessionAgentViewOptions{})
			if (err != nil) != tt.wantErr || n != tt.wantRequests {
				t.Fatalf("err=%v requests=%d, want err=%v requests=%d", err, n, tt.wantErr, tt.wantRequests)
			}
		})
	}
}

func TestRefreshingFetcher_RefreshesAndRetriesAfter401(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("Authorization"))
		if r.Header.Get("Authorization") != "Bearer fresh" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(liveResp("live", true, "", "x"))
	}))
	defer srv.Close()
	origE, origF, origB := liveViewEnsureToken, liveViewForceRefresh, liveViewBaseURL
	t.Cleanup(func() { liveViewEnsureToken, liveViewForceRefresh, liveViewBaseURL = origE, origF, origB })
	liveViewEnsureToken = func(context.Context, string) (string, error) { return "stale", nil }
	refreshes := 0
	liveViewForceRefresh = func(context.Context, string) (string, error) { refreshes++; return "fresh", nil }
	liveViewBaseURL = func(string) string { return srv.URL }

	f := &refreshingFetcher{ep: "https://sageox.ai"}
	if _, err := f.GetSessionAgentView(context.Background(), testSesID, api.SessionAgentViewOptions{}); err != nil {
		t.Fatal(err)
	}
	if refreshes != 1 || len(seen) != 2 || seen[0] != "Bearer stale" || seen[1] != "Bearer fresh" {
		t.Fatalf("refreshes=%d seen=%v", refreshes, seen)
	}

	// a failed refresh stays an auth error (one retry at most)
	liveViewForceRefresh = func(context.Context, string) (string, error) { return "", errors.New("nope") }
	if _, err := f.GetSessionAgentView(context.Background(), testSesID, api.SessionAgentViewOptions{}); !errors.Is(err, api.ErrUnauthorized) {
		t.Fatalf("err = %v", err)
	}
}

func TestNewLiveViewClient_BareIDUsesEndpointOverride(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("SAGEOX_ENDPOINT", "https://test.sageox.ai")
	orig := liveViewEnsureToken
	t.Cleanup(func() { liveViewEnsureToken = orig })
	liveViewEnsureToken = func(context.Context, string) (string, error) { return "tok", nil }
	_, ep, err := newLiveViewClient(context.Background(), liveViewRef{ID: testSesID})
	if err != nil || !strings.HasPrefix(ep, "https://test.sageox.ai") {
		t.Fatalf("ep=%q err=%v", ep, err)
	}
}

func TestLiveStoredEndpoint_FindsExpiredLogin(t *testing.T) {
	dir := t.TempDir()
	for _, k := range []string{"HOME", "XDG_DATA_HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME"} {
		t.Setenv(k, dir)
	}
	t.Setenv("SAGEOX_TOKEN", "")
	expired := &auth.StoredToken{AccessToken: "old", RefreshToken: "r", ExpiresAt: time.Now().Add(-time.Hour)}
	if err := auth.SaveTokenForEndpoint("https://test.sageox.ai", expired); err != nil {
		t.Skipf("cannot seed auth store hermetically: %v", err)
	}
	if got := liveStoredEndpoint("test.sageox.ai"); !strings.Contains(got, "test.sageox.ai") {
		t.Fatalf("expired-but-refreshable login not found: %q", got)
	}
	if got := liveStoredEndpoint("other.sageox.ai"); got != "" {
		t.Fatalf("unexpected match %q", got)
	}
}

func TestLiveConversationShow_FormatsAndEnvelope(t *testing.T) {
	good := liveResp("finished", false, "", "Status: FINISHED at t. 1 turns.\nbody\n")
	good["title"] = "T"
	newCmd := func(srv *httptest.Server) (*cobra.Command, *bytes.Buffer) {
		cmd, out, _ := newLiveViewCmd(t, srv)
		return cmd, out
	}
	t.Run("json envelope", func(t *testing.T) {
		srv := httptest.NewServer((&fakeAgentView{responses: []map[string]any{good}}).handler())
		defer srv.Close()
		cmd, out := newCmd(srv)
		if err := runLiveConversationShow(cmd, liveViewRef{ID: testSesID}, "json"); err != nil {
			t.Fatal(err)
		}
		var env struct {
			Success bool `json:"success"`
			Data    struct {
				SessionID string `json:"session_id"`
				Markdown  string `json:"markdown"`
			} `json:"data"`
		}
		if err := json.Unmarshal(out.Bytes(), &env); err != nil || !env.Success || env.Data.SessionID != testSesID || !strings.Contains(env.Data.Markdown, "body") {
			t.Fatalf("err=%v env=%+v\n%s", err, env, out.String())
		}
		if traced != 1 {
			t.Errorf("traced = %d", traced)
		}
	})
	t.Run("text", func(t *testing.T) {
		srv := httptest.NewServer((&fakeAgentView{responses: []map[string]any{good}}).handler())
		defer srv.Close()
		cmd, out := newCmd(srv)
		if err := runLiveConversationShow(cmd, liveViewRef{ID: testSesID}, "text"); err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(out.String(), "Status: FINISHED") {
			t.Fatalf("out = %q", out.String())
		}
	})
	t.Run("api failure is an error envelope", func(t *testing.T) {
		srv := httptest.NewServer((&fakeAgentView{status: http.StatusNotFound}).handler())
		defer srv.Close()
		cmd, out := newCmd(srv)
		if err := runLiveConversationShow(cmd, liveViewRef{ID: testSesID}, "json"); err == nil {
			t.Fatal("want a non-nil exit error")
		}
		var env struct {
			Success bool `json:"success"`
			Error   struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal(out.Bytes(), &env); err != nil || env.Success || env.Error.Code == "" {
			t.Fatalf("err=%v env=%+v\n%s", err, env, out.String())
		}
	})
	t.Run("withheld is link-only json", func(t *testing.T) {
		srv := httptest.NewServer((&fakeAgentView{responses: []map[string]any{withheldResp("live", true)}}).handler())
		defer srv.Close()
		cmd, out := newCmd(srv)
		if err := runLiveConversationShow(cmd, liveViewRef{ID: testSesID}, "json"); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out.String(), "SECRET-TITLE") || strings.Contains(out.String(), "markdown") || traced != 0 {
			t.Fatalf("leak or trace:\n%s", out.String())
		}
	})
}

func TestLiveView_FollowPollsDoNotConsumePagingAllowance(t *testing.T) {
	var resps []map[string]any
	for i := 0; i < maxFollowPages+5; i++ {
		resps = append(resps, liveResp("live", true, fmt.Sprintf("c%d", i), "Status: LIVE. x\n"))
	}
	p1 := liveResp("finished", false, "last1", "Status: FINISHED at t. 9 turns.\nfinal-one\n")
	p1["truncated"] = true
	p2 := liveResp("finished", false, "", "Status: FINISHED at t. 9 turns.\nfinal-two\n")
	resps = append(resps, p1, p2)
	f := &fakeAgentView{responses: resps}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()
	cmd, out, _ := newLiveViewCmd(t, srv, "--follow")
	if err := runLiveSessionView(cmd, liveViewRef{ID: testSesID}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "final-one") || !strings.Contains(out.String(), "final-two") {
		t.Fatalf("remaining pages not drained after many polls:\n%s", out.String())
	}
}

func TestLiveSessionHit_NoTitleAndKeepsAliases(t *testing.T) {
	view := &api.AgentView{SessionID: testSesID, ConversationID: "cnv_0190a1b2c3d47e5f8a9b0c1d2e3f4a5b", Title: "AKIAIOSFODNN7EXAMPLE secret title"}
	hit := liveSessionHit(view, "cnv_0190a1b2c3d47e5f8a9b0c1d2e3f4a5b", "https://sageox.ai")
	if hit.Title != "" {
		t.Fatalf("title must not reach the trace: %q", hit.Title)
	}
	if hit.Ref != testSesID || hit.RefType != "session" || !slices.Contains(hit.Aliases, "cnv_0190a1b2c3d47e5f8a9b0c1d2e3f4a5b") {
		t.Fatalf("hit = %+v", hit.SurfacedRef)
	}
}

func TestLiveConversationShow_EnvelopeMetadata(t *testing.T) {
	resp := liveResp("live", true, "c9", "Status: LIVE. x\n\n## Turn 1\nhello\n")
	resp["truncated"] = true
	srv := httptest.NewServer((&fakeAgentView{responses: []map[string]any{resp}}).handler())
	defer srv.Close()
	cmd, out, _ := newLiveViewCmd(t, srv)
	if err := runLiveConversationShow(cmd, liveViewRef{ID: testSesID}, "json"); err != nil {
		t.Fatal(err)
	}
	var env struct {
		TokenEstimate int    `json:"token_estimate"`
		Guidance      string `json:"guidance"`
	}
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	// the shared newLiveViewCmd endpoint is https://sageox.ai; the guidance must
	// carry the request endpoint, not a bare id
	if env.TokenEstimate <= 0 || !strings.Contains(env.Guidance, "ox session view https://sageox.ai/c/"+testSesID+" --since c9") {
		t.Fatalf("env = %+v", env)
	}
}

func TestLiveConversationShow_GuidanceIsSanitized(t *testing.T) {
	r := withheldResp("live", true)
	r["notice"] = "open it\x1b]8;;https://evil.example\x07link\x1b]8;;\x07"
	srv := httptest.NewServer((&fakeAgentView{responses: []map[string]any{r}}).handler())
	defer srv.Close()
	cmd, out, _ := newLiveViewCmd(t, srv)
	if err := runLiveConversationShow(cmd, liveViewRef{ID: testSesID}, "text"); err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(out.String(), "\x1b\x07") || strings.Contains(out.String(), "evil.example") {
		t.Fatalf("escapes survived: %q", out.String())
	}
}

func TestSettledLiveError_RequestTimeoutIsRetryable(t *testing.T) {
	if settledLiveError(fmt.Errorf("session view request: %w", context.DeadlineExceeded)) {
		t.Fatal("a per-request timeout must be retried")
	}
	// but an expired command context stops the retry loop
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	_, err := fetchWithRetry(ctx, fakeFetcher(func() (*api.AgentView, error) { calls++; return nil, context.DeadlineExceeded }), testSesID, api.SessionAgentViewOptions{})
	if err == nil || calls != 1 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}

type fakeFetcher func() (*api.AgentView, error)

func (f fakeFetcher) GetSessionAgentView(context.Context, string, api.SessionAgentViewOptions) (*api.AgentView, error) {
	return f()
}

func TestLiveProjectRoot_RequiresInitializedConfig(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if got := liveProjectRoot(); got != "" {
		t.Fatalf("no .sageox: root = %q", got)
	}
	if err := os.Mkdir(filepath.Join(dir, ".sageox"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := liveProjectRoot(); got != "" {
		t.Fatalf("empty .sageox must not count as a project: root = %q", got)
	}
	if err := os.WriteFile(filepath.Join(dir, ".sageox", "config.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := liveProjectRoot(); got == "" {
		t.Fatal("initialized project not detected")
	}
}

func TestLiveShowGuidance_KeepsRequestEndpoint(t *testing.T) {
	view := &api.AgentView{SessionID: testSesID, Truncated: true, NextSince: "c1"}
	got := liveShowGuidance(view, testSesID, "https://test.sageox.ai")
	if !strings.Contains(got, "https://test.sageox.ai/c/"+testSesID+" --since c1") || strings.Contains(got, "https://sageox.ai/") {
		t.Fatalf("guidance = %q", got)
	}
}

func TestLiveShowGuidance_OnlyRoundTrippingLinks(t *testing.T) {
	view := &api.AgentView{SessionID: testSesID, Truncated: true, NextSince: "c1"}
	for _, tc := range []struct {
		name, ep string
		wantLink bool
	}{
		{"prod", "https://sageox.ai", true},
		{"test env", "https://test.sageox.ai", true},
		{"localhost", "http://localhost:3000", false},
		{"self-hosted", "https://sageox.example.com", false},
		{"no endpoint", "", false},
	} {
		got := liveShowGuidance(view, testSesID, tc.ep)
		hasLink := strings.Contains(got, "https://") && strings.Contains(got, "/c/"+testSesID)
		if hasLink != tc.wantLink || !strings.Contains(got, "ox session view ") || !strings.Contains(got, testSesID) {
			t.Errorf("%s: guidance = %q", tc.name, got)
		}
		if !tc.wantLink && !strings.Contains(got, "ox session view "+testSesID+" --since c1") {
			t.Errorf("%s: want the bare id, got %q", tc.name, got)
		}
	}
}

func TestNewLiveViewClient_LoginErrorsAreSanitized(t *testing.T) {
	orig := liveViewEnsureToken
	t.Cleanup(func() { liveViewEnsureToken = orig })
	liveViewEnsureToken = func(context.Context, string) (string, error) { return "", errors.New("no") }
	t.Setenv("SAGEOX_ENDPOINT", "https://x.sageox.ai\x1b]0;pwned\x07")
	t.Chdir(t.TempDir())
	_, _, err := newLiveViewClient(context.Background(), liveViewRef{ID: testSesID})
	if err == nil || strings.ContainsAny(err.Error(), "\x1b\x07") {
		t.Fatalf("err = %q", err)
	}
	_, _, err = newLiveViewClient(context.Background(), liveViewRef{ID: testSesID, Host: "evil\x1b[2J.sageox.ai"})
	if err == nil || strings.ContainsAny(err.Error(), "\x1b") {
		t.Fatalf("err = %q", err)
	}
}
