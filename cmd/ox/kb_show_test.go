package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/cli"
)

// kb_show_test.go covers the resolution and error-handling logic that
// runKBShow layers on top of the api.KBClient. Unit tests target the
// pure helpers (filterKBsBySlug, pickKBByPriority, handleKBShowError)
// plus a small integration test that exercises resolveKBIdentifier
// against an httptest server. Full cobra-level wiring is exercised by
// the kb parent command's existing args plumbing.

// fixtureBubbles is reused across resolution tests so each test reads
// top-to-bottom without fixture indirection. The set deliberately
// includes a slug ("notes") shared by personal+team variants so the
// priority-tiebreak path is covered.
func fixtureBubbles() []api.KB {
	return []api.KB{
		{KBID: "kb_personal", KBType: api.KBTypePersonal, Slug: "notes", Name: "My Notes"},
		{KBID: "kb_team", KBType: api.KBTypeTeam, Slug: "notes", Name: "Team Notes"},
		{KBID: "kb_platform", KBType: api.KBTypeTeam, Slug: "platform", Name: "Platform"},
		{KBID: "kb_repo", KBType: api.KBTypeRepo, Slug: "my-app", Name: "my-app"},
		{KBID: "kb_custom_only", KBType: api.KBTypeCustom, Slug: "exp", Name: "Experiment"},
	}
}

// TestFilterKBsBySlug_ExactMatch verifies the slug matcher returns only
// rows whose slug equals the input. Match is case-insensitive (matches
// `ox kb path` / `ox kb hydrate`) but never a prefix match.
//
// Failure prevented: a fuzzy match would silently surface the wrong
// bubble when a shorter slug is a prefix of a longer one (e.g., "team"
// matching "team-platform"). Case-insensitivity is intentional so
// `ox kb show MyTeam` resolves the same bubble `ox kb path MyTeam`
// already does — server-side normalization keeps b.Slug lowercase, but
// user input may be mixed case.
func TestFilterKBsBySlug_ExactMatch(t *testing.T) {
	t.Parallel()

	got := filterKBsBySlug(fixtureBubbles(), "notes")
	if len(got) != 2 {
		t.Fatalf("expected 2 matches for 'notes', got %d (%+v)", len(got), got)
	}

	// Case-insensitive: "Notes" must resolve the same set as "notes".
	if mixed := filterKBsBySlug(fixtureBubbles(), "Notes"); len(mixed) != 2 {
		t.Errorf("expected 2 case-insensitive matches for 'Notes', got %d (%+v)", len(mixed), mixed)
	}
	if filtered := filterKBsBySlug(fixtureBubbles(), "note"); len(filtered) != 0 {
		t.Errorf("prefix match leaked through: %+v", filtered)
	}
	if filtered := filterKBsBySlug(fixtureBubbles(), "platform"); len(filtered) != 1 {
		t.Errorf("platform should match exactly once, got %d", len(filtered))
	}
	// Empty / whitespace-only input must not match anything.
	if filtered := filterKBsBySlug(fixtureBubbles(), "  "); len(filtered) != 0 {
		t.Errorf("whitespace input leaked through: %+v", filtered)
	}
}

// TestPickKBByPriority_PersonalWins verifies the documented tie-break:
// when a slug matches both personal and team bubbles, personal wins.
//
// Failure prevented: a shuffle in fixture order would silently change
// which bubble `ox kb show notes` resolves to.
func TestPickKBByPriority_PersonalWins(t *testing.T) {
	t.Parallel()

	matches := filterKBsBySlug(fixtureBubbles(), "notes")
	chosen := pickKBByPriority(matches)
	if chosen == nil {
		t.Fatal("expected a winner, got nil")
	}
	if chosen.KBType != api.KBTypePersonal {
		t.Errorf("expected personal to win, got %q (%s)", chosen.KBType, chosen.KBID)
	}
}

