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

func TestHandleInfo_DeclaresSharedAgentSkillsTarget(t *testing.T) {
	info, err := handleInfo()
	if err != nil {
		t.Fatal(err)
	}
	if len(info.SkillTargets) != 1 || info.SkillTargets[0].Key != "agents-project" || info.SkillTargets[0].Root != ".agents/skills" {
		t.Fatalf("gemini skill targets = %#v, want shared agents-project target", info.SkillTargets)
	}
}

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
	args := []string{"read-from-offset", "--session-file", realFixture, "--offset", "0"}
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

func TestInstallSkills_WritesCanonicalAgentSkills(t *testing.T) {
	repo := t.TempDir()
	var out bytes.Buffer
	if err := adapterruntime.RunWithArgs(adapterConfig, []string{"install-skills", "--repo-root", repo, "--version", "1.0.0", "--skill", "ox-cli-consult"}, nil, &out); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(repo, ".agents", "skills", "ox-cli-consult", "SKILL.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(data), "---\nname: ox-cli-consult") {
		t.Fatal("installed skill did not retain canonical frontmatter")
	}
}
