//go:build kb_twin

package kb_twin

// Comparison machinery for the kb twin. Walks the post-sync filesystem
// (paths.KBDir for the active endpoint, plus per-project .sageox/kb/
// symlink dirs) and asserts each property in ExpectedState.
//
// Diagnostic posture mirrors tests/ledger_twin/compare_test.go: when a
// scenario fails, the test output names the scenario, the phase, the
// kb_id, and the divergence in plain English. We never just say
// "expected true got false" without the surrounding context.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/paths"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// kbDiagContext bundles the information every divergence message
// needs. Carries scenario + phase so a failure deep in a multi-phase
// scenario points back at exactly the call site.
type kbDiagContext struct {
	scenarioName string
	phaseName    string
}

func (d kbDiagContext) prefix() string {
	if d.phaseName == "" {
		return fmt.Sprintf("[scenario=%s] ", d.scenarioName)
	}
	return fmt.Sprintf("[scenario=%s phase=%s] ", d.scenarioName, d.phaseName)
}

// assertExpectedState walks every category of expectation and emits a
// diagnostic message for each divergence. Uses assert (non-fatal) so
// a single scenario reports every problem in one run rather than
// stopping at the first.
func assertExpectedState(t *testing.T, dctx kbDiagContext, expected ExpectedState, projects map[string]string) {
	t.Helper()
	for kbID, ek := range expected.KBs {
		assertExpectedKB(t, dctx, kbID, ek)
	}
	for _, sl := range expected.Symlinks {
		assertExpectedSymlink(t, dctx, sl, projects)
	}
	for _, tr := range expected.Trash {
		assertExpectedTrash(t, dctx, tr)
	}
}

// assertExpectedKB checks a single bubble's on-disk state.
func assertExpectedKB(t *testing.T, dctx kbDiagContext, kbID string, ek ExpectedKB) {
	t.Helper()
	target := paths.KBDir(kbID)
	gitDir := filepath.Join(target, ".git")

	_, gitErr := os.Stat(gitDir)
	exists := gitErr == nil

	if ek.MustExist && !exists {
		t.Errorf("%skb=%s expected canonical clone at %s, but .git/ is missing (stat err: %v)",
			dctx.prefix(), kbID, target, gitErr)
		return
	}
	if !ek.MustExist && exists {
		t.Errorf("%skb=%s expected NO canonical clone, but found .git/ at %s",
			dctx.prefix(), kbID, target)
		return
	}
	if !ek.MustExist {
		return // nothing else to check
	}

	// meta.json
	metaPath := filepath.Join(target, ".sageox", "meta.json")
	raw, err := os.ReadFile(metaPath)
	if err != nil {
		t.Errorf("%skb=%s meta.json read failed: %v", dctx.prefix(), kbID, err)
		return
	}
	var meta struct {
		Type        api.KBType `json:"type"`
		Slug        string     `json:"slug"`
		OwnerUserID string     `json:"owner_user_id"`
		ViewerRole  string     `json:"viewer_role"`
		LastSync    string     `json:"last_sync"`
	}
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Errorf("%skb=%s meta.json parse failed: %v\nraw=%s", dctx.prefix(), kbID, err, string(raw))
		return
	}

	if ek.MetaType != "" {
		assert.Equal(t, ek.MetaType, meta.Type,
			"%skb=%s meta.type mismatch", dctx.prefix(), kbID)
	}
	if ek.MetaSlug != "" {
		assert.Equal(t, ek.MetaSlug, meta.Slug,
			"%skb=%s meta.slug mismatch", dctx.prefix(), kbID)
	}
	if ek.MetaOwnerUserID != "" {
		assert.Equal(t, ek.MetaOwnerUserID, meta.OwnerUserID,
			"%skb=%s meta.owner_user_id mismatch", dctx.prefix(), kbID)
	}
	if ek.MetaViewerRole != "" {
		assert.Equal(t, ek.MetaViewerRole, meta.ViewerRole,
			"%skb=%s meta.viewer_role mismatch", dctx.prefix(), kbID)
	}
	assert.NotEmpty(t, meta.LastSync,
		"%skb=%s meta.last_sync must be populated", dctx.prefix(), kbID)

	// file content checks
	for relPath, wantContent := range ek.FileChecks {
		full := filepath.Join(target, relPath)
		got, err := os.ReadFile(full)
		if err != nil {
			t.Errorf("%skb=%s expected file %s but got read error: %v",
				dctx.prefix(), kbID, relPath, err)
			continue
		}
		if wantContent != "" {
			assert.Equal(t, wantContent, string(got),
				"%skb=%s file %s content mismatch", dctx.prefix(), kbID, relPath)
		}
	}
}

