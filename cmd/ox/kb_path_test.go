package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/api"
	"github.com/spf13/cobra"
)

// stubKBResolver is a hand-rolled fake implementing kbResolver. We don't
// auto-generate mocks because the seam is two methods wide and the tests need
// to inject either an error or a fixed list — anything more clever would
// obscure intent.
type stubKBResolver struct {
	bubbles []api.KB
	listErr error
}

func (s *stubKBResolver) ListBubbles(_ context.Context) ([]api.KB, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	return s.bubbles, nil
}

func (s *stubKBResolver) GetBubble(_ context.Context, kbID string) (*api.KB, error) {
	for i := range s.bubbles {
		if s.bubbles[i].KBID == kbID || s.bubbles[i].Slug == kbID {
			return &s.bubbles[i], nil
		}
	}
	return nil, fmt.Errorf("not found: %s", kbID)
}

// newKBPathTestCmd returns a fresh cobra command configured with the buffers
// the test will inspect. Building it from scratch (rather than reusing the
// global kbPathCmd) keeps tests independent of init() state and other tests'
// flag mutations.
func newKBPathTestCmd(t *testing.T) (*cobra.Command, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	cmd := &cobra.Command{Use: "path"}
	cmd.Flags().Bool("json", false, "")
	cmd.SetContext(context.Background())
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	return cmd, &stdout, &stderr
}

// fakePathFor builds a deterministic path from a kb_id without depending on
// the real paths.KBDir which requires an endpoint to be configured. The
// real production wiring uses paths.KBDir directly.
func fakePathFor(kbID string) string {
	return "/test/data/sageox.ai/kb/" + kbID
}

// TestKBPath_ResolveByKBID verifies that a kb_*-prefixed argument bypasses
// the API entirely and emits paths.KBDir(kb_id) directly.
//
// Failure prevented: a regression that requires the API for kb_id resolution
// would break `cd $(ox kb path kb_xyz)` in offline / flag-off setups.
func TestKBPath_ResolveByKBID(t *testing.T) {
	cmd, stdout, stderr := newKBPathTestCmd(t)

	deps := kbPathDeps{
		// resolver is non-nil to prove kb_id never calls it (a panic-on-call
		// stub would also work; keeping a benign one keeps the test simple).
		resolver: &stubKBResolver{},
		legacy:   func(string) (string, string, bool) { return "", "", false },
		pathFor:  fakePathFor,
	}

	if err := runKBPathWithDeps(cmd, "kb_xyz123", false, deps); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := fakePathFor("kb_xyz123") + "\n"
	if got := stdout.String(); got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr should be empty on success, got %q", stderr.String())
	}
}

// TestKBPath_ResolveBySlug verifies that when the user passes a slug, the
// resolver looks it up via ListBubbles and prints the canonical path of the
// matched bubble.
//
// Failure prevented: silently returning the wrong bubble's path when slugs
// collide across types — would `cd` users into the wrong checkout.
func TestKBPath_ResolveBySlug(t *testing.T) {
	cmd, stdout, _ := newKBPathTestCmd(t)

	deps := kbPathDeps{
		resolver: &stubKBResolver{
			bubbles: []api.KB{
				{KBID: "kb_team", Slug: "platform", KBType: api.KBTypeTeam},
				{KBID: "kb_personal", Slug: "personal-foo", KBType: api.KBTypePersonal},
			},
		},
		legacy:  func(string) (string, string, bool) { return "", "", false },
		pathFor: fakePathFor,
	}

	if err := runKBPathWithDeps(cmd, "personal-foo", false, deps); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := fakePathFor("kb_personal") + "\n"
	if got := stdout.String(); got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
}

// TestKBPath_SlugPriority verifies the personal > profile > team > repo >
// custom tiebreak when multiple bubbles share the same slug.
//
// Failure prevented: a future refactor that loses the priority sort and
// returns whichever bubble the API listed first — depending on server-side
// ordering for correctness is a recipe for nondeterministic CLI behavior.
func TestKBPath_SlugPriority(t *testing.T) {
	cmd, stdout, _ := newKBPathTestCmd(t)

	deps := kbPathDeps{
		resolver: &stubKBResolver{
			// bubbles are listed in *non*-priority order to prove the picker
			// sorts; if the picker accidentally returned the first match, the
			// test would fail.
			bubbles: []api.KB{
				{KBID: "kb_team", Slug: "shared", KBType: api.KBTypeTeam},
				{KBID: "kb_personal", Slug: "shared", KBType: api.KBTypePersonal},
				{KBID: "kb_profile", Slug: "shared", KBType: api.KBTypeProfile},
			},
		},
		legacy:  func(string) (string, string, bool) { return "", "", false },
		pathFor: fakePathFor,
	}

	if err := runKBPathWithDeps(cmd, "shared", false, deps); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := fakePathFor("kb_personal") + "\n"
	if got := stdout.String(); got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
}

