package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/agentinstance"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/doctor"
	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Both doctor entry points must include cache work and explicit summary
// requests, exclude stubs, and distinguish uploads from summarization.
func TestDoctor_FinalizationEligibility(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git repositories for doctor path resolution")
	}
	t.Setenv("OX_SESSION_PUBLISHING", "auto")
	project, ledger := t.TempDir(), t.TempDir()
	runGit(t, project, "init")
	runGit(t, ledger, "init")
	require.NoError(t, os.MkdirAll(filepath.Join(project, ".sageox"), 0755))
	require.NoError(t, config.SaveLocalConfig(project, &config.LocalConfig{Ledger: &config.LedgerConfig{Path: ledger}}))
	t.Chdir(project)

	for _, fixture := range []struct {
		path  string
		files map[string]string
	}{
		{path: "sessions/unfinished"},
		{path: "sessions/stub", files: map[string]string{"raw.jsonl": "version https://git-lfs.github.com/spec/v1\noid sha256:" + strings.Repeat("a", 64) + "\nsize 1234\n"}},
		{path: ".sageox/cache/sessions/cache-only"},
		{path: ".sageox/cache/sessions/requested", files: map[string]string{"summary.md": "stub", "session.md": "stub", "summary.json": "{}", ".needs-summary": "{}"}},
		{path: ".sageox/cache/sessions/upload-only", files: map[string]string{"summary.md": "done", "session.md": "done", "summary.json": `{"title":"done"}`}},
	} {
		dir := filepath.Join(ledger, fixture.path)
		require.NoError(t, os.MkdirAll(dir, 0755))
		writeTestRawJSONLWithEntries(t, filepath.Join(dir, "raw.jsonl"), 2)
		for name, content := range fixture.files {
			require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0600))
		}
	}

	// An empty transcript already in the Ledger needs deterministic settlement,
	// not another AI-generated summary. Its bytes must match the Ledger manifest.
	emptyDir := filepath.Join(ledger, ".sageox", "cache", "sessions", "empty-rearmed")
	require.NoError(t, os.MkdirAll(emptyDir, 0755))
	rawPath := filepath.Join(emptyDir, "raw.jsonl")
	writeTestRawJSONLWithEntries(t, rawPath, 0)
	raw, err := os.ReadFile(rawPath)
	require.NoError(t, err)
	ledgerDir := filepath.Join(ledger, "sessions", "empty-rearmed")
	require.NoError(t, os.MkdirAll(ledgerDir, 0755))
	require.NoError(t, lfs.WriteSessionMetaOnly(ledgerDir, &lfs.SessionMeta{
		Version: "1.0", Files: map[string]lfs.FileRef{"raw.jsonl": lfs.NewFileRef(raw)},
	}))
	require.NoError(t, session.WriteNeedsSummaryMarker(emptyDir, rawPath, ledgerDir))

	before := snapshotDestination(t, ledger)
	output := buildAgentDoctorOutput("OxTest", project)
	require.True(t, output.Success, output.SessionScanError)
	var names []string
	for _, item := range output.IncompleteSessions {
		names = append(names, item.SessionID)
		assert.NotEmpty(t, item.SummarizePrompt)
	}
	assert.ElementsMatch(t, []string{"unfinished", "cache-only", "requested"}, names)
	assert.Equal(t, []string{"upload-only"}, output.PendingUploads)
	assert.Equal(t, []string{"empty-rearmed"}, output.PendingFinalization)
	result := checkSessionIncomplete(false)
	assert.True(t, result.requiresAgent)
	assert.Equal(t, "3 found", result.message)
	assert.Contains(t, result.detail, "Awaiting upload: upload-only")
	assert.Contains(t, result.detail, "Awaiting automatic finalization: empty-rearmed")
	assert.Equal(t, before, snapshotDestination(t, ledger), "inspection preserves session files and markers")
}

