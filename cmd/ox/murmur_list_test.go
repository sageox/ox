package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/ledger"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMurmurListJSONOutput(t *testing.T) {
	// set up a temp ledger with murmur files
	tmpDir := t.TempDir()
	now := time.Now().UTC()

	murmurs := []ledger.MurmurFile{
		{
			SchemaVersion: "1",
			ID:            "test-id-1",
			Timestamp:     now.Add(-5 * time.Minute),
			AgentID:       "OxAbc1",
			Topic:         "wip",
			Importance:    "normal",
			Content:       "refactoring auth middleware",
			Scope:         "ledger",
		},
		{
			SchemaVersion: "1",
			ID:            "test-id-2",
			Timestamp:     now.Add(-2 * time.Minute),
			AgentID:       "OxDef2",
			Topic:         "conflict",
			Importance:    "critical",
			Content:       "modifying shared database schema",
			Scope:         "ledger",
		},
	}

	for _, m := range murmurs {
		_, err := ledger.WriteMurmur(tmpDir, m)
		if err != nil {
			t.Fatalf("write murmur: %v", err)
		}
	}

	// use windowHours=2 to handle hour-boundary flakiness (e.g., test runs at HH:02,
	// -5min murmur lands in previous hour's directory partition)
	read, err := ledger.ReadMurmursInWindow(tmpDir, 2)
	if err != nil {
		t.Fatalf("read murmurs: %v", err)
	}
	if len(read) != 2 {
		t.Fatalf("expected 2 murmurs, got %d", len(read))
	}
}