// TestKBPath_StdoutIsClean verifies that on success, stdout contains exactly
// one line (the path), no styling/ANSI bytes, and exactly one trailing
// newline. This is the load-bearing contract for `cd $(ox kb path …)`.
//
// Failure prevented: someone adds a "Found bubble:" prelude or a styled
// confirmation line, breaking shell composition for every downstream user.
func TestKBPath_StdoutIsClean(t *testing.T) {
	cmd, stdout, _ := newKBPathTestCmd(t)

	deps := kbPathDeps{
		resolver: &stubKBResolver{
			bubbles: []api.KB{{KBID: "kb_clean", Slug: "clean", KBType: api.KBTypePersonal}},
		},
		legacy:  func(string) (string, string, bool) { return "", "", false },
		pathFor: fakePathFor,
	}

	if err := runKBPathWithDeps(cmd, "clean", false, deps); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := stdout.String()
	if strings.Count(got, "\n") != 1 {
		t.Errorf("stdout should have exactly one newline, got %q", got)
	}
	if !strings.HasSuffix(got, "\n") {
		t.Errorf("stdout should end with newline, got %q", got)
	}
	// reject ANSI escape (lipgloss / styled output) — the success path must
	// never decorate.
	if strings.ContainsAny(got, "\x1b\r") {
		t.Errorf("stdout contains styling/CR bytes: %q", got)
	}
	// reject any whitespace before the path
	trimmed := strings.TrimRight(got, "\n")
	if trimmed != strings.TrimSpace(trimmed) {
		t.Errorf("stdout has leading/trailing whitespace: %q", got)
	}
}

