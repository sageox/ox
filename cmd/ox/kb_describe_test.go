package main

// kb_describe_test.go covers the resolution and error-handling logic that
// runKBDescribe layers on top of the api.KBClient. Under ox ADR-028 §5,
// slugs are unique per scope (not globally), so slug resolution goes
// through GET /api/v1/kb/resolve within ONE scope — the old client-side
// kind-priority tie-break (pickKBByPriority / filterKBsBySlug /
// kbSlugTypePriority) is gone, and so are its tests. Unit tests target the
// pure helpers (resolveKBIdentifier, handleKBDescribeError, the render and
// JSON shapes) plus httptest-backed integration of the resolve → detail
// flow.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
)

// stageKBDescribeProject wires up a fake initialized ox project bound to
// teamID so ambientKBScopes(projectRoot) resolves a team scope. The team
// context is registered via config.local.toml (the same path the daemon
// writes), because ambientKBScopes goes through config.FindRepoTeamContext.
func stageKBDescribeProject(t *testing.T, teamID string) string {
	t.Helper()
	projectRoot := t.TempDir()
	teamRoot := t.TempDir()

	if err := config.SaveProjectConfig(projectRoot, &config.ProjectConfig{
		ProjectID:   "proj_kb_describe",
		WorkspaceID: "ws_kb_describe",
		TeamID:      teamID,
		TeamName:    "KB Describe Test",
	}); err != nil {
		t.Fatalf("SaveProjectConfig: %v", err)
	}
	if err := config.SaveLocalConfig(projectRoot, &config.LocalConfig{
		TeamContexts: []config.TeamContext{{
			TeamID:   teamID,
			TeamName: "KB Describe Test",
			Slug:     "kb-describe-test",
			Path:     teamRoot,
		}},
	}); err != nil {
		t.Fatalf("SaveLocalConfig: %v", err)
	}
	return projectRoot
}

// --- A. identifier resolution ---