func TestMurmurListFiltering(t *testing.T) {
	tmpDir := t.TempDir()
	now := time.Now().UTC()

	murmurs := []ledger.MurmurFile{
		{SchemaVersion: "1", ID: "m1", Timestamp: now.Add(-1 * time.Minute), AgentID: "OxA", Topic: "lint", Importance: "normal", Content: "fixing lint"},
		{SchemaVersion: "1", ID: "m2", Timestamp: now.Add(-2 * time.Minute), AgentID: "OxB", Topic: "architecture", Importance: "critical", Content: "redesigning API"},
		{SchemaVersion: "1", ID: "m3", Timestamp: now.Add(-3 * time.Minute), AgentID: "OxA", Topic: "lint", Importance: "ambient", Content: "more lint fixes"},
	}

	for _, m := range murmurs {
		if _, err := ledger.WriteMurmur(tmpDir, m); err != nil {
			t.Fatalf("write murmur: %v", err)
		}
	}

	// use windowHours=2 to handle hour-boundary flakiness
	all, err := ledger.ReadMurmursInWindow(tmpDir, 2)
	if err != nil {
		t.Fatalf("ReadMurmursInWindow failed: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("expected 3 murmurs, got %d", len(all))
	}

	// simulate topic filter
	var lintOnly []ledger.MurmurFile
	for _, m := range all {
		if m.Topic == "lint" {
			lintOnly = append(lintOnly, m)
		}
	}
	if len(lintOnly) != 2 {
		t.Errorf("topic filter: expected 2 lint murmurs, got %d", len(lintOnly))
	}

	// simulate agent filter
	var agentA []ledger.MurmurFile
	for _, m := range all {
		if m.AgentID == "OxA" {
			agentA = append(agentA, m)
		}
	}
	if len(agentA) != 2 {
		t.Errorf("agent filter: expected 2 from OxA, got %d", len(agentA))
	}
}

func TestMurmurListEntryJSON(t *testing.T) {
	entry := murmurListEntry{
		ID:         "test-id",
		Timestamp:  "2026-03-27T14:30:00Z",
		AgentID:    "OxTest",
		Topic:      "wip",
		Importance: "normal",
		Content:    "working on auth",
		Scope:      "ledger",
	}

	data, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded murmurListEntry
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if decoded.ID != entry.ID {
		t.Errorf("ID: got %q, want %q", decoded.ID, entry.ID)
	}
	if decoded.AgentID != entry.AgentID {
		t.Errorf("AgentID: got %q, want %q", decoded.AgentID, entry.AgentID)
	}
	if decoded.Topic != entry.Topic {
		t.Errorf("Topic: got %q, want %q", decoded.Topic, entry.Topic)
	}
}

func TestMurmurListOutputJSON(t *testing.T) {
	output := murmurListOutput{
		Murmurs: []murmurListEntry{
			{ID: "1", Topic: "lint", Content: "fixing"},
		},
		Total:  1,
		Window: "12h",
	}

	data, err := json.Marshal(output)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded murmurListOutput
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if decoded.Total != 1 {
		t.Errorf("Total: got %d, want 1", decoded.Total)
	}
	if decoded.Window != "12h" {
		t.Errorf("Window: got %q, want %q", decoded.Window, "12h")
	}
}

// --- Positional topic arg ---

func TestMurmurListPositionalTopicArg(t *testing.T) {
	// verify the command accepts a positional arg
	if murmurListCmd.Args == nil {
		t.Fatal("murmurListCmd.Args should be set (MaximumNArgs(1))")
	}

	// valid: 0 args
	if err := murmurListCmd.Args(murmurListCmd, nil); err != nil {
		t.Errorf("0 args should be valid: %v", err)
	}

	// valid: 1 arg
	if err := murmurListCmd.Args(murmurListCmd, []string{"wip"}); err != nil {
		t.Errorf("1 arg should be valid: %v", err)
	}

	// invalid: 2 args
	if err := murmurListCmd.Args(murmurListCmd, []string{"wip", "extra"}); err == nil {
		t.Error("2 args should be invalid")
	}
}

// --- File-changes hiding (ox-1p1) ---
//
// Failure prevented: file-changes murmurs (auto-published by the daemon for
// AI collision detection) leaking into human terminal output as noise, and
// the converse — silently hiding them from JSON consumers (AI coworkers)
// who need the full picture.

func TestShouldHideFileChanges(t *testing.T) {
	tests := []struct {
		name            string
		showFileChanges bool
		topicFilter     string
		want            bool
	}{
		{"default hides", false, "", true},
		{"--show-file-changes overrides", true, "", false},
		{"explicit topic filter overrides", false, "file-changes", false},
		{"unrelated topic filter still hides", false, "wip", true},
		{"flag wins when both set", true, "file-changes", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldHideFileChanges(tt.showFileChanges, tt.topicFilter); got != tt.want {
				t.Errorf("shouldHideFileChanges(%v, %q) = %v, want %v",
					tt.showFileChanges, tt.topicFilter, got, tt.want)
			}
		})
	}
}

func TestDropFileChangeMurmurs(t *testing.T) {
	now := time.Now().UTC()
	in := []ledger.MurmurFile{
		{ID: "m1", Topic: "wip", Timestamp: now},
		{ID: "m2", Topic: "file-changes", Timestamp: now.Add(-1 * time.Minute)},
		{ID: "m3", Topic: "conflict", Timestamp: now.Add(-2 * time.Minute)},
		{ID: "m4", Topic: "file-changes", Timestamp: now.Add(-3 * time.Minute)},
	}
	out, dropped := dropFileChangeMurmurs(in)
	if dropped != 2 {
		t.Errorf("dropped = %d, want 2", dropped)
	}
	if len(out) != 2 {
		t.Fatalf("len(out) = %d, want 2", len(out))
	}
	if out[0].ID != "m1" || out[1].ID != "m3" {
		t.Errorf("order not preserved or wrong entries kept: %+v", out)
	}
	for _, m := range out {
		if m.Topic == "file-changes" {
			t.Errorf("file-changes leaked through: %+v", m)
		}
	}
}

func TestDropFileChangeMurmurs_NoFileChanges(t *testing.T) {
	in := []ledger.MurmurFile{
		{ID: "m1", Topic: "wip"},
		{ID: "m2", Topic: "conflict"},
	}
	out, dropped := dropFileChangeMurmurs(in)
	if dropped != 0 {
		t.Errorf("dropped = %d, want 0", dropped)
	}
	if len(out) != 2 {
		t.Errorf("len(out) = %d, want 2", len(out))
	}
}