// TestPickKBByPriority_FullOrder steps through every priority tier so a
// future reorder of kbSlugTypePriority is caught immediately rather
// than discovered through a confused user.
//
// Failure prevented: silent regression of the documented type ordering
// (personal > profile > team > repo > custom).
func TestPickKBByPriority_FullOrder(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   []api.KB
		wantKey string
	}{
		{
			name: "personal > profile",
			input: []api.KB{
				{KBID: "kb_p", KBType: api.KBTypeProfile, Slug: "x"},
				{KBID: "kb_per", KBType: api.KBTypePersonal, Slug: "x"},
			},
			wantKey: "kb_per",
		},
		{
			name: "profile > team",
			input: []api.KB{
				{KBID: "kb_t", KBType: api.KBTypeTeam, Slug: "x"},
				{KBID: "kb_pr", KBType: api.KBTypeProfile, Slug: "x"},
			},
			wantKey: "kb_pr",
		},
		{
			name: "team > repo",
			input: []api.KB{
				{KBID: "kb_r", KBType: api.KBTypeRepo, Slug: "x"},
				{KBID: "kb_t", KBType: api.KBTypeTeam, Slug: "x"},
			},
			wantKey: "kb_t",
		},
		{
			name: "repo > custom",
			input: []api.KB{
				{KBID: "kb_c", KBType: api.KBTypeCustom, Slug: "x"},
				{KBID: "kb_r", KBType: api.KBTypeRepo, Slug: "x"},
			},
			wantKey: "kb_r",
		},
		{
			name: "all unknown -> first",
			input: []api.KB{
				{KBID: "kb_u1", KBType: api.KBTypeUnknown, Slug: "x"},
				{KBID: "kb_u2", KBType: api.KBTypeUnknown, Slug: "x"},
			},
			wantKey: "kb_u1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := pickKBByPriority(tt.input)
			if got == nil {
				t.Fatalf("pickKBByPriority(%s) returned nil", tt.name)
			}
			if got.KBID != tt.wantKey {
				t.Errorf("got %q, want %q", got.KBID, tt.wantKey)
			}
		})
	}
}

// TestPickKBByPriority_Empty proves the helper returns nil instead of
// panicking on empty input — important because resolveKBIdentifier
// calls it after a list call that may legitimately return zero rows.
//
// Failure prevented: an out-of-bounds panic when the user's input
// matches no bubbles in the merged list.
func TestPickKBByPriority_Empty(t *testing.T) {
	t.Parallel()
	if got := pickKBByPriority(nil); got != nil {
		t.Errorf("expected nil for empty input, got %+v", got)
	}
}

// TestResolveKBIdentifier_KBIDPassthrough verifies that a kb_id-prefixed
// argument bypasses the list call entirely. Important because the list
// call is gated by the knowledge-bubbles flag — direct kb_id lookup
// should still work even when ListBubbles would 403.
//
// Failure prevented: an extra round-trip on every `ox kb show kb_...`
// invocation, plus a confusing "feature unavailable" error when only
// the LIST endpoint is gated.
func TestResolveKBIdentifier_KBIDPassthrough(t *testing.T) {
	t.Parallel()

	listCalled := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/api/v1/kb") {
			listCalled = true
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("[]"))
	}))
	defer srv.Close()

	client := api.NewKBClientWithEndpoint(srv.URL)

	got, err := resolveKBIdentifier(context.Background(), client, "kb_01HXYZ")
	if err != nil {
		t.Fatalf("resolveKBIdentifier returned error: %v", err)
	}
	if got != "kb_01HXYZ" {
		t.Errorf("got %q, want kb_01HXYZ", got)
	}
	if listCalled {
		t.Error("kb_id input should not trigger a list call")
	}
}

// TestResolveKBIdentifier_SlugLookup walks the slug → kb_id resolution
// against a fake server. The server returns two bubbles sharing a slug
// so the priority logic is exercised via the actual API surface.
//
// Failure prevented: a wiring break between resolveKBIdentifier and the
// KBClient.ListBubbles call (e.g., wrong endpoint path, missing auth
// header, malformed JSON envelope).
func TestResolveKBIdentifier_SlugLookup(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/kb" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"bubbles": [
				{"kb_id": "kb_team_notes",     "kb_type": "team",     "slug": "notes"},
				{"kb_id": "kb_personal_notes", "kb_type": "personal", "slug": "notes"}
			]
		}`))
	}))
	defer srv.Close()

	client := api.NewKBClientWithEndpoint(srv.URL)
	got, err := resolveKBIdentifier(context.Background(), client, "notes")
	if err != nil {
		t.Fatalf("resolveKBIdentifier: %v", err)
	}
	if got != "kb_personal_notes" {
		t.Errorf("got %q, want kb_personal_notes (personal should win)", got)
	}
}

// TestResolveKBIdentifier_SlugNoMatch verifies a clean "not found"
// error when the slug doesn't appear in the listing.
//
// Failure prevented: an opaque empty-pointer error or a misleading
// success ("" returned silently) when the user mistypes a slug.
func TestResolveKBIdentifier_SlugNoMatch(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"bubbles": []}`))
	}))
	defer srv.Close()

	client := api.NewKBClientWithEndpoint(srv.URL)
	_, err := resolveKBIdentifier(context.Background(), client, "missing")
	if err == nil {
		t.Fatal("expected error for missing slug, got nil")
	}
	if !strings.Contains(err.Error(), "no knowledge bubble found") {
		t.Errorf("error should mention 'no knowledge bubble found', got: %v", err)
	}
	if !strings.Contains(err.Error(), "ox kb list") {
		t.Errorf("error should hint at 'ox kb list', got: %v", err)
	}
}

