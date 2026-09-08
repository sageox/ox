package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/sageox/ox/pkg/adapterruntime"
)

// TestReadFromOffset_WiredInOneShotMode drives read-from-offset through the
// real CLI dispatch path (adapterruntime.RunWithArgs against adapterConfig,
// exactly what os.Args[1:] does in main), not the handler function directly.
// A binary declaring adapterprotocol.CapIncrementalReader answered every
// one-shot read-from-offset call with {"error":"read-from-offset not
// implemented"} because Config.ReadFromOffset was never set — the daemon's
// catch-up read on restart (internal/daemon/agentwork/session_watcher.go)
// hit exactly this path and silently dropped every turn written since the
// last persisted offset.
func TestReadFromOffset_WiredInOneShotMode(t *testing.T) {
	var buf bytes.Buffer
	args := []string{"read-from-offset", "--session-file", fixtureTranscript, "--offset", "0"}
	if err := adapterruntime.RunWithArgs(adapterConfig, args, nil, &buf); err != nil {
		t.Fatalf("read-from-offset one-shot dispatch failed: %v (output: %s)", err, buf.String())
	}
	if strings.Contains(buf.String(), "not implemented") {
		t.Fatalf("read-from-offset returned %q — Config.ReadFromOffset is not wired in main.go", buf.String())
	}

	var result adapterprotocol.ReadFromOffsetResult
	if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
		t.Fatalf("decode read-from-offset result: %v (raw: %s)", err, buf.String())
	}
	if len(result.Entries) == 0 {
		t.Fatal("read-from-offset returned zero entries from a real transcript")
	}
	if result.NewOffset <= 0 {
		t.Fatalf("new_offset = %d, want > 0", result.NewOffset)
	}
}

func TestInfoDeclaresOMPInstalledSurfaces(t *testing.T) {
	info, err := handleInfo()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		adapterprotocol.CapSessionReader:     true,
		adapterprotocol.CapHookInstaller:     true,
		adapterprotocol.CapSkillsInstaller:   true,
		adapterprotocol.CapIncrementalReader: true,
		adapterprotocol.CapFileWatcher:       true,
		adapterprotocol.CapSessionImporter:   true,
		adapterprotocol.CapServeMode:         true,
	}
	if len(info.Capabilities) != len(want) {
		t.Fatalf("capabilities = %v, want %v", info.Capabilities, want)
	}
	for _, capability := range info.Capabilities {
		if !want[capability] {
			t.Errorf("unexpected capability %q", capability)
		}
	}
	if len(info.SkillTargets) != 1 ||
		info.SkillTargets[0].Key != "agents-project" ||
		info.SkillTargets[0].Root != ".agents/skills" {
		t.Fatalf("skill targets = %#v, want shared agents-project target", info.SkillTargets)
	}
}

func TestInstallSkillsWritesPortableAgentSkills(t *testing.T) {
	repo := t.TempDir()
	var out bytes.Buffer
	args := []string{"install-skills", "--repo-root", repo, "--version", "1.0.0"}
	if err := adapterruntime.RunWithArgs(adapterConfig, args, nil, &out); err != nil {
		t.Fatalf("install-skills failed: %v (output: %s)", err, out.String())
	}
	path := filepath.Join(repo, ".agents", "skills", "ox-cli-plan", "SKILL.md")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("OMP skill was not installed at %s: %v", path, err)
	}
}

func TestImportSessionResolvesTimestampPrefixedID(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	clearOMPPathEnv(t)
	direct := filepath.Join(home, "sessions")
	t.Setenv("PI_CODING_AGENT_SESSION_DIR", direct)
	if err := os.MkdirAll(direct, 0o755); err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(home, "project")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	id := "01a010a6-0d3d-7000-983a-8d87e5a3d151"
	path := filepath.Join(direct, "2026-08-17T16-55-12-957Z_"+id+".jsonl")
	writeOMPSession(t, path, repo, "imported turn")

	result, err := handleImportSession(adapterprotocol.ImportSessionParams{
		RepoRoot:  repo,
		SessionID: id,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Entries) != 1 || result.Entries[0].Content != "imported turn" {
		t.Fatalf("imported entries = %#v", result.Entries)
	}
}
