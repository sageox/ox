package kb

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/endpoint"
)

// writeMarker is a tiny helper that creates `<dir>/.sageox/<name>` with the
// given contents. Returns the anchor (i.e. dir).
func writeMarker(t *testing.T, dir, name, contents string) string {
	t.Helper()
	sageoxPath := filepath.Join(dir, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxPath, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sageoxPath, name), []byte(contents), 0o600))
	return dir
}

func TestResolveCurrentKB_NoMarker(t *testing.T) {
	// cwd in a tmpdir with no .sageox/ anywhere up to root — must return
	// (nil, nil), not an error. This is the "outside any KB" no-op case.
	dir := t.TempDir()
	sub := filepath.Join(dir, "nested", "deeper")
	require.NoError(t, os.MkdirAll(sub, 0o755))

	got, err := ResolveCurrentKB(sub)
	require.NoError(t, err)
	require.Nil(t, got)
}

func TestResolveCurrentKB_WorkspaceJSON(t *testing.T) {
	// Legacy .sageox/config.json with kb_id (and repo_id, which the resolver
	// ignores). Workspace artifacts (cache/) make IsWorkspace true.
	dir := t.TempDir()
	writeMarker(t, dir, "config.json",
		`{"kb_id":"kb_01abc","repo_id":"repo_xyz","endpoint":"sageox.ai"}`)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".sageox", "cache"), 0o755))

	got, err := ResolveCurrentKB(dir)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, "kb_01abc", got.KBID)
	require.Equal(t, filepath.Join(".sageox", "config.json"), got.Source)
	require.Equal(t, ScopeExclusive, got.Scope)
	require.Equal(t, "https://sageox.ai", got.Endpoint)
	require.True(t, got.IsWorkspace())
}

func TestResolveCurrentKB_BindingOnlyYAML(t *testing.T) {
	// Minimum binding-only marker: just kb_id in config.yaml, no cache/ledger.
	// IsWorkspace must be false.
	dir := t.TempDir()
	writeMarker(t, dir, "config.yaml", "kb_id: kb_personal_01\n")

	got, err := ResolveCurrentKB(dir)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, "kb_personal_01", got.KBID)
	require.Equal(t, filepath.Join(".sageox", "config.yaml"), got.Source)
	require.False(t, got.IsWorkspace())
}

func TestResolveCurrentKB_YAMLPriorityOverJSON(t *testing.T) {
	// Both formats present in the same directory: yaml wins per ADR-017 §2.
	dir := t.TempDir()
	writeMarker(t, dir, "config.json", `{"kb_id":"kb_legacy"}`)
	writeMarker(t, dir, "config.yaml", "kb_id: kb_current\n")

	got, err := ResolveCurrentKB(dir)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, "kb_current", got.KBID)
	require.Equal(t, filepath.Join(".sageox", "config.yaml"), got.Source)
}

func TestResolveCurrentKB_EndpointExplicit(t *testing.T) {
	// Explicit endpoint in the marker file propagates (and is normalized —
	// www.dev.sageox.ai → dev.sageox.ai per endpoints rule).
	dir := t.TempDir()
	writeMarker(t, dir, "config.yaml", "kb_id: kb_01\nendpoint: www.dev.sageox.ai\n")

	got, err := ResolveCurrentKB(dir)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, "https://dev.sageox.ai", got.Endpoint)
}

func TestResolveCurrentKB_EndpointFallback(t *testing.T) {
	// No endpoint: in the file → endpoint.Get() is used. We exercise the
	// "env var wins" branch of endpoint.Get to make this deterministic
	// without depending on the user's logged-in endpoints or default.
	t.Setenv(endpoint.EnvVar, "https://fallback.sageox.ai")

	dir := t.TempDir()
	writeMarker(t, dir, "config.yaml", "kb_id: kb_01\n")

	got, err := ResolveCurrentKB(dir)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, endpoint.NormalizeEndpoint("https://fallback.sageox.ai"), got.Endpoint)
}

func TestResolveCurrentKB_NestedMarkersRejected(t *testing.T) {
	// docs/.sageox inside a repo .sageox/ → subtree override, rejected in v1.
	outer := t.TempDir()
	writeMarker(t, outer, "config.yaml", "kb_id: kb_outer\n")

	inner := filepath.Join(outer, "docs")
	writeMarker(t, inner, "config.yaml", "kb_id: kb_inner\n")

	got, err := ResolveCurrentKB(inner)
	require.Nil(t, got)
	require.ErrorIs(t, err, ErrSubtreeOverridesNotSupported)
}

