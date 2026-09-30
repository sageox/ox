package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// gitRepoWithPages makes a git repo with one committed page, then returns it.
func gitRepoWithPages(t *testing.T) (root, committed string) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	root = t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	gitConfig(t, root, "user.name", "Person A")
	gitConfig(t, root, "user.email", "person@test.sageox.ai")
	gitConfig(t, root, "commit.gpgsign", "false")
	committed = filepath.Join(root, "agents", "buzz", "index.html")
	authoredPage(t, committed)
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte(".context/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-q", "-m", "init")
	return root, committed
}

// TestFindUnsavedArtifacts_OnlySessionWritten verifies the artifact nudge
// only names pages something wrote since the last commit.
// Failure prevented: a fresh checkout/worktree (every tracked file gets a new
// mtime) made ox tell the agent "you authored agents/buzz/index.html".
func TestFindUnsavedArtifacts_OnlySessionWritten(t *testing.T) {
	root, committed := gitRepoWithPages(t)
	untracked := filepath.Join(root, "mockups", "new.html")
	authoredPage(t, untracked)
	ignored := filepath.Join(root, ".context", "review.html")
	authoredPage(t, ignored)

	tests := []struct {
		name   string
		mutate func(t *testing.T)
		want   map[string]bool // path -> expected present
	}{
		{
			name:   "tracked and unmodified is not the session's page",
			mutate: func(t *testing.T) {},
			want:   map[string]bool{committed: false, untracked: true, ignored: true},
		},
		{
			name: "tracked and modified is",
			mutate: func(t *testing.T) {
				f, err := os.OpenFile(committed, os.O_APPEND|os.O_WRONLY, 0)
				if err != nil {
					t.Fatal(err)
				}
				_, _ = f.WriteString("<p>edited</p>")
				_ = f.Close()
			},
			want: map[string]bool{committed: true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.mutate(t)
			got := map[string]bool{}
			for _, p := range findUnsavedArtifacts(root, time.Now(), time.Time{}) {
				got[p] = true
			}
			for path, want := range tt.want {
				abs, _ := normalizeArtifactPath(path)
				if got[abs] != want {
					t.Errorf("%s present=%v, want %v (got %v)", filepath.Base(path), got[abs], want, got)
				}
			}
		})
	}
}

// TestFindUnsavedArtifacts_SinceSessionStart verifies a page older than the
// session's prime is not reported as this session's work.
// Failure prevented: yesterday's untracked scratch page nagging today's session.
func TestFindUnsavedArtifacts_SinceSessionStart(t *testing.T) {
	root := t.TempDir()
	page := filepath.Join(root, "old.html")
	authoredPage(t, page)
	before := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(page, before, before); err != nil {
		t.Fatal(err)
	}
	if got := findUnsavedArtifacts(root, time.Now(), time.Now().Add(-time.Hour)); len(got) != 0 {
		t.Errorf("page modified before the session started was reported: %v", got)
	}
	if got := findUnsavedArtifacts(root, time.Now(), time.Time{}); len(got) != 1 {
		t.Errorf("without a session start the page is still found: %v", got)
	}
}

// TestWrittenPageFromToolInput pins which tool calls count as writing a page.
func TestWrittenPageFromToolInput(t *testing.T) {
	tests := []struct {
		tool, input, want string
	}{
		{"Write", `{"file_path":"/r/a.html","content":"x"}`, "/r/a.html"},
		{"Edit", `{"file_path":"/r/A.HTML"}`, "/r/A.HTML"},
		{"MultiEdit", `{"file_path":"/r/a.html"}`, "/r/a.html"},
		{"Write", `{"file_path":"/r/a.md"}`, ""},
		{"Read", `{"file_path":"/r/a.html"}`, ""},
		{"Write", `not json`, ""},
	}
	for _, tt := range tests {
		if got := writtenPageFromToolInput(tt.tool, []byte(tt.input)); got != tt.want {
			t.Errorf("%s %s = %q, want %q", tt.tool, tt.input, got, tt.want)
		}
	}
}