// TestResolveKBIdentifier_KBIDPassthrough verifies that a kb_id-prefixed
// argument bypasses the resolve endpoint entirely — no HTTP call, no scope
// requirement. Important because direct kb_id lookup must work even
// outside a project and even when the resolve endpoint is gated.
//
// Failure prevented: an extra round-trip on every `ox kb describe kb_...`
// invocation, plus a confusing "no team scope" error for kb_id users
// outside an initialized repo.
func TestResolveKBIdentifier_KBIDPassthrough(t *testing.T) {
	t.Parallel()

	resolveCalled := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resolveCalled = true
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"kb_id":"kb_wrong"}`))
	}))
	defer srv.Close()

	client := api.NewKBClientWithEndpoint(srv.URL)

	// projectRoot empty: the kb_id path must not need an ambient scope.
	got, err := resolveKBIdentifier(context.Background(), client, "kb_01HXYZ", "team", "")
	if err != nil {
		t.Fatalf("resolveKBIdentifier returned error: %v", err)
	}
	if got != "kb_01HXYZ" {
		t.Errorf("got %q, want kb_01HXYZ", got)
	}
	if resolveCalled {
		t.Error("kb_id input should not trigger any API call")
	}
}

// TestResolveKBIdentifier_SlugDefaultScope_UsesResolveEndpoint walks the
// slug → kb_id resolution for the default (team) scope against a fake
// server: the request must hit GET /api/v1/kb/resolve carrying the
// project team's scope params plus the slug.
//
// Failure prevented: a wiring break between resolveKBIdentifier, the
// ambient-scope resolver, and KBClient.ResolveSlug (wrong endpoint path,
// missing scope params, resurrecting the deleted list-then-filter flow).
func TestResolveKBIdentifier_SlugDefaultScope_UsesResolveEndpoint(t *testing.T) {
	projectRoot := stageKBDescribeProject(t, "team_describe")

	var resolveCalls, listCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/kb/resolve":
			resolveCalls++
			if got := r.URL.Query().Get("scope_type"); got != "team" {
				t.Errorf("scope_type: got %q, want team", got)
			}
			if got := r.URL.Query().Get("scope_id"); got != "team_describe" {
				t.Errorf("scope_id: got %q, want team_describe", got)
			}
			if got := r.URL.Query().Get("slug"); got != "notes" {
				t.Errorf("slug: got %q, want notes", got)
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"kb_id":"kb_notes"}`))
		case "/api/v1/kb":
			listCalls++
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"kbs":[]}`))
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client := api.NewKBClientWithEndpoint(srv.URL)

	// both the explicit and the empty (defaulted) scope flag take the
	// team-resolve path.
	for _, scopeFlag := range []string{"team", ""} {
		got, err := resolveKBIdentifier(context.Background(), client, "notes", scopeFlag, projectRoot)
		if err != nil {
			t.Fatalf("scopeFlag=%q: resolveKBIdentifier: %v", scopeFlag, err)
		}
		if got != "kb_notes" {
			t.Errorf("scopeFlag=%q: got %q, want kb_notes", scopeFlag, got)
		}
	}
	if resolveCalls != 2 {
		t.Errorf("expected 2 resolve calls, got %d", resolveCalls)
	}
	if listCalls != 0 {
		t.Error("slug resolution must use /api/v1/kb/resolve, never the list endpoint")
	}
}

// TestResolveKBIdentifier_SlugThenDetail_FullFlow exercises the two-hop
// describe flow end-to-end against one server: resolve the slug within the
// team scope, then fetch the bubble via GET /api/v1/kb/{id} and decode the
// ADR-028 detail fields.
//
// Failure prevented: resolve returning an id the detail call can't
// round-trip (path templating, envelope decode) — the exact seam
// `ox kb describe <#slug>` lives on.
func TestResolveKBIdentifier_SlugThenDetail_FullFlow(t *testing.T) {
	projectRoot := stageKBDescribeProject(t, "team_describe")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/kb/resolve":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"kb_id":"kb_platform"}`))
		case "/api/v1/kb/kb_platform":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{
				"id":"kb_platform",
				"kb_type":"team",
				"slug":"platform",
				"name":"Platform",
				"scope_type":"team",
				"scope_id":"team_describe",
				"description":"Platform knowledge",
				"topics":["infra"],
				"viewer_role":"member",
				"last_activity_at":"2026-07-27T10:00:00Z"
			}`))
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client := api.NewKBClientWithEndpoint(srv.URL)

	kbID, err := resolveKBIdentifier(context.Background(), client, "platform", "team", projectRoot)
	if err != nil {
		t.Fatalf("resolveKBIdentifier: %v", err)
	}
	bubble, err := client.GetBubble(context.Background(), kbID)
	if err != nil {
		t.Fatalf("GetBubble: %v", err)
	}
	if bubble.KBID != "kb_platform" || bubble.ScopeType != "team" || bubble.Description != "Platform knowledge" {
		t.Errorf("detail fields did not survive the flow: %+v", bubble)
	}
	if len(bubble.Topics) != 1 || bubble.Topics[0] != "infra" {
		t.Errorf("topics: got %+v, want [infra]", bubble.Topics)
	}
}

// TestResolveKBIdentifier_SlugWithoutTeamScope_Errors verifies a slug
// outside any team-bound project fails with an actionable message (run
// inside an ox repo, or pass a kb_id) rather than a bare API error.
//
// Failure prevented: an opaque resolve failure when the ambient scope is
// simply absent.
func TestResolveKBIdentifier_SlugWithoutTeamScope_Errors(t *testing.T) {
	t.Parallel()

	client := api.NewKBClientWithEndpoint("http://unused.invalid")
	_, err := resolveKBIdentifier(context.Background(), client, "notes", "team", "")
	if err == nil {
		t.Fatal("expected error for slug resolution without a team scope")
	}
	if !strings.Contains(err.Error(), "no team scope available") {
		t.Errorf("expected 'no team scope available' guidance, got: %v", err)
	}
}

// TestResolveKBIdentifier_PersonalScope_Deferred pins the ADR-028 §4
// deferral: --scope personal is recognized but not serviceable until the
// ADR-086 personal-team backfill fix lands (bead ox-cag9.8). The error is
// the dedicated errKBScopeDeferred sentinel, not a generic failure.
//
// Failure prevented: silently resolving personal slugs against the team
// scope (wrong bubble) or emitting an unhelpful "invalid scope" error for
// a scope the CLI explicitly documents.
func TestResolveKBIdentifier_PersonalScope_Deferred(t *testing.T) {
	t.Parallel()

	client := api.NewKBClientWithEndpoint("http://unused.invalid")
	_, err := resolveKBIdentifier(context.Background(), client, "notes", "personal", "")
	if err == nil {
		t.Fatal("expected deferred error for --scope personal")
	}
	if err != errKBScopeDeferred {
		t.Errorf("expected errKBScopeDeferred, got %v", err)
	}
	if !strings.Contains(err.Error(), "not available yet") {
		t.Errorf("deferred error must carry the rollout copy, got: %v", err)
	}
}

// TestResolveKBIdentifier_InvalidScope_Errors verifies an unrecognized
// --scope value is rejected with the valid options named.
//
// Failure prevented: a typo like --scope tema silently defaulting to the
// team scope and resolving in a context the user didn't ask for.
func TestResolveKBIdentifier_InvalidScope_Errors(t *testing.T) {
	t.Parallel()

	client := api.NewKBClientWithEndpoint("http://unused.invalid")
	_, err := resolveKBIdentifier(context.Background(), client, "notes", "global", "")
	if err == nil {
		t.Fatal("expected error for invalid scope")
	}
	if !strings.Contains(err.Error(), `invalid --scope "global"`) {
		t.Errorf("expected invalid-scope copy naming the input, got: %v", err)
	}
	if !strings.Contains(err.Error(), "team or personal") {
		t.Errorf("expected the valid options in the error, got: %v", err)
	}
}

// --- B. error handling ---

// TestHandleKBDescribeError_Unavailable verifies the dedicated user-facing
// path for ErrKBAPIUnavailable: the updated copy ("no knowledge bubble
// matching ... in this scope") with the slug shown in `#slug` display form,
// returned as ErrSilent so main.go doesn't double-print "Error:".
//
// Failure prevented: a flag-disabled rollout (or a mistyped slug — the
// server deliberately returns the same 404 for both) printing a raw HTTP
// dump instead of the actionable copy + `ox kb list` hint.
func TestHandleKBDescribeError_Unavailable(t *testing.T) {
	t.Parallel()

	wrapped := fmt.Errorf("wrapped: %w", api.ErrKBAPIUnavailable)
	err := handleKBDescribeError(io.Discard, wrapped, "platform", false)
	if err == nil {
		t.Fatal("expected silent error, got nil")
	}
	if !cli.IsSilent(err) {
		t.Errorf("expected cli.IsSilent to recognize the error, got %#v", err)
	}
}

// TestHandleKBDescribeError_Unavailable_JSONCopy verifies the --json path:
// a machine-readable envelope with status=unavailable and the updated
// "no knowledge bubble matching ..." message, slug rendered with the
// display '#' prefix (kb_id inputs stay bare).
//
// Failure prevented: JSON consumers losing the status discriminator, or
// the copy regressing to the retired "Knowledge bubbles not enabled" text
// that no longer covers the slug-not-found-in-scope case.
func TestHandleKBDescribeError_Unavailable_JSONCopy(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		input       string
		wantDisplay string
	}{
		{"slug gets # prefix", "platform", `#platform`},
		{"kb_id stays bare", "kb_missing", `kb_missing`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			wrapped := fmt.Errorf("wrapped: %w", api.ErrKBAPIUnavailable)
			err := handleKBDescribeError(&buf, wrapped, tc.input, true)
			if err != nil {
				t.Fatalf("JSON path must not return an error (envelope is the answer), got %v", err)
			}

			var decoded map[string]any
			if uErr := json.Unmarshal(buf.Bytes(), &decoded); uErr != nil {
				t.Fatalf("unmarshal: %v\noutput: %s", uErr, buf.String())
			}
			if decoded["status"] != "unavailable" {
				t.Errorf("status: got %v, want unavailable", decoded["status"])
			}
			msg, _ := decoded["message"].(string)
			if !strings.Contains(msg, "no knowledge bubble matching") {
				t.Errorf("message must carry the updated copy, got %q", msg)
			}
			if !strings.Contains(msg, tc.wantDisplay) {
				t.Errorf("message must show the input as %q, got %q", tc.wantDisplay, msg)
			}
		})
	}
}