// TestKBPath_JSONOutput verifies the --json mode emits a compact object with
// the documented kb_id and path fields.
//
// Failure prevented: a JSON shape change that breaks scripts piping
// `ox kb path --json | jq`. Field-name regressions are silent.
func TestKBPath_JSONOutput(t *testing.T) {
	cmd, stdout, _ := newKBPathTestCmd(t)

	deps := kbPathDeps{
		resolver: &stubKBResolver{
			bubbles: []api.KB{{KBID: "kb_json", Slug: "json-bubble", KBType: api.KBTypePersonal}},
		},
		legacy:  func(string) (string, string, bool) { return "", "", false },
		pathFor: fakePathFor,
	}

	if err := runKBPathWithDeps(cmd, "json-bubble", true, deps); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var got struct {
		KBID string `json:"kb_id"`
		Path string `json:"path"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nraw: %s", err, stdout.String())
	}
	if got.KBID != "kb_json" {
		t.Errorf("kb_id = %q, want %q", got.KBID, "kb_json")
	}
	if got.Path != fakePathFor("kb_json") {
		t.Errorf("path = %q, want %q", got.Path, fakePathFor("kb_json"))
	}
}

// TestKBPath_NotFound verifies the user-facing error format and non-zero
// exit when the slug doesn't match any bubble in any source.
//
// Failure prevented: an unhelpful message ("error: not found in API") when
// the user mistypes a slug — the scripted contract is `kb not found: <slug>`.
func TestKBPath_NotFound(t *testing.T) {
	cmd, stdout, stderr := newKBPathTestCmd(t)

	deps := kbPathDeps{
		resolver: &stubKBResolver{bubbles: []api.KB{}},
		legacy:   func(string) (string, string, bool) { return "", "", false },
		pathFor:  fakePathFor,
	}

	err := runKBPathWithDeps(cmd, "missing-slug", false, deps)
	if err == nil {
		t.Fatal("expected non-nil error for missing slug, got nil")
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout must stay empty on failure (no half-printed paths), got %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "kb not found: missing-slug") {
		t.Errorf("stderr missing canonical not-found message; got %q", stderr.String())
	}
}

// TestKBPath_LegacyFallbackOnAPIUnavailable verifies that when the kb API
// returns ErrKBAPIUnavailable (flag off / 403), a slug-matched legacy
// team-context entry is still resolvable.
//
// Failure prevented: pre-flag users (most users today) would see "kb not
// found" for every slug they've used for months under the legacy team-
// context model — a hard regression in basic ergonomics.
func TestKBPath_LegacyFallbackOnAPIUnavailable(t *testing.T) {
	cmd, stdout, _ := newKBPathTestCmd(t)

	legacyCalls := 0
	deps := kbPathDeps{
		resolver: &stubKBResolver{
			listErr: fmt.Errorf("flag off: %w", api.ErrKBAPIUnavailable),
		},
		legacy: func(slug string) (string, string, bool) {
			legacyCalls++
			if slug == "legacy-team" {
				return "team_legacy_id", "/legacy/path/team_legacy_id", true
			}
			return "", "", false
		},
		pathFor: fakePathFor,
	}

	if err := runKBPathWithDeps(cmd, "legacy-team", false, deps); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if legacyCalls == 0 {
		t.Error("legacy fallback was never called when API returned ErrKBAPIUnavailable")
	}

	want := fakePathFor("team_legacy_id") + "\n"
	if got := stdout.String(); got != want {
		t.Errorf("stdout = %q, want %q (legacy id should flow through pathFor)", got, want)
	}
}

// TestKBPath_LegacyFallbackMissesAlsoMissesOverall verifies that an
// API-unavailable error combined with a legacy miss still produces the
// canonical not-found message — not a leaked api error.
//
// Failure prevented: a refactor that returns the raw ErrKBAPIUnavailable to
// the user, exposing internal sentinel naming and breaking the "not found"
// contract scripts depend on.
func TestKBPath_LegacyFallbackMissesAlsoMissesOverall(t *testing.T) {
	cmd, stdout, stderr := newKBPathTestCmd(t)

	deps := kbPathDeps{
		resolver: &stubKBResolver{
			listErr: fmt.Errorf("403: %w", api.ErrKBAPIUnavailable),
		},
		legacy:  func(string) (string, string, bool) { return "", "", false },
		pathFor: fakePathFor,
	}

	err := runKBPathWithDeps(cmd, "totally-missing", false, deps)
	if err == nil {
		t.Fatal("expected non-nil error, got nil")
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout must stay empty on failure, got %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "kb not found: totally-missing") {
		t.Errorf("stderr missing canonical not-found message; got %q", stderr.String())
	}
	// the internal sentinel must not leak.
	if strings.Contains(stderr.String(), "kb API unavailable") {
		t.Errorf("stderr leaked internal ErrKBAPIUnavailable string: %q", stderr.String())
	}
}

// TestKBPath_NonAvailabilityErrorPropagates verifies that errors *other*
// than ErrKBAPIUnavailable (e.g., timeout, 500) are surfaced rather than
// silently masked as not-found, but still go to stderr with a non-zero exit.
//
// Failure prevented: a network blip that maps "transient 500" to
// "kb not found: foo" would mislead users into thinking they typo'd the slug.
func TestKBPath_NonAvailabilityErrorPropagates(t *testing.T) {
	cmd, stdout, stderr := newKBPathTestCmd(t)

	apiErr := errors.New("HTTP 503 from /api/v1/kb")
	deps := kbPathDeps{
		resolver: &stubKBResolver{listErr: apiErr},
		legacy:   func(string) (string, string, bool) { return "", "", false },
		pathFor:  fakePathFor,
	}

	err := runKBPathWithDeps(cmd, "anything", false, deps)
	if err == nil {
		t.Fatal("expected non-nil error, got nil")
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout must stay empty on failure, got %q", stdout.String())
	}
	if strings.Contains(stderr.String(), "kb not found:") {
		t.Errorf("non-availability errors should NOT masquerade as not-found; got %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "503") {
		t.Errorf("stderr should surface the underlying error; got %q", stderr.String())
	}
}

// TestKBPath_PickBubbleBySlug_NoMatch directly exercises the picker's empty
// case to lock in nil-on-miss behavior, which the resolver depends on.
//
// Failure prevented: returning a zero-valued *api.KB instead of nil would
// trick the resolver into emitting paths.KBDir("") to stdout.
func TestKBPath_PickBubbleBySlug_NoMatch(t *testing.T) {
	bubbles := []api.KB{{KBID: "kb_a", Slug: "a", KBType: api.KBTypePersonal}}
	if got := pickBubbleBySlug(bubbles, "b"); got != nil {
		t.Errorf("picker returned %+v for non-matching slug, want nil", got)
	}
}