// TestHandleKBShowError_Unavailable verifies the dedicated user-facing
// path for ErrKBAPIUnavailable. Returns ErrSilent because the
// human-readable headline + hint are emitted directly to stderr/stdout
// — main.go must not double-print "Error: ..." on top of that.
//
// Failure prevented: a flag-disabled rollout printing a generic 403
// stack trace, leaving users to guess that knowledge bubbles aren't
// enabled for their account yet.
func TestHandleKBShowError_Unavailable(t *testing.T) {
	t.Parallel()

	wrapped := fmt.Errorf("wrapped: %w", api.ErrKBAPIUnavailable)
	err := handleKBShowError(io.Discard, wrapped, "personal", false)
	if err == nil {
		t.Fatal("expected silent error, got nil")
	}
	// cli.ErrSilent's contract: Error() returns empty string and IsSilent
	// reports true. main.go uses cli.IsSilent to skip the "Error: ..."
	// prefix, so callers really need this exact value back.
	if !cli.IsSilent(err) {
		t.Errorf("expected cli.IsSilent to recognize the error, got %#v", err)
	}
}

// TestHandleKBShowError_NotFound verifies that a 404 from GetBubble is
// translated into a friendly "no knowledge bubble found matching ..."
// message rather than leaking the raw HTTP error.
//
// Failure prevented: typo'ing a kb_id and getting a confusing
// "HTTP 404 from https://api..." dump instead of an actionable hint.
func TestHandleKBShowError_NotFound(t *testing.T) {
	t.Parallel()

	raw := errors.New("HTTP 404 from https://api.example/api/v1/kb/kb_missing")
	err := handleKBShowError(io.Discard, raw, "kb_missing", false)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "no knowledge bubble found matching \"kb_missing\"") {
		t.Errorf("expected friendly not-found message, got: %v", err)
	}
}

// TestKBShowOutput_JSONShape captures the JSON contract: kb_id, slug,
// and local_path are all present and the embedded api.KB fields stay
// at the top level (no nesting under a "kb" key).
//
// Failure prevented: a future refactor inadvertently nesting the kb
// payload would break consumers who script against the flat JSON.
func TestKBShowOutput_JSONShape(t *testing.T) {
	t.Parallel()

	out := kbShowOutput{
		KB: &api.KB{
			KBID:           "kb_01",
			KBType:         api.KBTypePersonal,
			Slug:           "personal-abc",
			Name:           "Personal",
			LifecycleState: "active",
			ViewerRole:     "owner",
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

	for _, key := range []string{"kb_id", "kb_type", "slug", "name", "local_path"} {
		if _, ok := decoded[key]; !ok {
			t.Errorf("expected key %q in JSON output, got: %s", key, string(data))
		}
	}
	if got, _ := decoded["kb_id"].(string); got != "kb_01" {
		t.Errorf("kb_id: got %q, want kb_01", got)
	}
	if got, _ := decoded["local_path"].(string); got != "/tmp/kb/kb_01" {
		t.Errorf("local_path: got %q, want /tmp/kb/kb_01", got)
	}
}

// TestRenderKBShow_HumanOutput exercises the human-readable path so a
// regression in the field-rendering loop (e.g., dropping local_path)
// is caught here rather than in a manual smoke test.
//
// Failure prevented: a renderer change that silently drops a field
// the user relies on (kb_id, slug, local_path) without a compile error.
func TestRenderKBShow_HumanOutput(t *testing.T) {
	t.Parallel()

	bubble := &api.KB{
		KBID:           "kb_01HXYZ",
		KBType:         api.KBTypePersonal,
		Slug:           "personal-abc",
		Name:           "Personal Notes",
		LifecycleState: "active",
		ViewerRole:     "owner",
	}

	var buf bytes.Buffer
	renderKBShow(&buf, bubble, "/local/kb/kb_01HXYZ", "https://example.invalid")
	got := buf.String()

	for _, want := range []string{"kb_01HXYZ", "personal", "personal-abc", "active", "owner", "/local/kb/kb_01HXYZ"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q\nfull output:\n%s", want, got)
		}
	}
}

// TestKBShowCmd_RegistrationOnParent verifies the show subcommand is
// wired onto the kb parent. A registration miss would surface in
// production as `ox kb show ...` printing "unknown command" instead
// of running our handler — easy to miss without this test.
//
// Failure prevented: a refactor splitting kb_show.go into multiple
// files and dropping the AddCommand call.
func TestKBShowCmd_RegistrationOnParent(t *testing.T) {
	t.Parallel()

	var found bool
	for _, sub := range kbCmd.Commands() {
		if sub.Name() == "show" {
			found = true
			break
		}
	}
	if !found {
		t.Error("kb parent does not have 'show' subcommand registered")
	}

	// also assert the alias contract documented in the plan
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