// TestHandleKBDescribeError_ScopeDeferred verifies --scope personal's
// deferred error path: human mode returns ErrSilent (message goes to
// stderr), JSON mode emits {"status":"deferred", "message":...}.
//
// Failure prevented: the deferral surfacing as a generic error, or the
// JSON path returning ErrSilent (which would make scripts treat a
// documented deferral as a hard failure).
func TestHandleKBDescribeError_ScopeDeferred(t *testing.T) {
	t.Parallel()

	t.Run("human mode is silent", func(t *testing.T) {
		err := handleKBDescribeError(io.Discard, errKBScopeDeferred, "notes", false)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !cli.IsSilent(err) {
			t.Errorf("expected cli.ErrSilent, got %#v", err)
		}
	})

	t.Run("json mode emits deferred envelope", func(t *testing.T) {
		var buf bytes.Buffer
		err := handleKBDescribeError(&buf, errKBScopeDeferred, "notes", true)
		if err != nil {
			t.Fatalf("JSON path must not return an error, got %v", err)
		}
		var decoded map[string]any
		if uErr := json.Unmarshal(buf.Bytes(), &decoded); uErr != nil {
			t.Fatalf("unmarshal: %v\noutput: %s", uErr, buf.String())
		}
		if decoded["status"] != "deferred" {
			t.Errorf("status: got %v, want deferred", decoded["status"])
		}
		if decoded["message"] != kbDescribeScopeDeferredMsg {
			t.Errorf("message: got %v, want the documented deferral copy", decoded["message"])
		}
	})
}

