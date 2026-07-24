package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/decision"
)

// newDecisionTestRepo creates a temp git repo with a small DR corpus and
// chdirs into it (findGitRoot resolves from cwd).
func newDecisionTestRepo(t *testing.T, withCorpus bool) string {
	t.Helper()
	root := t.TempDir()
	cmd := exec.Command("git", "init", "-q")
	cmd.Dir = root // never touch the real repo or global config
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	if withCorpus {
		p := filepath.Join(root, "docs", "adr", "ADR-001-first-decision.md")
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		body := "# ADR-001: First Decision\n\n**Status**: Accepted\n**Date**: 2026-01-01\n\n## Context\n\nSomething about session streaming.\n"
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(root)
	return root
}

func runDecisionEnrich(t *testing.T, args ...string) string {
	t.Helper()
	cmd := decisionEnrichCmd
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetIn(strings.NewReader("")) // not a TTY, but empty
	t.Cleanup(func() {
		_ = cmd.Flags().Set("topic", "")
		_ = cmd.Flags().Set("file", "")
		_ = cmd.Flags().Set("text", "false")
	})
	for i := 0; i+1 < len(args); i += 2 {
		if err := cmd.Flags().Set(args[i], args[i+1]); err != nil {
			t.Fatal(err)
		}
	}
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("enrich RunE: %v", err)
	}
	return out.String()
}

func TestDecisionEnrichCmd_TopicJSON(t *testing.T) {
	newDecisionTestRepo(t, true)

	out := runDecisionEnrich(t, "topic", "session streaming decision")
	var res decision.Result
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if res.SchemaVersion != "v1" {
		t.Errorf("schema: %q", res.SchemaVersion)
	}
	if res.Conventions.NextNumber != 2 {
		t.Errorf("next number: %d", res.Conventions.NextNumber)
	}
	if res.Decision.SuggestedID != "ADR-002" {
		t.Errorf("suggested: %q", res.Decision.SuggestedID)
	}
	if res.Guidance == "" {
		t.Error("guidance empty")
	}
}

func TestDecisionEnrichCmd_Text(t *testing.T) {
	newDecisionTestRepo(t, true)

	out := runDecisionEnrich(t, "topic", "session streaming decision", "text", "true")
	if strings.HasPrefix(strings.TrimSpace(out), "{") {
		t.Fatalf("--text emitted JSON:\n%s", out)
	}
	if !strings.Contains(out, "Signals:") || !strings.Contains(out, "Guidance:") {
		t.Errorf("summary sections missing:\n%s", out)
	}
}

func TestDecisionEnrichCmd_NoInput(t *testing.T) {
	newDecisionTestRepo(t, true)

	out := runDecisionEnrich(t) // no topic, no file, empty stdin
	if !strings.Contains(out, "No input") {
		t.Errorf("friendly no-input message missing:\n%s", out)
	}
}

func TestWriteDecisionRecordGuidance_GatedOnCorpus(t *testing.T) {
	t.Run("corpus present", func(t *testing.T) {
		newDecisionTestRepo(t, true)
		var sb strings.Builder
		writeDecisionRecordGuidance(&sb)
		got := sb.String()
		if !strings.Contains(got, "<decision-record-guidance>") {
			t.Fatalf("block missing:\n%s", got)
		}
		// The citation-format example (`<!-- SOURCE: sageox ... -->`) moved to
		// `ox guide decision-records` when the prime preamble was compacted — the
		// block keeps every command + the verbatim-citation rule and routes the
		// format detail to the guide. Assert the surviving directives + that route.
		for _, want := range []string{"ox decision enrich --topic", "--file", "ox code search", "VERBATIM", "ox guide decision-records"} {
			if !strings.Contains(got, want) {
				t.Errorf("block missing %q", want)
			}
		}
		if strings.Contains(got, "<!-- SOURCE: sageox") {
			t.Error("block must not reintroduce the inline citation example; it now lives in `ox guide decision-records`")
		}
	})
	t.Run("no corpus", func(t *testing.T) {
		newDecisionTestRepo(t, false)
		var sb strings.Builder
		writeDecisionRecordGuidance(&sb)
		if sb.Len() != 0 {
			t.Fatalf("block emitted for DR-less repo:\n%s", sb.String())
		}
	})
	t.Run("config opt-out wins over corpus", func(t *testing.T) {
		root := newDecisionTestRepo(t, true)
		writeProjectDecisionEnrich(t, root, false)
		var sb strings.Builder
		writeDecisionRecordGuidance(&sb)
		if sb.Len() != 0 {
			t.Fatalf("block emitted despite decision.enrich=false:\n%s", sb.String())
		}
	})
}

// writeProjectDecisionEnrich writes a minimal committed config with the
// decision.enrich toggle set — priming must default ON (nil) and honor an
// explicit false.
func writeProjectDecisionEnrich(t *testing.T, root string, enabled bool) {
	t.Helper()
	cfgDir := filepath.Join(root, ".sageox")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := map[string]any{
		"config_version":         "2",
		"update_frequency_hours": 24,
		"decision":               map[string]any{"enrich": enabled},
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCheckDecisionPaths(t *testing.T) {
	t.Run("no config skips", func(t *testing.T) {
		newDecisionTestRepo(t, true)
		r := checkDecisionPaths()
		if !r.skipped {
			t.Errorf("want skipped without config: %+v", r)
		}
	})
	t.Run("typo path warns", func(t *testing.T) {
		root := newDecisionTestRepo(t, true)
		writeProjectDecisionConfig(t, root, []string{"docs/adr-typo"})
		r := checkDecisionPaths()
		if r.passed || !r.warning {
			t.Errorf("want warning for zero-match path: %+v", r)
		}
	})
	t.Run("valid config passes", func(t *testing.T) {
		root := newDecisionTestRepo(t, true)
		writeProjectDecisionConfig(t, root, []string{"docs/adr"})
		r := checkDecisionPaths()
		if !r.passed {
			t.Errorf("want pass: %+v", r)
		}
		if !strings.Contains(r.message, "1 decision record") {
			t.Errorf("message: %q", r.message)
		}
	})
}

func writeProjectDecisionConfig(t *testing.T, root string, paths []string) {
	t.Helper()
	cfgDir := filepath.Join(root, ".sageox")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := map[string]any{
		"config_version":         "2",
		"update_frequency_hours": 24,
		"decision":               map[string]any{"paths": paths},
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}
