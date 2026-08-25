package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/cli"
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

func runDecisionEnrichResult(t *testing.T, args ...string) (string, error) {
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
		_ = cmd.Flags().Set("explain", "false")
	})
	for i := 0; i+1 < len(args); i += 2 {
		if err := cmd.Flags().Set(args[i], args[i+1]); err != nil {
			t.Fatal(err)
		}
	}
	err := cmd.RunE(cmd, nil)
	return out.String(), err
}

func runDecisionEnrich(t *testing.T, args ...string) string {
	t.Helper()
	out, err := runDecisionEnrichResult(t, args...)
	if err != nil {
		t.Fatalf("enrich RunE: %v", err)
	}
	return out
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

// writeADRFile drops a single Decision Record into a repo's docs/adr corpus.
func writeADRFile(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestDecisionEnrichCmd_FindsRelatedADRForLongTopic is the #823 proof at the real
// command surface: a long, real-world topic that contradicts an existing ADR must
// surface that ADR as a related decision through `ox decision enrich`. This is the
// silent failure the reporter hit — a full plan/issue body found nothing because
// the old scorer diluted relevance with query length. See also
// tests/acceptance/features/decision-records/consult-before-drafting.feature.
func TestDecisionEnrichCmd_FindsRelatedADRForLongTopic(t *testing.T) {
	root := newDecisionTestRepo(t, false) // git repo + chdir, no default corpus
	writeADRFile(t, root, "docs/adr/ADR-002-feature-flags.md",
		"# ADR-002: Feature flags are added only at explicit user request\n\n"+
			"**Status**: Accepted\n**Date**: 2026-02-01\n\n"+
			"## Context\n\nWe add a feature flag only when a coworker asks for one by "+
			"name — never speculatively for staged rollouts or kill switches.\n")

	longTopic := "we want to gate the new todo digest emailer behind a feature flag " +
		"so we can stage the rollout by percentage and keep a kill switch in case the " +
		"new sender misbehaves in production, adding two flag-shaped environment variables"

	out := runDecisionEnrich(t, "topic", longTopic)
	var res decision.Result
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	found := false
	for _, a := range res.Annotations {
		if a.Type == decision.BadgeRelatedDecision && a.Ref == "ADR-002" {
			found = true
		}
	}
	if !found {
		t.Errorf("#823: long topic did not surface the contradicting ADR-002 at the command surface: %s", out)
	}
	if res.Signals.Degraded {
		t.Errorf("a readable corpus must not report degraded: %s", out)
	}
}

// TestDecisionEnrichCmd_ExplainSurfacesDropped proves --explain exposes sub-floor
// candidates in the JSON so a caller can tell "nothing relevant" from "found and
// dropped" (#823 ask 4).
func TestDecisionEnrichCmd_ExplainSurfacesDropped(t *testing.T) {
	root := newDecisionTestRepo(t, false)
	writeADRFile(t, root, "docs/adr/ADR-070-widget.md",
		"# ADR-070: Widget Rendering Pipeline\n\n**Status**: Accepted\n**Date**: 2026-02-02\n\n"+
			"## Context\n\nThe kubernetes cluster hosts the renderer.\n")

	// "kubernetes" hits only the excerpt → scores below the floor → dropped.
	off := runDecisionEnrich(t, "topic", "kubernetes")
	var offRes decision.Result
	if err := json.Unmarshal([]byte(off), &offRes); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, off)
	}
	if len(offRes.Dropped) != 0 {
		t.Errorf("dropped must be empty without --explain: %+v", offRes.Dropped)
	}

	on := runDecisionEnrich(t, "topic", "kubernetes", "explain", "true")
	var onRes decision.Result
	if err := json.Unmarshal([]byte(on), &onRes); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, on)
	}
	if len(onRes.Dropped) == 0 || onRes.Dropped[0].Ref != "ADR-070" {
		t.Errorf("--explain should surface ADR-070 as a dropped candidate: %s", on)
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

func TestDecisionEnrichCmd_DegradedResultExitsNonZero(t *testing.T) {
	root := newDecisionTestRepo(t, false)
	writeADRFile(t, root, "docs/adr/deployment-strategy.md",
		"# Deployment Strategy\n\nProse without enough metadata to catalog this decision.\n")

	out, err := runDecisionEnrichResult(t, "topic", "deployment strategy")
	if !cli.IsSilent(err) {
		t.Fatalf("degraded retrieval must return a silent non-zero error, got %v", err)
	}
	var res decision.Result
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("degraded output must remain valid JSON: %v\n%s", err, out)
	}
	if !res.Signals.Degraded {
		t.Fatalf("malformed decision source must be marked degraded: %s", out)
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