// assertExpectedSymlink checks one per-project symlink. Uses os.Lstat
// and os.Readlink so we never accidentally follow a broken link.
func assertExpectedSymlink(t *testing.T, dctx kbDiagContext, sl ExpectedSymlink, projects map[string]string) {
	t.Helper()
	projectRoot, ok := projects[sl.ProjectKey]
	if !ok {
		t.Errorf("%ssymlink check refers to unknown project key %q (known: %v)",
			dctx.prefix(), sl.ProjectKey, sortedKeys(projects))
		return
	}
	link := filepath.Join(projectRoot, ".sageox", "kb", sl.Slug)

	info, err := os.Lstat(link)
	switch {
	case sl.Exists && os.IsNotExist(err):
		t.Errorf("%sexpected symlink at %s but it does not exist", dctx.prefix(), link)
		return
	case !sl.Exists:
		if err == nil {
			t.Errorf("%sexpected NO symlink at %s but it exists", dctx.prefix(), link)
		}
		return
	case err != nil:
		t.Errorf("%slstat %s: %v", dctx.prefix(), link, err)
		return
	}

	if info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("%sentry at %s is not a symlink (mode=%v)", dctx.prefix(), link, info.Mode())
		return
	}

	got, err := os.Readlink(link)
	if err != nil {
		t.Errorf("%sreadlink %s: %v", dctx.prefix(), link, err)
		return
	}
	want := paths.KBDir(sl.TargetKBID)
	if got != want {
		t.Errorf("%ssymlink %s -> %s, want %s",
			dctx.prefix(), link, got, want)
	}
}

// assertExpectedTrash inspects the kb/.trash dir. The reaper's
// timestamp-suffix format is internal; we only check kb_id prefix
// presence so changes to the suffix format don't ripple here.
func assertExpectedTrash(t *testing.T, dctx kbDiagContext, tr ExpectedTrash) {
	t.Helper()
	root := paths.KBDir("")
	trashDir := filepath.Join(root, ".trash")
	entries, err := os.ReadDir(trashDir)
	if err != nil && !os.IsNotExist(err) {
		t.Errorf("%sread trash dir %s: %v", dctx.prefix(), trashDir, err)
		return
	}

	found := false
	var observed []string
	for _, e := range entries {
		observed = append(observed, e.Name())
		if strings.HasPrefix(e.Name(), tr.KBIDPrefix+"-") {
			found = true
		}
	}

	if tr.MustBePresent && !found {
		t.Errorf("%sexpected trash entry with prefix %q in %s, observed=%v",
			dctx.prefix(), tr.KBIDPrefix, trashDir, observed)
	}
	if !tr.MustBePresent && found {
		t.Errorf("%sdid NOT expect trash entry with prefix %q, but found one (observed=%v)",
			dctx.prefix(), tr.KBIDPrefix, observed)
	}
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// requireFreshKBRoot makes sure each scenario starts from a clean
// canonical kb root. Without this, a previous scenario's leftover
// dirs would be misread as orphans by the GC pass of the next one.
// (Each scenario already gets a fresh XDG_DATA_HOME via t.TempDir,
// but this guard is cheap and intent-revealing.)
func requireFreshKBRoot(t *testing.T) {
	t.Helper()
	root := paths.KBDir("")
	if root == "" {
		require.Fail(t, "paths.KBDir resolution failed — endpoint not set?")
		return
	}
	entries, err := os.ReadDir(root)
	if err != nil && !os.IsNotExist(err) {
		require.NoError(t, err, "read kb root %s", root)
		return
	}
	require.Empty(t, entries, "kb root %s must be empty before scenario starts", root)
}