func TestResolveCurrentKB_NearestWinsWithoutNesting(t *testing.T) {
	// A marker only at the outer dir: walking up from a subdir finds it.
	// (No inner marker, so subtree-override doesn't trigger.)
	outer := t.TempDir()
	writeMarker(t, outer, "config.yaml", "kb_id: kb_outer\n")

	deep := filepath.Join(outer, "src", "pkg")
	require.NoError(t, os.MkdirAll(deep, 0o755))

	got, err := ResolveCurrentKB(deep)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, "kb_outer", got.KBID)
	// resolved anchor uses the real path; on macOS t.TempDir lives under
	// /private/var, so compare via EvalSymlinks rather than literal equality.
	wantAnchor, err := filepath.EvalSymlinks(outer)
	require.NoError(t, err)
	require.Equal(t, wantAnchor, got.Anchor)
}

func TestResolveCurrentKB_MalformedYAML(t *testing.T) {
	// Malformed YAML → parse error, not silently nil. The bead description
	// explicitly calls this out: a hand-edited bad marker must surface.
	dir := t.TempDir()
	writeMarker(t, dir, "config.yaml", "kb_id: [unterminated\n")

	got, err := ResolveCurrentKB(dir)
	require.Error(t, err)
	require.Nil(t, got)
}

func TestResolveCurrentKB_MalformedJSON(t *testing.T) {
	dir := t.TempDir()
	writeMarker(t, dir, "config.json", `{"kb_id":`)

	got, err := ResolveCurrentKB(dir)
	require.Error(t, err)
	require.Nil(t, got)
}

func TestResolveCurrentKB_LegacyMarkerWithoutKBID(t *testing.T) {
	// Pre-ADR-017 config.json with only repo_id is not a KB binding. ADR-017
	// §7 says this synthesizes kb_type=repo via the merger; the resolver itself
	// returns (nil, nil) so callers fall through to legacy code paths cleanly.
	dir := t.TempDir()
	writeMarker(t, dir, "config.json", `{"repo_id":"repo_abc"}`)

	got, err := ResolveCurrentKB(dir)
	require.NoError(t, err)
	require.Nil(t, got)
}

func TestResolveCurrentKB_EmptyCwd(t *testing.T) {
	got, err := ResolveCurrentKB("")
	require.NoError(t, err)
	require.Nil(t, got)
}

func TestIsWorkspace_CacheDir(t *testing.T) {
	dir := t.TempDir()
	writeMarker(t, dir, "config.yaml", "kb_id: kb_01\n")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".sageox", "cache"), 0o755))

	got, err := ResolveCurrentKB(dir)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.True(t, got.IsWorkspace())
}

func TestIsWorkspace_LedgerDir(t *testing.T) {
	dir := t.TempDir()
	writeMarker(t, dir, "config.yaml", "kb_id: kb_01\n")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".sageox", "ledger"), 0o755))

	got, err := ResolveCurrentKB(dir)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.True(t, got.IsWorkspace())
}

func TestIsWorkspace_KBLink(t *testing.T) {
	if runtime.GOOS == "windows" {
		// os.Symlink requires elevated privileges or Developer Mode on
		// Windows; the workspace-marker semantics are platform-agnostic
		// (the function just looks for any dir entry), so skipping here
		// is safe.
		t.Skip("symlink creation may require elevated privileges on Windows")
	}
	dir := t.TempDir()
	writeMarker(t, dir, "config.yaml", "kb_id: kb_01\n")
	kbDir := filepath.Join(dir, ".sageox", "kb")
	require.NoError(t, os.MkdirAll(kbDir, 0o755))
	// any entry under kb/ counts as a workspace marker.
	require.NoError(t, os.Symlink("/tmp/nonexistent-target", filepath.Join(kbDir, "team-conventions")))

	got, err := ResolveCurrentKB(dir)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.True(t, got.IsWorkspace())
}

func TestIsWorkspace_BindingOnly(t *testing.T) {
	dir := t.TempDir()
	writeMarker(t, dir, "config.yaml", "kb_id: kb_01\n")

	got, err := ResolveCurrentKB(dir)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.False(t, got.IsWorkspace())
}

func TestIsWorkspace_NilReceiver(t *testing.T) {
	var b *KBBinding
	require.False(t, b.IsWorkspace())
}

// TestErrSubtreeOverridesNotSupported_ErrorsIs makes sure callers can identify
// the sentinel via errors.Is — it would be unfortunate if a future refactor
// wrapped the error in a way that broke this.
func TestErrSubtreeOverridesNotSupported_ErrorsIs(t *testing.T) {
	outer := t.TempDir()
	writeMarker(t, outer, "config.yaml", "kb_id: kb_outer\n")
	inner := filepath.Join(outer, "nested")
	writeMarker(t, inner, "config.yaml", "kb_id: kb_inner\n")

	_, err := ResolveCurrentKB(inner)
	require.True(t, errors.Is(err, ErrSubtreeOverridesNotSupported))
}