// TestHandleKBDescribeError_PassThroughOtherErrors verifies non-sentinel
// errors (real outages) pass through unwrapped so the user sees the
// underlying failure.
//
// Failure prevented: a 500 being swallowed into the "not found" copy,
// masking real backend incidents.
func TestHandleKBDescribeError_PassThroughOtherErrors(t *testing.T) {
	t.Parallel()

	raw := fmt.Errorf("HTTP 500 from https://api.example/api/v1/kb/kb_x")
	err := handleKBDescribeError(io.Discard, raw, "kb_x", false)
	if err != raw {
		t.Errorf("expected pass-through of the raw error, got %v", err)
	}
}

// --- C. output shapes ---

// TestKBDescribeOutput_JSONShape captures the JSON contract: the embedded
// api.KB fields stay at the top level (no nesting under a "kb" key),
// local_path is appended, and the ADR-028 fields (scope_type/scope_id,
// description, topics, git_path, last_activity_at) surface.
//
// Failure prevented: a future refactor inadvertently nesting the kb
// payload or dropping the new fields would break consumers who script
// against the flat JSON.
func TestKBDescribeOutput_JSONShape(t *testing.T) {
	t.Parallel()

	out := kbDescribeOutput{
		KB: &api.KB{
			KBID:           "kb_01",
			KBType:         api.KBTypeTeam,
			Slug:           "platform",
			Name:           "Platform",
			ScopeType:      "team",
			ScopeID:        "team_abc",
			Description:    "Platform knowledge",
			Topics:         []string{"infra", "deploys"},
			GitPath:        "kb/kb_01",
			LastActivityAt: "2026-07-27T10:00:00Z",
			LifecycleState: "active",
			ViewerRole:     "member",
		},
		LocalPath: "/tmp/kb/kb_01",
	}

	data, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	for _, key := range []string{
		"id", "kb_type", "slug", "name", "local_path",
		"scope_type", "scope_id", "description", "topics", "git_path", "last_activity_at",
	} {
		if _, ok := decoded[key]; !ok {
			t.Errorf("expected key %q in JSON output, got: %s", key, string(data))
		}
	}
	if got, _ := decoded["id"].(string); got != "kb_01" {
		t.Errorf("id: got %q, want kb_01", got)
	}
	if got, _ := decoded["local_path"].(string); got != "/tmp/kb/kb_01" {
		t.Errorf("local_path: got %q, want /tmp/kb/kb_01", got)
	}
	if got, _ := decoded["scope_type"].(string); got != "team" {
		t.Errorf("scope_type: got %q, want team", got)
	}
}

