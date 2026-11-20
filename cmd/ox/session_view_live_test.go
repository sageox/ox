package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/spf13/cobra"
)

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
	liveViewNewClient = func(context.Context, liveViewRef) (*api.RepoClient, error) {
		return api.NewRepoClientWithEndpoint(srv.URL).WithAuthToken("tok").WithTimeout(5 * time.Second), nil
	}
	liveViewWait = func(context.Context, time.Duration) error { waits++; return nil }
	origProj := liveViewProject
	liveViewProject = func() (string, string) { return testRepo, "https://sageox.ai" }
	t.Cleanup(func() { liveViewNewClient, liveViewWait, liveViewProject = origClient, origWait, origProj })
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
		liveViewProject = func() (string, string) { return repo, "https://sageox.ai" }
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
	// two 429s (initial + retry) in a follow, then the session finishes
	f := &fakeAgentView{throttle: 2, responses: []map[string]any{liveResp("finished", false, "", "Status: FINISHED at t. 1 turns.\nbody\n")}}
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

func TestLiveView_RateLimitWithoutFollowReportsAfterRetry(t *testing.T) {
	f := &fakeAgentView{throttle: 5, responses: []map[string]any{liveResp("live", true, "", "x")}}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()
	cmd, _, _ := newLiveViewCmd(t, srv)
	err := runLiveSessionView(cmd, liveViewRef{ID: testSesID})
	if err == nil || !strings.Contains(err.Error(), "rate limited") || len(f.queries) != 2 {
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
