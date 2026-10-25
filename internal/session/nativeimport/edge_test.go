package nativeimport

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Failure prevented: reading the wrong store, or none, when a coworker keeps
// Claude Code or Codex data outside the default location.
func TestStoreLocationsHonorTheirEnvironment(t *testing.T) {
	custom := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", custom)
	t.Setenv("CODEX_HOME", custom)
	dir, err := ClaudeProjectsDir()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(custom, "projects"), dir)
	home, err := CodexHome()
	require.NoError(t, err)
	assert.Equal(t, custom, home)

	userHome := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", "")
	t.Setenv("HOME", userHome)
	dir, err = ClaudeProjectsDir()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(userHome, ".claude", "projects"), dir)
	home, err = CodexHome()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(userHome, ".codex"), home)
}

// Failure prevented: a file that is not a native transcript read, and
// uploaded, as one.
func TestInspectRefusesWhatIsNotATranscript(t *testing.T) {
	const otherCodexID = "01a0ee54-4491-70e1-b771-3c5d2e8f9a10"
	rollout := func(dir, id string) string { return filepath.Join(dir, "rollout-2026-09-20T10-00-00-"+id+".jsonl") }
	tests := []struct {
		name    string
		inspect func(dir string) error
		want    string
	}{
		{"a Claude file not named for a session", func(dir string) error {
			p := filepath.Join(dir, "notes.jsonl")
			writeLines(t, p, "{}")
			_, err := InspectClaude(p)
			return err
		}, "not a session ID"},
		{"a Claude line that is not JSON", func(dir string) error {
			p := filepath.Join(dir, claudeID+".jsonl")
			writeLines(t, p, "not json")
			_, err := InspectClaude(p)
			return err
		}, "invalid JSON at line 1"},
		{"a Claude transcript with no usable timestamp", func(dir string) error {
			p := filepath.Join(dir, claudeID+".jsonl")
			writeLines(t, p, `{"type":"user","sessionId":"`+claudeID+`","timestamp":"yesterday","message":{"content":"hi"}}`)
			_, err := InspectClaude(p)
			return err
		}, "no timestamps"},
		{"a directory named like a transcript", func(dir string) error {
			p := filepath.Join(dir, claudeID+".jsonl")
			require.NoError(t, os.Mkdir(p, 0o755))
			_, err := InspectClaude(p)
			return err
		}, "not a regular file"},
		{"a Codex rollout not named for a session", func(dir string) error {
			p := filepath.Join(dir, "rollout-2026-09-20T10-00-00.jsonl")
			writeLines(t, p, codexHeader(codexID, `"cli"`))
			_, err := InspectCodex(p)
			return err
		}, "no session ID"},
		{"a Codex line that is not JSON", func(dir string) error {
			p := rollout(dir, codexID)
			writeLines(t, p, codexHeader(codexID, `"cli"`), "not json")
			_, err := InspectCodex(p)
			return err
		}, "invalid JSON at line 2"},
		{"a Codex rollout with no timestamps", func(dir string) error {
			p := rollout(dir, otherCodexID)
			writeLines(t, p, `{"type":"session_meta","payload":{"id":"`+otherCodexID+`","source":"cli"}}`)
			_, err := InspectCodex(p)
			return err
		}, "no timestamps"},
		{"a Codex source this ox does not know", func(dir string) error {
			p := rollout(dir, codexID)
			writeLines(t, p, codexHeader(codexID, `"desktop-v9"`))
			_, err := InspectCodex(p)
			return err
		}, `unrecognized Codex session source "desktop-v9"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.ErrorContains(t, tt.inspect(t.TempDir()), tt.want)
		})
	}
}

// Failure prevented: a Codex thread misfiled as the coworker's history, or
// the coworker's history dropped as a thread, for a source shape that is not
// the common string form.
func TestCodexSourceShapes(t *testing.T) {
	tests := []struct {
		name, header string
		internal     bool
		wantErr      string
	}{
		{"a rollout from before the field existed", `{"type":"session_meta","timestamp":"2026-09-20T10:00:00Z","payload":{"id":"` + codexID + `","cwd":"/work/repo"}}`, false, ""},
		{"a custom client", codexHeader(codexID, `{"custom":"my-editor"}`), false, ""},
		{"an internal thread", codexHeader(codexID, `{"internal":{}}`), true, ""},
		{"an ambiguous tagged source", codexHeader(codexID, `{"subagent":{},"custom":"x"}`), false, "unrecognized Codex session source"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "rollout-2026-09-20T10-00-00-"+codexID+".jsonl")
			writeLines(t, p, tt.header)
			s, err := InspectCodex(p)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.internal, s.Internal)
		})
	}
}

// Failure prevented: an older Codex rollout, which logs the conversation only
// as events, judged empty and never imported; or an aborted turn left "in
// progress" forever.
func TestInspectCodexCountsEventOnlyConversations(t *testing.T) {
	p := filepath.Join(t.TempDir(), "rollout-2026-09-20T10-00-00-"+codexID+".jsonl")
	writeLines(t, p,
		codexHeader(codexID, `"exec"`),
		`{"type":"event_msg","timestamp":"2026-09-20T10:00:01Z","payload":{"type":"task_started"}}`,
		`{"type":"event_msg","timestamp":"2026-09-20T10:00:02Z","payload":{"type":"user_message","message":"why is CI red?"}}`,
		`{"type":"event_msg","timestamp":"2026-09-20T10:00:03Z","payload":{"type":"agent_message","message":"A flaky lock test."}}`,
		`{"type":"event_msg","timestamp":"2026-09-20T10:00:04Z","payload":{"type":"turn_aborted"}}`,
	)
	s, err := InspectCodex(p)
	require.NoError(t, err)
	assert.Equal(t, 1, s.Prompts)
	assert.Equal(t, 1, s.Replies)
	assert.True(t, s.HasConversation())
	assert.Equal(t, 2, s.Messages())
	assert.False(t, s.InFlight, "an aborted turn is over")
}

// Failure prevented: counting records that are not the coworker's own
// conversation, or failing a whole transcript over one odd record.
func TestInspectClaudeSkipsWhatIsNotTheConversation(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "-work-repo")
	path := filepath.Join(dir, claudeID+".jsonl")
	missing := filepath.Join(dir, claudeID, "tool-results", "gone.txt")
	writeLines(t, path,
		claudeLine("user", `,"isCompactSummary":true,"message":{"content":"summary of earlier work"}`),
		claudeLine("user", `,"message":"not an object"`),
		claudeLine("user", `,"message":{"content":5}`),
		claudeLine("user", `,"isMeta":true,"message":{"content":[{"type":"text","text":"injected"}]}`),
		claudeLine("user", `,"message":{"content":[{"type":"text","text":"rename the flag"}]}`),
		claudeLine("assistant", `,"message":"not an object"`),
		claudeLine("assistant", `,"message":{"content":"plain"}`),
		claudeLine("assistant", `,"message":{"content":[{"type":"text","text":"Renamed."}]}`),
		claudeLine("attachment", `,"attachment":{"type":"hook_success","content":"no marker here"}`),
		claudeLine("attachment", `,"attachment":"a session-context mention in plain text"`),
		claudeLine("attachment", `,"attachment":{"content":"Full output saved to: `+missing+`"}`),
		`{"type":"user","sessionId":"`+claudeID+`","timestamp":"not a time","message":{"content":"thanks"}}`,
	)
	s, err := InspectClaude(path)
	require.NoError(t, err)
	assert.Equal(t, 2, s.Prompts, "only the coworker's own prompts count")
	assert.Equal(t, 1, s.Replies)
	assert.Empty(t, s.Markers, "a missing side file and a quoted mention are not markers")
}

// Failure prevented: a store that cannot be read reported as an empty one,
// which would hide the coworker's sessions from the preview.
func TestDiscoveryReportsAStoreItCannotRead(t *testing.T) {
	notADir := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(notADir, []byte("x"), 0o644))

	_, err := DiscoverClaude(notADir)
	require.Error(t, err)
	_, err = DiscoverCodex(notADir)
	require.Error(t, err)

	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "README"), []byte("x"), 0o644))
	writeLines(t, filepath.Join(root, "-work-repo", claudeID+".jsonl"), "{}")
	require.NoError(t, os.MkdirAll(filepath.Join(root, "-work-repo", "no-subagents"), 0o755))
	got, err := DiscoverClaude(root)
	require.NoError(t, err)
	assert.Len(t, got.Paths, 1, "a stray file beside the project folders is not a transcript")
	assert.Zero(t, got.Subagents)
}

// Failure prevented: a transcript with one very long record (a large tool
// result) cut short or refused.
func TestReadRecordsKeepsLongRecordsWhole(t *testing.T) {
	long := strings.Repeat("x", 200*1024)
	p := filepath.Join(t.TempDir(), "long.jsonl")
	writeLines(t, p, `{"a":"`+long+`"}`, `{"b":1}`)
	var lens []int
	_, err := readRecords(p, func(_ int, line []byte) error {
		lens = append(lens, len(line))
		return nil
	})
	require.NoError(t, err)
	require.Len(t, lens, 2)
	assert.Greater(t, lens[0], 200*1024)

	_, err = readRecords(filepath.Join(t.TempDir(), "absent.jsonl"), func(int, []byte) error { return nil })
	require.Error(t, err)
}

// Failure prevented: sessions of a deleted worktree, or of a submodule-style
// checkout, attributed to the wrong repository.
func TestScopeResolvesUnusualCheckouts(t *testing.T) {
	_, err := NewScope(t.TempDir())
	require.ErrorContains(t, err, "resolve git common dir", "a directory outside any repository has no scope")

	// A .git file that is not a gitdir pointer is not a checkout.
	odd := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(odd, ".git"), []byte("not a pointer\n"), 0o644))
	_, ok := commonDirFor(odd)
	assert.False(t, ok)

	// A relative gitdir with no commondir is its own common dir, as a
	// submodule's is.
	sub := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(sub, "modules", "lib"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sub, ".git"), []byte("gitdir: modules/lib\n"), 0o644))
	common, ok := commonDirFor(sub)
	require.True(t, ok)
	assert.Equal(t, resolvePath(filepath.Join(sub, "modules", "lib")), common)

	// A missing .git file target cannot be opened.
	gone := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(gone, ".git"), 0o755))
	require.NoError(t, os.Remove(filepath.Join(gone, ".git")))
	_, ok = commonDirFromFile(filepath.Join(gone, ".git"), gone)
	assert.False(t, ok)

	assert.Equal(t, string(filepath.Separator), resolvePath(string(filepath.Separator)))
}

// Failure prevented: a raw.jsonl written somewhere it was never meant to go.
func TestWriteRawRefusesAPathItCannotCreate(t *testing.T) {
	_, err := WriteRaw(filepath.Join(t.TempDir(), "missing", "raw.jsonl"), t.TempDir(), RawHeader{SessionID: "ses_x"}, nil)
	require.Error(t, err)
}