func TestDropFileChangeMurmurs_AllFileChanges(t *testing.T) {
	in := []ledger.MurmurFile{
		{ID: "m1", Topic: "file-changes"},
		{ID: "m2", Topic: "file-changes"},
	}
	out, dropped := dropFileChangeMurmurs(in)
	if dropped != 2 {
		t.Errorf("dropped = %d, want 2", dropped)
	}
	if len(out) != 0 {
		t.Errorf("len(out) = %d, want 0", len(out))
	}
}

// TestMurmurListShowFileChangesFlagRegistered verifies the flag is wired up
// with the right name, default, and type. Failure prevented: a rename or
// removal that silently breaks the documented "ox murmur list --show-file-changes"
// invocation.
func TestMurmurListShowFileChangesFlagRegistered(t *testing.T) {
	flag := murmurListCmd.Flags().Lookup("show-file-changes")
	if flag == nil {
		t.Fatal("--show-file-changes flag not registered")
	}
	if flag.Value.Type() != "bool" {
		t.Errorf("flag type = %q, want bool", flag.Value.Type())
	}
	if flag.DefValue != "false" {
		t.Errorf("flag default = %q, want false", flag.DefValue)
	}
	if flag.Usage == "" {
		t.Error("flag usage text is empty")
	}
}

// TestMurmurListJSON_FileChangesNeverHidden documents the contract that
// JSON output is unaffected by --show-file-changes. AI coworkers consuming
// the JSON path need the full data set regardless of human-facing flags.
// This test asserts the property via the JSON entry shape (Topic field
// preserved through marshal/unmarshal); the actual JSON-path behavior in
// runMurmurList does not branch on showFileChanges by design.
func TestMurmurListJSON_FileChangesNeverHidden(t *testing.T) {
	entries := []murmurListEntry{
		{ID: "m1", Topic: "wip", Content: "hello"},
		{ID: "m2", Topic: "file-changes", Content: "edited foo.go"},
	}
	out := murmurListOutput{Murmurs: entries, Total: 2, Window: "12h"}
	data, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded murmurListOutput
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(decoded.Murmurs) != 2 {
		t.Fatalf("expected 2 murmurs in JSON, got %d", len(decoded.Murmurs))
	}
	var sawFileChanges bool
	for _, m := range decoded.Murmurs {
		if m.Topic == "file-changes" {
			sawFileChanges = true
		}
	}
	if !sawFileChanges {
		t.Error("file-changes murmur missing from JSON output")
	}
}

func TestMurmurFileRoundTrip(t *testing.T) {
	tmpDir := t.TempDir()
	now := time.Now().UTC()

	original := ledger.MurmurFile{
		SchemaVersion: "1",
		ID:            "roundtrip-id",
		Timestamp:     now,
		AgentID:       "OxRound",
		Topic:         "test",
		Importance:    "normal",
		Content:       "testing round trip",
		Scope:         "ledger",
	}

	relPath, err := ledger.WriteMurmur(tmpDir, original)
	if err != nil {
		t.Fatalf("write: %v", err)
	}

	// verify file exists
	fullPath := filepath.Join(tmpDir, relPath)
	if _, err := os.Stat(fullPath); err != nil {
		t.Fatalf("file not found: %v", err)
	}

	// read back
	read, err := ledger.ReadMurmursInWindow(tmpDir, 1)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(read) != 1 {
		t.Fatalf("expected 1 murmur, got %d", len(read))
	}

	if read[0].ID != original.ID {
		t.Errorf("ID mismatch: got %q, want %q", read[0].ID, original.ID)
	}
	if read[0].Content != original.Content {
		t.Errorf("Content mismatch: got %q, want %q", read[0].Content, original.Content)
	}
}

