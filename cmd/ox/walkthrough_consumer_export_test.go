//go:build slow

package main

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// This opt-in harness export lets a real consuming agent read the compiled CLI
// and inspect producer images after the test process exits. Credentials are the
// existing hermetic fake login, never a developer's real account or token.
func TestExportWalkthroughConsumerWorkspace(t *testing.T) {
	exportRoot := os.Getenv("SAGEOX_WALKTHROUGH_CONSUMER_EXPORT")
	clone := os.Getenv("SAGEOX_WALKTHROUGH_PUBLISHED_CLONE")
	if exportRoot == "" || clone == "" {
		t.Skip("requires explicit consumer export and published clone")
	}
	e2e := setupConversationE2E(t)
	require.NoError(t, os.RemoveAll(filepath.Join(e2e.primaryTeam.path, "discussions")))
	copyTree := func(source, target string) {
		require.NoError(t, filepath.WalkDir(source, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, e := filepath.Rel(source, p)
			if e != nil {
				return e
			}
			dst := filepath.Join(target, rel)
			if d.IsDir() {
				return os.MkdirAll(dst, 0755)
			}
			b, e := os.ReadFile(p)
			if e != nil {
				return e
			}
			return os.WriteFile(dst, b, 0600)
		}))
	}
	copyTree(filepath.Join(clone, "discussions"), filepath.Join(e2e.primaryTeam.path, "discussions"))
	var entries []map[string]any
	folders, err := os.ReadDir(filepath.Join(e2e.primaryTeam.path, "discussions"))
	require.NoError(t, err)
	for _, folder := range folders {
		if !folder.IsDir() {
			continue
		}
		var recording string
		require.NoError(t, filepath.WalkDir(filepath.Join(e2e.primaryTeam.path, "discussions", folder.Name()), func(p string, d fs.DirEntry, e error) error {
			if e != nil {
				return e
			}
			if d.Name() != "layer.json" {
				return nil
			}
			b, e := os.ReadFile(p)
			if e != nil {
				return e
			}
			var layer struct {
				ConversationID string `json:"conversation_id"`
			}
			if json.Unmarshal(b, &layer) == nil {
				recording = strings.Replace(layer.ConversationID, "cnv_", "rec_", 1)
			}
			return nil
		}))
		if recording == "" {
			continue
		}
		entries = append(entries, map[string]any{"folder": folder.Name(), "recording_id": recording, "title": "Synthetic walkthrough feedback"})
	}
	b, err := json.Marshal(entries)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(e2e.primaryTeam.path, "discussions", "INDEX.json"), b, 0644))
	oldRoot := filepath.Dir(e2e.workspace)
	copyTree(oldRoot, exportRoot)
	// Rewrite only generated harness config references after relocating it.
	localConfig := filepath.Join(exportRoot, "workspace", ".sageox", "config.local.toml")
	b, err = os.ReadFile(localConfig)
	require.NoError(t, err)
	b = []byte(strings.ReplaceAll(string(b), oldRoot, exportRoot))
	require.NoError(t, os.WriteFile(localConfig, b, 0600))
	var env []string
	for _, entry := range e2e.env {
		env = append(env, strings.ReplaceAll(entry, oldRoot, exportRoot))
	}
	b, err = json.Marshal(map[string]any{"workspace": filepath.Join(exportRoot, "workspace"), "environment": env, "recordings": entries, "auth": "hermetic fake credential and seeded membership cache"})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(exportRoot, "environment.json"), b, 0600))
}
