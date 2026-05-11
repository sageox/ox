package language

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// expectedGotreesitterSum is the content hash from go.sum for the pinned
// fork version of github.com/odvcencio/gotreesitter. Per ox-abtj: this
// dependency is a personal fork; the supply-chain risk is that the
// upstream account is compromised and a new tag retags v0.13.0 to point
// at malicious content. Our go.sum entry would reject that on `go mod
// download`, but a developer who runs `go get` and refreshes go.sum
// without auditing would silently miss the change. This test pins the
// expected sum so any drift (including legitimate updates) triggers a
// loud failure and forces a human review.
//
// If you ever bump this dependency:
//  1. Compare the diff against upstream tree-sitter / tree-sitter-go.
//  2. Update the constant to match the new go.sum line.
//  3. Note the audit findings in the commit message.
const expectedGotreesitterSum = `github.com/odvcencio/gotreesitter v0.13.0 h1:y2CuuMjh88r648IQQph4mDbt0i3cA6G6ZKt8hUq5Y4g=`

// TestGotreesitterPinned asserts the expected content-hash line is
// present in go.sum. If this test fails, someone changed the dependency
// version or content and the supply-chain audit needs to be redone.
func TestGotreesitterPinned(t *testing.T) {
	// walk up from this test file to find the repo root (the dir
	// containing go.sum). Test runs from package dir so /../../.. lands
	// at the module root.
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok, "could not resolve runtime caller")
	root := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", "..", ".."))

	sum, err := os.ReadFile(filepath.Join(root, "go.sum"))
	require.NoError(t, err, "go.sum should exist at module root")

	if !strings.Contains(string(sum), expectedGotreesitterSum) {
		t.Fatalf(
			"go.sum no longer contains the expected pin for "+
				"github.com/odvcencio/gotreesitter v0.13.0.\n\n"+
				"This is a personal-fork dependency (see ox-abtj). Any drift "+
				"requires human audit BEFORE the test pin is updated:\n"+
				"  1. Diff the new tag against upstream tree-sitter / tree-sitter-go.\n"+
				"  2. Confirm no malicious changes have been introduced.\n"+
				"  3. Update expectedGotreesitterSum with the new go.sum line.\n\n"+
				"Expected line:\n  %s",
			expectedGotreesitterSum,
		)
	}
}
