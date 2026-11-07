package gitutil

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Anything git reports that the scanner cannot fully read must hold the push:
// an unread or half-read object is not evidence that it is safe to publish.
func TestCommittedSessionScanFailsClosedOnUnreadableGitOutput(t *testing.T) {
	useFreshFixtureRules(t)
	ledger := publicationLedger(t, "data/plans/draft/plan.md", "# Ordinary plan\n")
	realGit, err := exec.LookPath("git")
	require.NoError(t, err)
	bin := t.TempDir()
	script := `#!/bin/sh
case "$FAKE_GIT_MODE: $* " in
  "revlist-fails:"*" rev-list "*) exit 1 ;;
  "raw-log-fails:"*" --raw "*) exit 1 ;;
  "name-log-fails:"*" --name-only "*) exit 1 ;;
  "no-output:"*" cat-file "*) cat >/dev/null; exit 0 ;;
  "missing-object:"*" cat-file "*) cat >/dev/null; echo "0123 missing"; exit 0 ;;
  "bad-size:"*" cat-file "*) cat >/dev/null; echo "0123 blob big"; exit 0 ;;
  "short-blob:"*" cat-file "*) cat >/dev/null; echo "0123 blob 100"; printf short; exit 0 ;;
  "short-tree:"*" cat-file "*) cat >/dev/null; echo "0123 tree 100"; printf short; exit 0 ;;
  "cat-file-fails:"*" cat-file "*) "` + realGit + `" "$@"; exit 1 ;;
esac
exec "` + realGit + `" "$@"
`
	require.NoError(t, os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	t.Setenv("FAKE_GIT_MODE", "none")
	require.NoError(t, ValidateSessionPublication(context.Background(), ledger), "control: an ordinary plan publishes")

	for _, mode := range []string{"revlist-fails", "raw-log-fails", "name-log-fails", "no-output", "missing-object", "bad-size", "short-blob", "short-tree", "cat-file-fails"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("FAKE_GIT_MODE", mode)
			require.ErrorContains(t, ValidateSessionPublication(context.Background(), ledger), "publication pending")
		})
	}
}

func TestCommittedSessionScanHoldsBlobOverScanLimit(t *testing.T) {
	useFreshFixtureRules(t)
	ledger := publicationLedger(t, "data/plans/draft/plan.md", strings.Repeat("a", sessionPublicationScanLimit+1))
	require.ErrorContains(t, ValidateSessionPublication(context.Background(), ledger), "exceeds scan limit")
}
