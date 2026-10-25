package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/lfs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeSessionFixture writes one session dir with a raw.jsonl and a meta.json whose
// manifest carries manifestRef for raw.jsonl (nil: the manifest names no files).
func writeSessionFixture(t *testing.T, sessionsDir, id, rawContent string, manifestRef *lfs.FileRef) {
	t.Helper()
	dir := filepath.Join(sessionsDir, id)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "raw.jsonl"), []byte(rawContent), 0o644))
	meta := map[string]any{"title": id}
	if manifestRef != nil {
		meta["files"] = map[string]lfs.FileRef{"raw.jsonl": *manifestRef}
	}
	data, err := json.Marshal(meta)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "meta.json"), data, 0o644))
}

func gitShow(t *testing.T, repo, spec string) string {
	t.Helper()
	out, err := runIsolatedGit(t, repo, "show", spec)
	require.NoError(t, err, "git show %s: %s", spec, out)
	return out + "\n" // runIsolatedGit trims output; pointers end in a newline
}

// TestRunSessionCommit_GuardsHydratedArtifacts covers hydrated session content sitting where
// LFS pointers belong (#1174). Without the guard, `git add <sessionsDir>` commits the raw
// bytes, the push validator then refuses every push, and the Ledger wedges.
func TestRunSessionCommit_GuardsHydratedArtifacts(t *testing.T) {
	project, _ := newSessionCommitProject(t)
	sessionsDir := filepath.Join(project, ".sageox", "sessions")

	pointerOnly := lfs.NewFileRef([]byte("uploaded earlier\n"))
	writeSessionFixture(t, sessionsDir, "2026-10-06T10-00-ann-OxPPPP", lfs.FormatPointer(pointerOnly.OID, pointerOnly.Size), &pointerOnly)

	hydratedContent := "{\"role\":\"user\"}\n{\"role\":\"assistant\"}\n"
	hydratedRef := lfs.NewFileRef([]byte(hydratedContent))
	writeSessionFixture(t, sessionsDir, "2026-10-06T11-00-bob-OxBBBB", hydratedContent, &hydratedRef)

	unknownContent := "{\"role\":\"user\",\"note\":\"no oid anywhere\"}\n"
	writeSessionFixture(t, sessionsDir, "2026-10-06T12-00-cy-OxCCCC", unknownContent, nil)

	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	require.NoError(t, runSessionCommit(sessionCommitCmd, nil))

	rel := func(id, name string) string { return "HEAD:.sageox/sessions/" + id + "/" + name }

	t.Run("a: pointer artifact is committed as-is", func(t *testing.T) {
		assert.Equal(t, lfs.FormatPointer(pointerOnly.OID, pointerOnly.Size),
			gitShow(t, project, rel("2026-10-06T10-00-ann-OxPPPP", "raw.jsonl")))
	})
	t.Run("b: hydrated artifact with a manifest OID is committed as a pointer", func(t *testing.T) {
		assert.Equal(t, lfs.FormatPointer(hydratedRef.OID, hydratedRef.Size),
			gitShow(t, project, rel("2026-10-06T11-00-bob-OxBBBB", "raw.jsonl")))
		cached, err := os.ReadFile(filepath.Join(project, ".sageox", "cache", "sessions", "2026-10-06T11-00-bob-OxBBBB", "raw.jsonl"))
		require.NoError(t, err, "restoring a pointer must keep the local copy in the cache")
		assert.Equal(t, hydratedContent, string(cached))
	})
	t.Run("c: hydrated artifact with no known OID is not staged and warns once", func(t *testing.T) {
		tracked, _ := runIsolatedGit(t, project, "ls-files", ".sageox/sessions/2026-10-06T12-00-cy-OxCCCC")
		assert.NotContains(t, tracked, "raw.jsonl", "raw content must not reach the commit")
		onDisk, err := os.ReadFile(filepath.Join(sessionsDir, "2026-10-06T12-00-cy-OxCCCC", "raw.jsonl"))
		require.NoError(t, err)
		assert.Equal(t, unknownContent, string(onDisk), "the unrepairable file is left untouched")
		warn := "2026-10-06T12-00-cy-OxCCCC/raw.jsonl"
		assert.Equal(t, 1, strings.Count(logs.String(), warn), "exactly one WARN names the path: %s", logs.String())
		assert.Contains(t, logs.String(), "level=WARN")
	})
	t.Run("d: plain meta.json files are committed", func(t *testing.T) {
		for _, id := range []string{"2026-10-06T10-00-ann-OxPPPP", "2026-10-06T11-00-bob-OxBBBB", "2026-10-06T12-00-cy-OxCCCC"} {
			assert.Contains(t, gitShow(t, project, rel(id, "meta.json")), id)
		}
	})
}
