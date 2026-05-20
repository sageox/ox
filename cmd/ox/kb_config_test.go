package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/paths"
	"github.com/stretchr/testify/require"
)

// withKBConfigResolver swaps the package-level resolver used by `ox kb config`
// for the duration of a test and restores it on Cleanup. Tests use this to
// avoid hitting the real kb-API (which would require an authenticated
// endpoint).
func withKBConfigResolver(t *testing.T, fn func(ctx context.Context, raw string) (string, error)) {
	t.Helper()
	prev := kbConfigResolver
	kbConfigResolver = fn
	t.Cleanup(func() { kbConfigResolver = prev })
}

// kb_config_test.go exercises the cobra command surface enough to catch
// argument-validation and JSON-shape regressions. The deep precedence + safety
// inversion logic is covered by internal/kb/effective_test.go; these tests
// focus on the CLI plumbing (unknown key, missing --kb outside a binding, etc.)
// without requiring a real KB checkout or network call.

func TestKBConfigGet_UnknownKey(t *testing.T) {
	cmd := newRootForTest(t)
	out, errOut, err := executeCmd(cmd, "kb", "config", "get", "--kb=kb_test", "features.nope")
	_ = out
	if err == nil {
		t.Fatal("expected error for unknown key")
	}
	if !strings.Contains(errOut.String(), "unknown key") {
		t.Errorf("stderr=%q; want 'unknown key'", errOut.String())
	}
}

// TestKBConfigGet_BareSlugResolves verifies that `--kb=marketing` (no prefix)
// is routed through the shared resolver and accepted.
//
// Failure prevented: regression to the v1 "kb_id only" behavior that forced
// users to look up an opaque kb_id before they could read their own config.
func TestKBConfigGet_BareSlugResolves(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv(config.EnvUserConfig, filepath.Join(tmp, "config.yaml"))
	t.Setenv(config.EnvSessionRecording, "")

	calls := 0
	withKBConfigResolver(t, func(_ context.Context, raw string) (string, error) {
		calls++
		if raw != "marketing" {
			t.Errorf("resolver got raw=%q; want %q", raw, "marketing")
		}
		return "kb_marketing_resolved", nil
	})

	cmd := newRootForTest(t)
	stdout, stderr, err := executeCmd(cmd, "kb", "config", "get", "--kb=marketing", "features.visibility")
	if err != nil {
		t.Fatalf("unexpected error: %v; stderr=%q", err, stderr.String())
	}
	if calls != 1 {
		t.Errorf("resolver called %d times; want 1", calls)
	}
	// Built-in default for visibility is "private" — proves we resolved a kb
	// and ran the rest of the pipeline.
	if got := strings.TrimSpace(stdout.String()); got != "private" {
		t.Errorf("stdout=%q; want 'private'", got)
	}
}

// TestKBConfigGet_HashPrefixedSlugResolves verifies that `--kb=#marketing`
// is normalized (the `#` is stripped before resolution) and accepted.
//
// Failure prevented: copy-pasting a slug from `ox kb list` (which renders
// slugs as `#name`) would otherwise error out — a confusing DX moment.
func TestKBConfigGet_HashPrefixedSlugResolves(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv(config.EnvUserConfig, filepath.Join(tmp, "config.yaml"))
	t.Setenv(config.EnvSessionRecording, "")

	gotRaw := ""
	withKBConfigResolver(t, func(_ context.Context, raw string) (string, error) {
		gotRaw = raw
		return "kb_hash_resolved", nil
	})

	cmd := newRootForTest(t)
	_, stderr, err := executeCmd(cmd, "kb", "config", "get", "--kb=#marketing", "features.visibility")
	if err != nil {
		t.Fatalf("unexpected error: %v; stderr=%q", err, stderr.String())
	}
	// The resolver itself is called with the original form; NormalizeSlugArg
	// happens inside resolveKBInput.
	if gotRaw != "#marketing" {
		t.Errorf("resolver raw arg=%q; want %q", gotRaw, "#marketing")
	}
}

// TestKBConfigGet_KBIDBypassesResolver verifies that a kb_*-prefixed argument
// short-circuits the resolver entirely.
//
// Failure prevented: a regression that routes kb_id through the kb-API would
// break offline / flag-off users for whom kb_id is the documented escape
// hatch.
func TestKBConfigGet_KBIDBypassesResolver(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv(config.EnvUserConfig, filepath.Join(tmp, "config.yaml"))
	t.Setenv(config.EnvSessionRecording, "")

	withKBConfigResolver(t, func(_ context.Context, raw string) (string, error) {
		t.Fatalf("resolver must not be called for kb_id input; got %q", raw)
		return "", nil
	})

	cmd := newRootForTest(t)
	_, stderr, err := executeCmd(cmd, "kb", "config", "get", "--kb=kb_direct", "features.visibility")
	if err != nil {
		t.Fatalf("unexpected error: %v; stderr=%q", err, stderr.String())
	}
}