// TestRenderKBDescribe_HumanOutput exercises the human-readable path so a
// regression in the field-rendering loop (e.g., dropping the new scope /
// description / topics / admin / last_activity rows) is caught here rather
// than in a manual smoke test.
//
// Failure prevented: a renderer change that silently drops a field the
// user relies on without a compile error.
func TestRenderKBDescribe_HumanOutput(t *testing.T) {
	t.Parallel()

	bubble := &api.KB{
		KBID:           "kb_01HXYZ",
		KBType:         api.KBTypeTeam,
		Slug:           "platform",
		Name:           "Platform",
		ScopeType:      "team",
		ScopeID:        "team_abc",
		Description:    "Curated platform knowledge",
		Topics:         []string{"infra", "deploys"},
		LifecycleState: "active",
		ViewerRole:     "member",
		Manager:        "usr_admin",
		LastActivityAt: "2026-07-27T10:00:00Z",
		Steering:       "Focus on deploy tooling and incident retros",
	}

	var buf bytes.Buffer
	// endpoint deliberately unauthenticated in tests: ownerIsCaller
	// resolves false, so the admin row must be shown.
	renderKBDescribe(&buf, bubble, "/local/kb/kb_01HXYZ", "https://example.invalid")
	got := buf.String()

	for _, want := range []string{
		"kb_01HXYZ",
		"team team_abc", // scope row
		"#platform",     // slug in display form
		"Curated platform knowledge",
		"infra, deploys",
		"active",
		"member",
		"usr_admin", // admin row (caller is not the admin)
		// the curator steering prompt: how team conversations were
		// synthesized into this bubble. `ox agent prime` tells AI coworkers
		// to read it here, so a dropped row breaks that promise.
		"Focus on deploy tooling and incident retros",
		"2026-07-27T10:00:00Z",
		"/local/kb/kb_01HXYZ",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q\nfull output:\n%s", want, got)
		}
	}
}

// TestRenderKBDescribe_EmptyOptionalRowsOmitted verifies rows with no
// value (description, topics, last_activity, ...) are omitted entirely
// rather than rendered as blank key/value lines.
//
// Failure prevented: a sparse bubble (freshly provisioned, no metadata)
// rendering a wall of empty rows.
func TestRenderKBDescribe_EmptyOptionalRowsOmitted(t *testing.T) {
	t.Parallel()

	bubble := &api.KB{
		KBID:   "kb_sparse",
		KBType: api.KBTypeTeam,
		Slug:   "sparse",
	}

	var buf bytes.Buffer
	renderKBDescribe(&buf, bubble, "", "https://example.invalid")
	got := buf.String()

	for _, absent := range []string{"description:", "topics:", "steering:", "last_activity:", "local_path:", "admin:"} {
		if strings.Contains(got, absent) {
			t.Errorf("empty field %q must be omitted, got:\n%s", absent, got)
		}
	}
	if !strings.Contains(got, "kb_sparse") {
		t.Errorf("kb_id must always render:\n%s", got)
	}
}

// --- D. command wiring ---

// TestKBDescribeCmd_RegistrationOnParent verifies the describe subcommand
// is wired onto the kb parent with the back-compat "show" alias. A
// registration miss would surface in production as `ox kb describe ...`
// printing "unknown command" instead of running our handler.
//
// Failure prevented: the kb_show.go → kb_describe.go rename dropping the
// AddCommand call or the alias that keeps existing `ox kb show` muscle
// memory working.
func TestKBDescribeCmd_RegistrationOnParent(t *testing.T) {
	t.Parallel()

	var describeCmd *struct {
		hasShowAlias bool
	}
	for _, sub := range kbCmd.Commands() {
		if sub.Name() == "describe" {
			d := struct{ hasShowAlias bool }{}
			for _, alias := range sub.Aliases {
				if alias == "show" {
					d.hasShowAlias = true
				}
			}
			describeCmd = &d
			break
		}
	}
	if describeCmd == nil {
		t.Fatal("kb parent does not have 'describe' subcommand registered")
	}
	if !describeCmd.hasShowAlias {
		t.Error("describe must keep the 'show' alias for back-compat")
	}

	// also assert the parent alias contract
	wantAliases := map[string]bool{"bubble": false, "bubbles": false}
	for _, alias := range kbCmd.Aliases {
		wantAliases[alias] = true
	}
	for alias, present := range wantAliases {
		if !present {
			t.Errorf("kb parent missing documented alias %q", alias)
		}
	}
}