// TestEmitWrittenPageNudge_SameTurnOnce verifies the PostToolUse nudge emits a
// single Claude Code additionalContext JSON document, once per page.
// Failure prevented: the page nudge arriving only on the human's NEXT prompt,
// after the agent already said "done" — or a plain-text line Claude discards.
func TestEmitWrittenPageNudge_SameTurnOnce(t *testing.T) {
	root := t.TempDir()
	page := filepath.Join(root, "mockup.html")
	authoredPage(t, page)
	input := []byte(`{"file_path":"` + page + `"}`)

	var first, second bytes.Buffer
	if !emitWrittenPageNudge(&first, root, "Ox1", "Write", input) {
		t.Fatal("first write of an authored page must nudge")
	}
	var payload struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	dec := json.NewDecoder(&first)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&payload); err != nil {
		t.Fatalf("stdout is not the additionalContext envelope: %v", err)
	}
	if payload.HookSpecificOutput.HookEventName != "PostToolUse" ||
		!strings.Contains(payload.HookSpecificOutput.AdditionalContext, "Saved nothing yet") ||
		!strings.Contains(payload.HookSpecificOutput.AdditionalContext, "--kind mockup|review|plan") {
		t.Errorf("payload = %+v", payload)
	}
	if emitWrittenPageNudge(&second, root, "Ox1", "Edit", input) || second.Len() != 0 {
		t.Error("the same page must not nudge twice")
	}
}

// TestUnsavedPlanNudge_NeverForSavedPlans verifies the unsaved-plan stamp is
// never armed for, and never delivered about, a file inside the ledger.
// Failure prevented: the nudge telling an agent to save
// .../data/plans/<dir>/plan.md — a plan that only exists because it was saved.
func TestUnsavedPlanNudge_NeverForSavedPlans(t *testing.T) {
	root := newPlanCaptureTestRepo(t)
	ledgerPlan := filepath.Join(artifactLedgerRoot(root), "data", "plans", "2026-09-30-x", "plan.md")
	if artifactLedgerRoot(root) == "" {
		t.Fatal("test repo has no ledger path")
	}
	if !planSourceAlreadySaved(root, ledgerPlan) {
		t.Fatal("a ledger plan.md must count as already saved")
	}
	if planSourceAlreadySaved(root, filepath.Join(root, "plan.md")) {
		t.Fatal("a working-tree draft must not count as saved")
	}

	// A stamp armed before the check existed must be dropped at delivery.
	path := planUnsavedPath(root, "Ox1")
	st := unsavedPlanStamp{Topic: "x", SourcePath: ledgerPlan, Material: true, ArmedAt: time.Now().UTC()}
	data, _ := json.Marshal(st)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	emitUnsavedPlanNudge(&buf, root, "Ox1")
	if buf.Len() != 0 {
		t.Errorf("nudged about a saved plan: %q", buf.String())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("stamp for a saved plan must be cleared")
	}
}

// TestArmUnsavedPlanFromPrompt verifies Codex arms the unsaved-plan nudge from
// a plan-shaped prompt, and only then.
// Failure prevented: a Codex plan (no enrich, no ExitPlanMode) dying unsaved
// with no reminder at all.
func TestArmUnsavedPlanFromPrompt(t *testing.T) {
	tests := []struct {
		name   string
		prompt string
		want   bool
	}{
		{"plan mode", `{"prompt":"design the retry path","permission_mode":"plan"}`, true},
		{"html plan intent", `{"prompt":"make me an html plan for the rollout"}`, true},
		{"ordinary prompt", `{"prompt":"fix the flaky test"}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			armUnsavedPlanFromPrompt(root, "Oxcx", []byte(tt.prompt))
			st, ok := readUnsavedPlanStamp(planUnsavedPath(root, "Oxcx"))
			if ok != tt.want {
				t.Fatalf("armed=%v, want %v", ok, tt.want)
			}
			if ok && (!st.FromPrompt || strings.Contains(st.Topic, "retry")) {
				t.Errorf("stamp must be prompt-marked and must not store prompt text: %+v", st)
			}
			if ok && !strings.Contains(unsavedPlanNudgeLine(st), "ox plan save --file") {
				t.Errorf("nudge line = %q", unsavedPlanNudgeLine(st))
			}
		})
	}
}