// TestKBConfigGet_UnknownSlug verifies the user-facing error format when the
// resolver reports kb-not-found.
//
// Failure prevented: a typo'd slug returning a confusing internal error
// instead of the canonical `kb not found: #<slug>` form.
func TestKBConfigGet_UnknownSlug(t *testing.T) {
	withKBConfigResolver(t, func(_ context.Context, _ string) (string, error) {
		return "", errKBNotFound
	})

	cmd := newRootForTest(t)
	_, stderr, err := executeCmd(cmd, "kb", "config", "get", "--kb=nonexistent", "features.visibility")
	if err == nil {
		t.Fatal("expected error for unknown slug")
	}
	if !strings.Contains(stderr.String(), "kb not found: #nonexistent") {
		t.Errorf("stderr=%q; want 'kb not found: #nonexistent'", stderr.String())
	}
}

// TestKBConfigList_SlugAccepted verifies the slug acceptance generalizes to
// `kb config list` (the spec asks for both commands).
//
// Failure prevented: get/list diverging on argument acceptance — they share
// loadKBConfigContext but a future split could re-introduce drift.
func TestKBConfigList_SlugAccepted(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv(config.EnvUserConfig, filepath.Join(tmp, "config.yaml"))
	t.Setenv(config.EnvSessionRecording, "")

	withKBConfigResolver(t, func(_ context.Context, raw string) (string, error) {
		if raw != "marketing" {
			t.Errorf("resolver raw=%q; want %q", raw, "marketing")
		}
		return "kb_list_resolved", nil
	})

	cmd := newRootForTest(t)
	stdout, stderr, err := executeCmd(cmd, "kb", "config", "list", "--kb=marketing", "--json")
	if err != nil {
		t.Fatalf("unexpected error: %v; stderr=%q", err, stderr.String())
	}

	var got struct {
		KBID string `json:"kb_id"`
	}
	if jerr := json.Unmarshal(stdout.Bytes(), &got); jerr != nil {
		t.Fatalf("json parse: %v\nstdout=%q", jerr, stdout.String())
	}
	if got.KBID != "kb_list_resolved" {
		t.Errorf("kb_id=%q; want kb_list_resolved", got.KBID)
	}
}

// TestKBConfigGet_APIUnreachable verifies the helpful error message when the
// resolver bails on api-unreachable rather than not-found.
//
// Failure prevented: users staring at "kb not found" when their network/
// daemon is the actual problem, with no hint that `--kb=kb_<id>` would work.
func TestKBConfigGet_APIUnreachable(t *testing.T) {
	withKBConfigResolver(t, func(_ context.Context, _ string) (string, error) {
		// Wrap so errors.Is(_, errKBAPIUnreachable) trips in formatKBInputError.
		return "", wrapForTest(errKBAPIUnreachable, "kb api unreachable: HTTP 500")
	})

	cmd := newRootForTest(t)
	_, stderr, err := executeCmd(cmd, "kb", "config", "get", "--kb=marketing", "features.visibility")
	if err == nil {
		t.Fatal("expected error when kb-API is unreachable")
	}
	if !strings.Contains(stderr.String(), "kb-API unavailable") {
		t.Errorf("stderr=%q; want hint about kb-API unavailable", stderr.String())
	}
	if !strings.Contains(stderr.String(), "--kb=kb_<id>") {
		t.Errorf("stderr=%q; want kb_id escape-hatch hint", stderr.String())
	}
}

// wrapForTest is a tiny helper to produce errors that satisfy errors.Is(err, target).
func wrapForTest(target error, msg string) error {
	return &wrappedTestErr{target: target, msg: msg}
}

type wrappedTestErr struct {
	target error
	msg    string
}

func (w *wrappedTestErr) Error() string { return w.msg }
func (w *wrappedTestErr) Unwrap() error { return w.target }