// An inspection acknowledges a stale chore only after its work is complete;
// failed scans and pending recording recovery must not lose their marker.
func TestAgentDoctor_AcknowledgesCompletedWork(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git repositories for doctor path resolution")
	}
	oldCfg := cfg
	cfg = &config.Config{}
	t.Cleanup(func() { cfg = oldCfg })
	t.Setenv("OX_SESSION_PUBLISHING", "auto")
	for _, state := range []string{"complete", "incomplete", "scan error", "git error", "recording recovery", "live recording", "live draft", "failed stop", "failed draft stop", "pending commit", "no ledger", "missing ledger"} {
		t.Run(state, func(t *testing.T) {
			t.Setenv("OX_XDG_ENABLE", "1")
			t.Setenv("XDG_CACHE_HOME", t.TempDir())
			project, ledger := t.TempDir(), t.TempDir()
			runGit(t, project, "init")
			runGit(t, ledger, "init")
			require.NoError(t, os.WriteFile(filepath.Join(ledger, ".git", "info", "exclude"), []byte(".sageox/\n"), 0600))
			require.NoError(t, os.MkdirAll(filepath.Join(project, ".sageox"), 0755))
			if state != "no ledger" {
				require.NoError(t, config.SaveLocalConfig(project, &config.LocalConfig{Ledger: &config.LedgerConfig{Path: ledger}}))
				require.NoError(t, config.SaveProjectConfig(project, &config.ProjectConfig{RepoID: filepath.Base(ledger)}))
			}
			t.Chdir(project)
			switch state {
			case "missing ledger":
				require.NoError(t, os.RemoveAll(ledger))
			case "incomplete":
				dir := filepath.Join(ledger, ".sageox", "cache", "sessions", "2026-10-01T00-00-coworker-OxTest")
				require.NoError(t, os.MkdirAll(dir, 0755))
				writeTestRawJSONLWithEntries(t, filepath.Join(dir, "raw.jsonl"), 2)
			case "scan error":
				require.NoError(t, os.WriteFile(filepath.Join(ledger, "sessions"), []byte("not a directory"), 0600))
			case "git error":
				require.NoError(t, os.WriteFile(filepath.Join(ledger, ".git", "index"), []byte("corrupt index"), 0600))
			case "pending commit":
				require.NoError(t, os.WriteFile(filepath.Join(ledger, "metadata.json"), []byte("{}"), 0600))
			case "recording recovery", "live recording", "live draft", "failed stop", "failed draft stop":
				dir := filepath.Join(ledger, ".sageox", "cache", "sessions", "2026-10-01T00-00-coworker-OxTest")
				require.NoError(t, os.MkdirAll(dir, 0755))
				writeTestRawJSONLWithEntries(t, filepath.Join(dir, "raw.jsonl"), 2)
				recording := session.RecordingState{AgentID: "OxTest", SessionPath: dir, ParentPID: 99999999, StartedAt: time.Now().Add(-48 * time.Hour)}
				if state != "recording recovery" {
					recording.ParentPID = os.Getpid()
				}
				if strings.Contains(state, "draft") {
					require.NoError(t, os.WriteFile(filepath.Join(dir, "meta.json"), []byte(`{"draft":true}`), 0600))
				}
				if strings.HasPrefix(state, "failed") {
					require.NoError(t, session.MarkExplicitStop(project, "OxTest"))
				}
				if state == "failed stop" {
					require.NoError(t, os.Remove(filepath.Join(dir, "raw.jsonl")))
				}
				data, err := json.Marshal(recording)
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(filepath.Join(dir, ".recording.json"), data, 0600))
			}
			require.NoError(t, doctor.SetNeedsDoctorAgent(project))
			before := snapshotDestination(t, ledger)
			var out bytes.Buffer
			require.NoError(t, runAgentDoctor(&out, &agentinstance.Instance{AgentID: "OxTest"}))
			pending := state != "complete" && !strings.HasPrefix(state, "live")
			assert.Equal(t, pending, doctor.NeedsDoctorAgent(project), "only unfinished work keeps its marker")
			if strings.HasPrefix(state, "failed") {
				var output AgentDoctorOutput
				require.NoError(t, json.Unmarshal(out.Bytes(), &output))
				assert.Equal(t, []string{"2026-10-01T00-00-coworker-OxTest"}, output.RecoverySessions)
				assert.Empty(t, output.IncompleteSessions)
				assert.True(t, session.HasExplicitStop(project, "OxTest"))
			}
			assert.Equal(t, before, snapshotDestination(t, ledger), "doctor must not repair recordings while inspecting")
			if state == "missing ledger" || state == "git error" {
				var output AgentDoctorOutput
				require.NoError(t, json.Unmarshal(out.Bytes(), &output))
				assert.False(t, output.Success)
				assert.False(t, checkSessionIncomplete(false).passed)
			}
			if state == "pending commit" {
				result := checkSessionIncomplete(false)
				assert.True(t, result.warning, "a pending commit must also keep human doctor from clearing the marker")
			}
		})
	}
}
