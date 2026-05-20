package main

// agent_prime_current_kb_test.go — tests for the resolveCurrentKBEntry
// helper extracted from runAgentPrime. The helper is the unit boundary that
// runAgentPrime calls to populate output.CurrentKB — testing it directly
// avoids spinning up the full prime pipeline (auth, daemon, instance store)
// just to assert one field is populated.

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/prime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeKBBindingYAML writes a minimal .sageox/config.yaml carrying the given
// kb_id. Returns the project root so the caller can pass it as cwd.
func writeKBBindingYAML(t *testing.T, kbID string) string {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".sageox"), 0o755))
	body := "kb_id: " + kbID + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(root, ".sageox", "config.yaml"), []byte(body), 0o644))
	return root
}

// TestResolveCurrentKBEntry_CurrentKBPresent verifies the happy path: a
// project with a binding kb_id that matches a row in the KB list returns the
// matched row.
//
// Failure prevented: a regression in the resolver wiring — or in the lookup
// loop's KBID equality check — would silently leave output.CurrentKB nil,
// stranding agents in a no-current-KB state even when the binding is valid.
func TestResolveCurrentKBEntry_CurrentKBPresent(t *testing.T) {
	root := writeKBBindingYAML(t, "kb_test123")

	kbList := []prime.KBInfo{
		{KBID: "kb_other", Type: "team", Slug: "platform"},
		{KBID: "kb_test123", Type: "personal", Slug: "personal-abc", Name: "Ryan's Personal"},
	}

	got := resolveCurrentKBEntry(root, kbList)
	require.NotNil(t, got, "binding kb_id present in list must produce a CurrentKB")
	assert.Equal(t, "kb_test123", got.KBID)
	assert.Equal(t, "personal-abc", got.Slug)
	assert.Equal(t, "personal", got.Type)
}

// TestResolveCurrentKBEntry_CurrentKBNil_OutsideTree verifies that a cwd
// with no .sageox/ marker on the upward walk returns nil — the documented
// "outside any KB-bound tree" case from ADR-017.
//
// Failure prevented: a resolver bug that fabricated a binding for an
// uninitialized directory would attach the wrong KB to a prime session,
// causing recording to land in the wrong bubble.
func TestResolveCurrentKBEntry_CurrentKBNil_OutsideTree(t *testing.T) {
	// fresh tempdir with no .sageox/ anywhere on the path.
	bare := t.TempDir()

	kbList := []prime.KBInfo{
		{KBID: "kb_anything", Type: "team", Slug: "platform"},
	}

	got := resolveCurrentKBEntry(bare, kbList)
	assert.Nil(t, got, "cwd outside any KB-bound tree must yield nil CurrentKB")
}

// TestResolveCurrentKBEntry_BindingNotInList_WarnsAndReturnsNil verifies the
// revoked / unsynced case: a binding kb_id that isn't present in the KB list
// logs a warn and returns nil so the agent doesn't reference a vanished row.
//
// Failure prevented: a silent regression here would either crash on nil
// access downstream or, worse, attribute work to a kb_id the agent can't
// actually read — exactly the post-revocation footgun ADR-017 §7 calls out.
func TestResolveCurrentKBEntry_BindingNotInList_WarnsAndReturnsNil(t *testing.T) {
	root := writeKBBindingYAML(t, "kb_revoked")

	// capture slog so we can assert the warn path fires.
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
	buf := &bytes.Buffer{}
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))

	kbList := []prime.KBInfo{
		{KBID: "kb_other", Type: "team", Slug: "platform"},
	}

	got := resolveCurrentKBEntry(root, kbList)
	assert.Nil(t, got, "binding not in list must return nil")
	assert.Contains(t, buf.String(), "current_kb_not_in_list", "warn must fire so operators see the mismatch")
	assert.Contains(t, buf.String(), "kb_revoked", "warn must carry the missing kb_id for diagnosis")
}

// TestResolveCurrentKBEntry_EmptyCwd verifies a defensive guard: an empty
// cwd (caller passed "") returns nil rather than treating the filesystem
// root as a binding anchor.
//
// Failure prevented: a code path that forgot to populate projectRoot would
// otherwise walk from "" — which ResolveCurrentKB normalizes — but the
// helper's contract is "no binding, no result" for the empty input.
func TestResolveCurrentKBEntry_EmptyCwd(t *testing.T) {
	got := resolveCurrentKBEntry("", []prime.KBInfo{{KBID: "kb_x"}})
	assert.Nil(t, got)
}