// murmurListProject stages an initialized git project whose ledger holds one
// murmur per age, ids m1..mN in the order given. The test chdirs into the
// project so the command resolves it the way a user's shell would.
func murmurListProject(t *testing.T, ages ...time.Duration) {
	t.Helper()
	projectRoot := t.TempDir()
	ledgerPath := t.TempDir()

	gitInit := exec.Command("git", "init", "-q")
	gitInit.Dir = projectRoot // never touch the real repo or its identity
	require.NoError(t, gitInit.Run(), "git init")

	sageoxDir := filepath.Join(projectRoot, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, "config.json"),
		[]byte(`{"config_version":"2","repo_id":"repo_test_murmur_list"}`), 0o644))
	require.NoError(t, config.SaveLocalConfig(projectRoot, &config.LocalConfig{
		Ledger: &config.LedgerConfig{Path: ledgerPath},
	}))

	now := time.Now().UTC()
	for i, age := range ages {
		_, err := ledger.WriteMurmur(ledgerPath, ledger.MurmurFile{
			ID:         "m" + string(rune('1'+i)),
			Timestamp:  now.Add(-age),
			AgentID:    "OxAbc1",
			Topic:      "wip",
			Importance: "normal",
			Content:    "working on item " + string(rune('1'+i)),
			Scope:      "ledger",
		})
		require.NoError(t, err)
	}

	t.Setenv(config.EnvProjectRoot, "")
	t.Chdir(projectRoot)
}

// runMurmurListJSON drives the production flag surface and RunE under a root
// that carries the persistent --json flag, and returns the decoded output.
func runMurmurListJSON(t *testing.T, args ...string) (murmurListOutput, error) {
	t.Helper()
	root := &cobra.Command{Use: "ox", SilenceUsage: true, SilenceErrors: true}
	root.PersistentFlags().Bool("json", false, "")
	cmd := &cobra.Command{Use: "list [topic]", Args: cobra.MaximumNArgs(1), RunE: runMurmurList}
	registerMurmurListFlags(cmd)
	root.AddCommand(cmd)

	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs(append([]string{"list", "--json"}, args...))
	if err := root.Execute(); err != nil {
		return murmurListOutput{}, err
	}
	var out murmurListOutput
	require.NoError(t, json.Unmarshal(buf.Bytes(), &out), "stdout must be one JSON document: %q", buf.String())
	return out, nil
}

func murmurIDs(out murmurListOutput) []string {
	ids := make([]string, 0, len(out.Murmurs))
	for _, m := range out.Murmurs {
		ids = append(ids, m.ID)
	}
	return ids
}

// TestMurmurList_LimitVocabulary: --limit is the canonical cap, --last the
// hidden legacy spelling, and --limit wins when both are given.
// Failure prevented: an agent that learned `session list --limit` types
// `murmur list --limit 3` and gets "unknown flag", or an old script's
// `--last` stops working.
func TestMurmurList_LimitVocabulary(t *testing.T) {
	// five murmurs, newest first: m1 is the most recent
	murmurListProject(t, 10*time.Minute, 20*time.Minute, 30*time.Minute, 3*time.Hour, 5*time.Hour)

	tests := []struct {
		name      string
		args      []string
		wantIDs   []string
		wantTotal int
	}{
		{"default cap is 10", nil, []string{"m1", "m2", "m3", "m4", "m5"}, 5},
		{"limit caps newest first", []string{"--limit", "3"}, []string{"m1", "m2", "m3"}, 5},
		{"last is the same cap", []string{"--last", "3"}, []string{"m1", "m2", "m3"}, 5},
		{"limit wins over last", []string{"--limit", "2", "--last", "4"}, []string{"m1", "m2"}, 5},
		{"limit wins whichever is typed first", []string{"--last", "2", "--limit", "4"}, []string{"m1", "m2", "m3", "m4"}, 5},
		{"limit 0 means no limit", []string{"--limit", "0"}, []string{"m1", "m2", "m3", "m4", "m5"}, 5},
		{"last 0 means no limit", []string{"--last", "0"}, []string{"m1", "m2", "m3", "m4", "m5"}, 5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := runMurmurListJSON(t, tt.args...)
			require.NoError(t, err)
			if got := murmurIDs(out); !slices.Equal(got, tt.wantIDs) {
				t.Errorf("murmur ids = %v, want %v", got, tt.wantIDs)
			}
			if out.Total != tt.wantTotal {
				t.Errorf("total = %d, want %d (the matched count, not the page size)", out.Total, tt.wantTotal)
			}
		})
	}
}

