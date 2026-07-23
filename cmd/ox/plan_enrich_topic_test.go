package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/plan"
	"github.com/spf13/pflag"
)

// newPlanEnrichTestRepo creates a temp git repo and chdirs into it (findGitRoot
// resolves from cwd) — mirrors decision_test.go's newDecisionTestRepo. It also
// points HOME at an empty temp dir so plan.Resolve's ~/.claude/plans
// auto-discovery can never pick up a file from the real machine running the
// test (matching the t.Setenv("HOME", ...) pattern used throughout cmd/ox).
func newPlanEnrichTestRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	cmd := exec.Command("git", "init", "-q")
	cmd.Dir = root // never touch the real repo or global config
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	t.Setenv("HOME", t.TempDir())
	t.Chdir(root)
	return root
}

// runPlanEnrich drives planEnrichCmd.RunE directly with the given flag
// name/value pairs, resetting every touched flag afterward. --files is a
// StringSlice flag; pflag's stringSliceValue.Set APPENDS after the first
// change (by design, so `--files a --files b` accumulates on one command
// line), so a plain Set("files", "") in cleanup would not clear a prior call's
// value — only the pflag.SliceValue.Replace(nil) escape hatch actually resets
// the backing slice to empty.
func runPlanEnrich(t *testing.T, args ...string) string {
	t.Helper()
	cmd := planEnrichCmd
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetIn(strings.NewReader("")) // not a TTY, but empty
	t.Cleanup(func() {
		_ = cmd.Flags().Set("topic", "")
		_ = cmd.Flags().Set("file", "")
		_ = cmd.Flags().Set("text", "false")
		_ = cmd.Flags().Set("persist", "false")
		if sv, ok := cmd.Flags().Lookup("files").Value.(pflag.SliceValue); ok {
			_ = sv.Replace(nil)
		}
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

func TestPlanEnrichCmd_TopicJSON(t *testing.T) {
	newPlanEnrichTestRepo(t)

	out := runPlanEnrich(t, "topic", "auth token refresh flow")
	var res plan.Result
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if res.Guidance == "" {
		t.Error("guidance empty")
	}
	if len(res.DiagramHints) != 0 || len(res.VizHints) != 0 || res.MockupSection != "" {
		t.Errorf("topic-only result must carry no doc-structural fields: %+v", res)
	}
}

func TestPlanEnrichCmd_TopicWithFilesJSON(t *testing.T) {
	newPlanEnrichTestRepo(t)

	out := runPlanEnrich(t,
		"topic", "auth token refresh flow",
		"files", "internal/auth/token.go,internal/auth/refresh.go")
	var res plan.Result
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if res.Signals.Files != 2 {
		t.Errorf("expected Signals.Files=2 from --files, got %d (%+v)", res.Signals.Files, res.Signals)
	}
}

// TestPlanEnrichCmd_TopicBeatsFile verifies the documented precedence order
// (topic > file > stdin) at the CLI layer, mirroring decision's own contract.
func TestPlanEnrichCmd_TopicBeatsFile(t *testing.T) {
	root := newPlanEnrichTestRepo(t)
	p := filepath.Join(root, "ignored.md")
	if err := os.WriteFile(p, []byte("## Ignored Heading\nshould not be read when --topic is set"), 0o644); err != nil {
		t.Fatal(err)
	}

	out := runPlanEnrich(t, "topic", "my consult subject", "file", p)
	var res plan.Result
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if strings.Contains(out, "Ignored Heading") {
		t.Errorf("--topic must win over --file, but file content leaked into the result:\n%s", out)
	}
}

func TestPlanEnrichCmd_NoInput_MentionsTopic(t *testing.T) {
	newPlanEnrichTestRepo(t)

	out := runPlanEnrich(t) // no topic, no file, empty stdin, no auto-discoverable plan
	if !strings.Contains(out, "No plan found") {
		t.Errorf("friendly no-input message missing:\n%s", out)
	}
	if !strings.Contains(out, `--topic "<subject>"`) {
		t.Errorf("no-input message must teach the pre-draft --topic pattern:\n%s", out)
	}
}

func TestPlanEnrichCmd_TopicText(t *testing.T) {
	newPlanEnrichTestRepo(t)

	out := runPlanEnrich(t, "topic", "auth token refresh flow", "text", "true")
	if strings.HasPrefix(strings.TrimSpace(out), "{") {
		t.Fatalf("--text emitted JSON:\n%s", out)
	}
	if !strings.Contains(out, "Plan signals:") {
		t.Errorf("human summary missing signals line:\n%s", out)
	}
}

// TestPlanEnrichCmd_FullDocPathStillWorks is the CLI-layer regression check:
// the pre-existing --file path (unaffected by ResolveInput's new topic/files
// parameters, which default to zero values here) must still enrich a real
// plan document exactly as before.
func TestPlanEnrichCmd_FullDocPathStillWorks(t *testing.T) {
	root := newPlanEnrichTestRepo(t)
	p := filepath.Join(root, "plan.md")
	body := "## Approach\nEdit `internal/auth/token.go` and `internal/auth/refresh.go`.\n\n" +
		"## Rollout\nRoll out in phases over two weeks."
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	out := runPlanEnrich(t, "file", p)
	var res plan.Result
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if res.Signals.Files != 2 {
		t.Errorf("expected Signals.Files=2 from the parsed doc, got %d", res.Signals.Files)
	}
	if res.Guidance == "" || !strings.Contains(res.Guidance, "Implementation notes") {
		t.Errorf("expected the full-doc two-layer authoring guidance (decision layer + Implementation notes), got %q", res.Guidance)
	}
}
