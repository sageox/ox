//go:build darwin || linux

package nativeimport

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/sageox/ox/internal/paths"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/adapters"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Both conversion and the writer load policy. Pause the canonical user-policy
// read after repo policy has been read, then edit that repo policy. The strict
// writer must reject the newly invalid policy rather than exposing entries
// that were converted under its old version.
func TestPreviewEntriesRevalidatesPolicyAfterConversion(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("OX_XDG_DISABLE", "")
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, ".sageox"), 0o700))
	repoPolicy := filepath.Join(root, ".sageox", "REDACT.md")
	require.NoError(t, os.WriteFile(repoPolicy, []byte("```redact\nliteral \"private-source\" -> [PRIVATE]\n```\n"), 0o600))
	require.NoError(t, os.MkdirAll(paths.ConfigDir(), 0o700))
	userPolicy := filepath.Join(paths.ConfigDir(), "REDACT.md")
	require.NoError(t, syscall.Mkfifo(userPolicy, 0o600))

	// If a peer never opens the FIFO, cleanup pairs with a blocked read-open
	// and delivers EOF. All communication below also has bounded deadlines.
	t.Cleanup(func() {
		if file, err := os.OpenFile(userPolicy, os.O_RDWR|syscall.O_NONBLOCK, 0); err == nil {
			_ = file.Close()
		}
	})
	type result struct {
		entries []session.Entry
		err     error
	}
	done := make(chan result, 1)
	go func() {
		entries, err := PreviewEntries(root, []adapters.RawEntry{{Role: "user", Content: "private-source"}})
		done <- result{entries: entries, err: err}
	}()

	var writer *os.File
	deadline := time.Now().Add(5 * time.Second)
	for {
		var err error
		writer, err = os.OpenFile(userPolicy, os.O_WRONLY|syscall.O_NONBLOCK, 0)
		if err == nil {
			break
		}
		require.ErrorIs(t, err, syscall.ENXIO)
		select {
		case result := <-done:
			t.Fatalf("preview finished before the policy boundary: %v", result.err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("preview did not reach the user-policy read")
		}
		time.Sleep(time.Millisecond)
	}
	defer func() { _ = writer.Close() }()

	// The reader now holds the FIFO's original inode. Replace its path with
	// a regular file so the writer's second policy load cannot block.
	require.NoError(t, os.Remove(userPolicy))
	require.NoError(t, os.WriteFile(userPolicy, nil, 0o600))
	require.NoError(t, os.WriteFile(repoPolicy, []byte("```redact\nregex \"[\" -> [X]\n```\n"), 0o600))
	_, err := fmt.Fprintln(writer, "user policy has no custom rules")
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	select {
	case result := <-done:
		require.ErrorContains(t, result.err, "invalid redaction policy")
		assert.Nil(t, result.entries, "no preview escapes when policy changes before the strict writer")
		files, err := os.ReadDir(root)
		require.NoError(t, err)
		assert.Len(t, files, 1, "only the test's preexisting policy directory exists")
	case <-time.After(5 * time.Second):
		t.Fatal("preview did not finish after policy input was closed")
	}
}