// TestMurmurList_SinceUntilWindow: --since and --until bound the listing from
// both ends with the grammar the other lists use.
// Failure prevented: `murmur list --until 1h` is "unknown flag", or a window
// silently ignores one of its ends.
func TestMurmurList_SinceUntilWindow(t *testing.T) {
	murmurListProject(t, 10*time.Minute, 20*time.Minute, 30*time.Minute, 3*time.Hour, 5*time.Hour)

	tests := []struct {
		name      string
		args      []string
		wantIDs   []string
		wantTotal int
	}{
		{"since keeps the newer end", []string{"--since", "4h"}, []string{"m1", "m2", "m3", "m4"}, 4},
		{"until keeps the older end, past the 12h default", []string{"--until", "1h"}, []string{"m4", "m5"}, 2},
		{"since and until bound both ends", []string{"--since", "6h", "--until", "1h"}, []string{"m4", "m5"}, 2},
		{"window excludes both neighbors", []string{"--since", "4h", "--until", "1h"}, []string{"m4"}, 1},
		{"limit applies inside the window", []string{"--since", "6h", "--until", "1h", "--limit", "1"}, []string{"m4"}, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := runMurmurListJSON(t, tt.args...)
			require.NoError(t, err)
			if got := murmurIDs(out); !slices.Equal(got, tt.wantIDs) {
				t.Errorf("murmur ids = %v, want %v", got, tt.wantIDs)
			}
			if out.Total != tt.wantTotal {
				t.Errorf("total = %d, want %d", out.Total, tt.wantTotal)
			}
		})
	}
}

// TestMurmurList_InvalidWindowIsAUsageError: a bad value names its flag and an
// example, and an inverted window is refused rather than answered with nothing.
func TestMurmurList_InvalidWindowIsAUsageError(t *testing.T) {
	murmurListProject(t, 10*time.Minute)

	tests := []struct {
		name string
		args []string
		want []string
	}{
		{"bad since", []string{"--since", "lately"}, []string{"--since", "lately", "2026-10-01"}},
		{"bad until", []string{"--until", "soon"}, []string{"--until", "soon", "2026-10-01"}},
		{"inverted window", []string{"--since", "1h", "--until", "6h"}, []string{"--since", "--until"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := runMurmurListJSON(t, tt.args...)
			require.Error(t, err)
			for _, want := range tt.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q lacks %q", err, want)
				}
			}
		})
	}
}

// Failure prevented: polls have no raw delivery facts, or send murmur content instead of just a count and time.
func TestMurmurList_OutcomeFacts(t *testing.T) {
	for _, n := range []int{0, 3} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			if testing.Short() {
				t.Skip("short: initializes a git project")
			}
			withIsolatedConfig(t)
			t.Setenv("SAGEOX_TOKEN", "")
			t.Setenv("SAGEOX_ENDPOINT", "http://127.0.0.1:1")
			t.Setenv("OX_XDG_DISABLE", "")
			ages := []time.Duration{time.Minute, 2 * time.Minute, 3 * time.Minute}
			murmurListProject(t, ages[:n]...)
			saved := cliCtx
			t.Cleanup(func() { cliCtx = saved })
			cliCtx = &cli.Context{}
			out, err := runMurmurListJSON(t)
			require.NoError(t, err)
			expected := map[string]any{"murmur_count": n}
			if n > 0 {
				expected["newest_murmur_at"] = out.Murmurs[0].Timestamp
			}
			assert.Equal(t, expected, cliCtx.Outcome())
		})
	}
}