// TestRenderKBDescribe_StripsTerminalControlSequences verifies every
// server-provided field is sanitized before it reaches the terminal, not
// just the one that prompted the fix.
//
// A knowledge bubble is untrusted, multi-author, server-stored content —
// the premise of the whole KB feature. Without sanitizing, a hostile or
// compromised bubble could repaint the screen, forge additional key/value
// rows, retitle the window, or smuggle an OSC 52 clipboard write into a
// user who merely ran a read-only `ox kb describe`.
//
// Failure prevented: a new row added to renderKBDescribe that passes raw
// server text through, reopening the hole on a different field.
func TestRenderKBDescribe_StripsTerminalControlSequences(t *testing.T) {
	t.Parallel()

	const esc = "\x1b"
	bubble := &api.KB{
		KBID:           "kb_evil" + esc + "[31m",
		KBType:         api.KBTypeTeam,
		Slug:           "platform" + esc + "[2J",
		Name:           "Platform" + esc + "]0;pwned\x07",
		Description:    "desc" + esc + "]52;c;ZXZpbA==\x07",
		Topics:         []string{"infra" + esc + "[1m", "deploys"},
		LifecycleState: "active",
		ViewerRole:     "member",
		Manager:        "usr" + esc + "[0m",
		LastActivityAt: "2026-07-27T10:00:00Z",
		Steering:       "steer" + esc + "]8;;https://evil.example\x07link\x1b]8;;\x07",
	}

	var buf bytes.Buffer
	renderKBDescribe(&buf, bubble, "/local/kb/kb_evil", "https://example.invalid")
	got := buf.String()

	// The renderer applies its own lipgloss styling to KEYS, so the output
	// legitimately contains escapes. Assert on the VALUES instead: none of
	// the injected payloads may survive anywhere in the output.
	for _, payload := range []string{"]0;pwned", "]52;c;", "]8;;https://evil.example", "[2J"} {
		if strings.Contains(got, payload) {
			t.Errorf("control payload %q survived rendering:\n%q", payload, got)
		}
	}

	// ...and the printable text around them must still render.
	for _, want := range []string{"kb_evil", "platform", "Platform", "desc", "infra", "steer", "link"} {
		if !strings.Contains(got, want) {
			t.Errorf("printable text %q was lost during sanitization:\n%q", want, got)
		}
	}
}

// TestRenderKBDescribe_MultilineValueStaysInsideItsRow verifies a
// multi-line untrusted value renders as indented continuation lines under
// its own row rather than escaping to column zero.
//
// Steering prompts are multi-line in practice. A continuation at column
// zero visually reads as a new key/value row or a standalone instruction —
// which is how untrusted bubble text forges output it was never given.
//
// Failure prevented: reverting the row helper to a single Fprintf, where
// only the first line gets the label and indent.
func TestRenderKBDescribe_MultilineValueStaysInsideItsRow(t *testing.T) {
	t.Parallel()

	bubble := &api.KB{
		KBID:     "kb_multi",
		KBType:   api.KBTypeTeam,
		Slug:     "platform",
		Steering: "first line\nsecond line\n\nlocal_path:      /etc/passwd",
	}

	var buf bytes.Buffer
	renderKBDescribe(&buf, bubble, "/real/path", "https://example.invalid")

	for _, line := range strings.Split(buf.String(), "\n") {
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "  ") {
			t.Errorf("every rendered line must be indented inside its row, got %q\nfull:\n%s", line, buf.String())
		}
	}

	got := buf.String()
	// all three substantive lines survive...
	for _, want := range []string{"first line", "second line", "/etc/passwd"} {
		if !strings.Contains(got, want) {
			t.Errorf("multiline content %q was lost:\n%s", want, got)
		}
	}
	// ...but the forged text never occupies the KEY column. Real rows put
	// their key at columns 2-18; a continuation is pushed past that into
	// the value column, so it cannot be read as a key/value row of its own.
	for _, line := range strings.Split(got, "\n") {
		if !strings.Contains(line, "/etc/passwd") {
			continue
		}
		keyCol := line[:min(len(line), 2+kbRowKeyWidth)]
		if strings.TrimSpace(keyCol) != "" {
			t.Errorf("forged content reached the key column: %q", line)
		}
	}
	// the real local_path row is still present and correct.
	if !strings.Contains(got, "/real/path") {
		t.Errorf("genuine local_path row missing:\n%s", got)
	}
}