func TestKBConfigGet_DefaultVisibility(t *testing.T) {
	// Point user config at an empty temp dir so the user layer never lights up.
	tmp := t.TempDir()
	t.Setenv(config.EnvUserConfig, filepath.Join(tmp, "config.yaml"))
	t.Setenv(config.EnvSessionRecording, "")

	cmd := newRootForTest(t)
	stdout, _, err := executeCmd(cmd, "kb", "config", "get", "--kb=kb_test", "features.visibility")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := strings.TrimSpace(stdout.String()); got != "private" {
		t.Errorf("stdout=%q; want 'private' (built-in default)", got)
	}
}

func TestKBConfigList_JSONShape(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv(config.EnvUserConfig, filepath.Join(tmp, "config.yaml"))
	t.Setenv(config.EnvSessionRecording, "")

	cmd := newRootForTest(t)
	stdout, _, err := executeCmd(cmd, "kb", "config", "list", "--kb=kb_test", "--json")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var got struct {
		KBID string `json:"kb_id"`
		Keys []struct {
			Key       string `json:"key"`
			Effective string `json:"effective"`
		} `json:"keys"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("json parse: %v\nstdout=%q", err, stdout.String())
	}
	if got.KBID != "kb_test" {
		t.Errorf("kb_id=%q; want kb_test", got.KBID)
	}
	if len(got.Keys) != 3 {
		t.Errorf("len(keys)=%d; want 3 (one per known key)", len(got.Keys))
	}
}

func TestKBConfigGet_KBLayerReadsFromConfigYAML(t *testing.T) {
	// Build a KB checkout with config.yaml pinning visibility=team and assert
	// that the KB-layer read surfaces "team". Use paths.KBDir as the canonical
	// helper so the test stays in sync with on-disk layout, and pin a known
	// endpoint so the path under XDG is deterministic.
	tmp := t.TempDir()
	t.Setenv("XDG_DATA_HOME", tmp)
	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("SAGEOX_ENDPOINT", "test-endpoint")
	t.Setenv(config.EnvUserConfig, filepath.Join(tmp, "user-config.yaml"))
	t.Setenv(config.EnvSessionRecording, "")

	kbCfgDir := filepath.Join(paths.KBDir("kb_xyz"), ".sageox")
	if err := os.MkdirAll(kbCfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "version: 1\nfeatures:\n  visibility: team\n"
	if err := os.WriteFile(filepath.Join(kbCfgDir, "config.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := newRootForTest(t)
	stdout, _, err := executeCmd(cmd, "kb", "config", "get", "--kb=kb_xyz", "features.visibility")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := strings.TrimSpace(stdout.String()); got != "team" {
		t.Fatalf("stdout=%q; want %q", got, "team")
	}
}

func TestKBConfigGet_DefaultSessionRecordingUsesLocalKBType(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_DATA_HOME", tmp)
	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("SAGEOX_ENDPOINT", "test-endpoint")
	t.Setenv(config.EnvUserConfig, filepath.Join(tmp, "user-config.yaml"))
	t.Setenv(config.EnvSessionRecording, "")

	metaDir := filepath.Join(paths.KBDir("kb_personal"), ".sageox")
	require.NoError(t, os.MkdirAll(metaDir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(metaDir, "meta.json"),
		[]byte(`{"type":"personal","slug":"my-personal"}`),
		0o644,
	))

	cmd := newRootForTest(t)
	stdout, stderr, err := executeCmd(cmd, "kb", "config", "get", "--kb=kb_personal", "features.session_recording.mode")
	if err != nil {
		t.Fatalf("unexpected error: %v; stderr=%q", err, stderr.String())
	}
	if got := strings.TrimSpace(stdout.String()); got != "auto" {
		t.Fatalf("stdout=%q; want %q", got, "auto")
	}
}

// newRootForTest builds a fresh root command tree for a test invocation. The
// global rootCmd in this package is fine to reuse across tests because cobra
// resets state in Execute, but we wrap it so SetArgs / SetOut don't bleed.
func newRootForTest(_ *testing.T) *cobraCommandLike {
	return &cobraCommandLike{cmd: rootCmd}
}

type cobraCommandLike struct{ cmd interface{} }

// executeCmd drives the rootCmd cobra surface with the given args, capturing
// stdout/stderr. It's a thin helper that mirrors patterns used in the
// existing cmd/ox tests (e.g. kb_path_test.go).
func executeCmd(c *cobraCommandLike, args ...string) (stdout, stderr *bytes.Buffer, err error) {
	stdout = &bytes.Buffer{}
	stderr = &bytes.Buffer{}

	rootCmd.SetArgs(args)
	rootCmd.SetOut(stdout)
	rootCmd.SetErr(stderr)
	defer func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
	}()

	err = rootCmd.ExecuteContext(context.Background())
	return stdout, stderr, err
}
