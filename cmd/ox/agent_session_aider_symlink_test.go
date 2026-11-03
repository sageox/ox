package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/adapters"
	"github.com/stretchr/testify/require"
)

func TestProcessAgentSession_AiderRejectsEscapingHistory(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the real Aider adapter")
	}
	if runtime.GOOS == "windows" {
		t.Skip("requires Unix symlinks")
	}
	binary := filepath.Join(t.TempDir(), "ox-adapter-aider")
	build := exec.Command("go", "build", "-o", binary, "./cmd/ox-adapter-aider")
	build.Dir = repoPath("..", "..")
	output, err := build.CombinedOutput()
	require.NoError(t, err, "build Aider adapter: %s", output)
	adapter, err := adapters.NewExternalAdapter(binary)
	require.NoError(t, err)
	var prior adapters.Adapter
	for _, name := range adapters.ListAdapters() {
		if name == "aider" {
			prior, err = adapters.GetAdapter(name)
			require.NoError(t, err)
			break
		}
	}
	adapters.Unregister("aider")
	adapters.Register(adapter)
	t.Cleanup(func() {
		adapters.Unregister("aider")
		if prior != nil {
			adapters.Register(prior)
		}
	})

	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "unexpected publication", http.StatusInternalServerError)
	}))
	defer server.Close()
	t.Setenv("SAGEOX_ENDPOINT", server.URL)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("SAGEOX_DAEMON", "false")

	for _, discoverFirst := range []bool{false, true} {
		name := "already_symlinked"
		if discoverFirst {
			name = "replaced_after_discovery"
		}
		t.Run(name, func(t *testing.T) {
			repo := t.TempDir()
			require.NoError(t, os.Mkdir(filepath.Join(repo, ".sageox"), 0o700))
			history := filepath.Join(repo, ".aider.chat.history.md")
			started := time.Now()
			lookup := adapters.SessionLookup{RepoRoot: repo, AgentID: "OxAider", Since: started.Add(-5 * time.Minute)}
			if discoverFirst {
				require.NoError(t, os.WriteFile(history, []byte("workspace conversation\n"), 0o600))
				found, err := adapter.FindSessionFile(lookup)
				require.NoError(t, err)
				require.Equal(t, history, found)
				require.NoError(t, os.Remove(history))
			}
			outside := filepath.Join(t.TempDir(), "notes.md")
			require.NoError(t, os.WriteFile(outside, []byte("private local notes without an Aider header\n"), 0o600))
			require.NoError(t, os.Symlink(outside, history))
			if !discoverFirst {
				found, err := adapter.FindSessionFile(lookup)
				require.Error(t, err)
				require.Empty(t, found)
			}

			// A header-only hook recording takes CLI stop's full-read fallback,
			// even when a source path was saved before the symlink was installed.
			state := &session.RecordingState{
				AdapterName: "aider", AgentID: "OxAider", WorkspacePath: repo,
				SessionFile: history, SessionPath: t.TempDir(), StartedAt: started,
				WatchMode: "hook", PublishingMode: config.SessionPublishingAuto,
			}
			raw := filepath.Join(state.SessionPath, "raw.jsonl")
			before := []byte("{\"type\":\"header\"}\n")
			require.NoError(t, os.WriteFile(raw, before, 0o600))
			require.False(t, session.HasSubstantiveEntries(raw))
			result, err := processAgentSession(repo, state)
			require.ErrorContains(t, err, "failed to read session")
			require.Nil(t, result)
			after, err := os.ReadFile(raw)
			require.NoError(t, err)
			require.Equal(t, before, after, "rejected source must not be recorded")
			require.Zero(t, requests.Load(), "rejected source must not reach publishing")
		})
	}
}
