package kb

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/paths"
)

// setupEnrichEnv pins endpoint + XDG_DATA_HOME so paths.KBDir resolves into a
// tmp tree owned by the test rather than the user's real ~/.local/share.
func setupEnrichEnv(t *testing.T) string {
	t.Helper()
	t.Setenv(endpoint.EnvVar, "https://test.sageox.ai")
	dataHome := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dataHome)
	return dataHome
}

func TestResolveCurrentKBEnriched_WithMeta(t *testing.T) {
	// happy path: marker resolves to a KB whose checkout has a daemon-written
	// meta.json — KBType + KBID populated.
	setupEnrichEnv(t)

	dir := t.TempDir()
	writeMarker(t, dir, "config.yaml", "kb_id: kb_01abc\n")

	// stage meta.json in the KB checkout (mirrors daemon's writeKBMeta).
	kbCheckout := paths.KBDir("kb_01abc")
	require.NoError(t, os.MkdirAll(filepath.Join(kbCheckout, ".sageox"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(kbCheckout, ".sageox", "meta.json"),
		[]byte(`{"type":"team","slug":"team-conventions","last_sync":"2026-05-19T00:00:00Z"}`),
		0o600,
	))

	got, err := ResolveCurrentKBEnriched(dir)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, "kb_01abc", got.KBID)
	require.Equal(t, "team", got.KBType)
}

func TestResolveCurrentKBEnriched_MissingMeta(t *testing.T) {
	// missing meta.json: still returns a binding (KBID populated), but KBType
	// is left empty. This is the brand-new-bubble-pre-daemon-sync state and
	// must NOT surface as an error.
	setupEnrichEnv(t)

	dir := t.TempDir()
	writeMarker(t, dir, "config.yaml", "kb_id: kb_02xyz\n")

	got, err := ResolveCurrentKBEnriched(dir)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, "kb_02xyz", got.KBID)
	require.Equal(t, "", got.KBType)
}

func TestResolveCurrentKBIDAndType_NoBinding(t *testing.T) {
	// outside any binding: helper degrades to ("", "") so callers can pass
	// straight through to config.ResolveSessionRecording without branching.
	setupEnrichEnv(t)

	dir := t.TempDir()
	sub := filepath.Join(dir, "no-marker-here")
	require.NoError(t, os.MkdirAll(sub, 0o755))

	kbID, kbType := ResolveCurrentKBIDAndType(sub)
	require.Equal(t, "", kbID)
	require.Equal(t, "", kbType)
}
