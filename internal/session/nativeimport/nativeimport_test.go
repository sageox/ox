package nativeimport

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	claudeID = "3f9a1c2b-7d4e-4a01-9c55-2b8e0f6d1a73"
	codexID  = "01a0ee54-4491-70e1-b771-5fbbead28fbd"
	sesURL   = "https://sageox.ai/c/ses_01a0e93a-ee60-75c8-9514-c1f09debf2ed"
)

func writeLines(t *testing.T, path string, lines ...string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644))
}

func TestNameAndSessionID(t *testing.T) {
	start := time.Date(2026, 9, 12, 7, 3, 59, 0, time.FixedZone("PDT", -7*3600))
	name := Name(AgentClaude, claudeID, start)
	assert.Equal(t, "2026-09-12T14-03-import-claude-"+claudeID, name, "UTC minute, then -import-<agent>-<native ID>")
	assert.Equal(t, "ses_1106b069-bd55-538d-9919-65adbb9337f2",
		SessionID("repo_0198a3c4-5e6f-7a8b-9c0d-1e2f3a4b5c6d", name),
		"the legacy UUIDv5 derivation over repo_id/name")
	assert.NotEqual(t, name, Name(AgentCodex, claudeID, start), "the agent is part of the name")
}

func TestParseMarkers(t *testing.T) {
	tests := []struct {
		name      string
		text      string
		want      []Marker
		sessionID string
		session   string
	}{
		{
			name:      "recording with a ses_ link",
			text:      `<ox-prime><session-context agent_id="Ox2Ake" status="fresh" recording="true" url="` + sesURL + `">`,
			want:      []Marker{{AgentID: "Ox2Ake", URL: sesURL}},
			sessionID: "ses_01a0e93a-ee60-75c8-9514-c1f09debf2ed",
		},
		{
			name:    "recording with a name link",
			text:    `<session-context agent_id="OxA1b2" url="https://sageox.ai/repo/repo_x/sessions/2026-04-01T10-00-lau-OxA1b2/view">`,
			want:    []Marker{{AgentID: "OxA1b2", URL: "https://sageox.ai/repo/repo_x/sessions/2026-04-01T10-00-lau-OxA1b2/view"}},
			session: "2026-04-01T10-00-lau-OxA1b2",
		},
		{
			name: "no url while not recording",
			text: `<session-context agent_id="Ox37JG" status="fresh" you="Lau">`,
			want: []Marker{{AgentID: "Ox37JG"}},
		},
		{name: "malformed agent id is ignored", text: `<session-context agent_id="OxPENDING">`},
		{name: "no tag", text: `agent_id="Ox2Ake"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseMarkers(tt.text)
			assert.Equal(t, tt.want, got)
			if len(got) > 0 {
				assert.Equal(t, tt.sessionID, got[0].SessionID())
				assert.Equal(t, tt.session, got[0].SessionName())
			}
		})
	}
}

func claudeLine(typ, extra string) string {
	return `{"type":"` + typ + `","sessionId":"` + claudeID + `","timestamp":"2026-09-12T14:03:10Z","cwd":"/work/repo","gitBranch":"main"` + extra + `}`
}

func TestInspectClaude(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "-work-repo")
	side := filepath.Join(dir, claudeID, "tool-results", "hook-1.txt")
	writeLines(t, side, `<session-context agent_id="OxSIDE" recording="true" url="`+sesURL+`">`)

	path := filepath.Join(dir, claudeID+".jsonl")
	writeLines(t, path,
		`{"type":"summary","summary":"no session id or timestamp"}`,
		claudeLine("attachment", `,"attachment":{"type":"hook_success","hookEvent":"SessionStart","content":"<session-context agent_id=\"OxHOOK\" recording=\"true\" url=\"`+sesURL+`\">"}`),
		claudeLine("attachment", `,"attachment":{"type":"hook_success","content":"Output too large. Full output saved to: `+side+`"}`),
		claudeLine("user", `,"message":{"content":"fix the login bug"}`),
		claudeLine("user", `,"isMeta":true,"message":{"content":"<system-reminder>meta</system-reminder>"}`),
		claudeLine("assistant", `,"message":{"content":[{"type":"tool_use","id":"toolu_1","input":{"command":"ox agent prime"}}]}`),
		claudeLine("user", `,"message":{"content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"<session-context agent_id=\"OxTOOL\">"}]}`),
		claudeLine("user", `,"message":{"content":[{"type":"tool_result","tool_use_id":"toolu_2","content":"<session-context agent_id=\"OxQUOT\">"}]}`),
		claudeLine("assistant", `,"message":{"content":[{"type":"text","text":"Found it."}]}`),
		claudeLine("assistant", `,"isSidechain":true,"message":{"content":[{"type":"text","text":"subagent"}]}`),
		`{"type":"user","sessionId":"`+claudeID+`","timestamp":"2026-09-12T15:20:00Z","cwd":"/work/repo/.claude/worktrees/x","message":{"content":"thanks"}}`,
	)

	s, err := InspectClaude(path)
	require.NoError(t, err)
	assert.Equal(t, claudeID, s.NativeID)
	assert.Equal(t, time.Date(2026, 9, 12, 14, 3, 10, 0, time.UTC), s.StartedAt)
	assert.Equal(t, time.Date(2026, 9, 12, 15, 20, 0, 0, time.UTC), s.LastActivity)
	assert.Equal(t, []string{"/work/repo", "/work/repo/.claude/worktrees/x"}, s.CWDs)
	assert.Equal(t, 2, s.Prompts, "meta and tool results are not prompts")
	assert.Equal(t, 1, s.Replies, "sidechain replies are not the session's own")
	agents := []string{}
	for _, m := range s.Markers {
		agents = append(agents, m.AgentID)
	}
	assert.ElementsMatch(t, []string{"OxHOOK", "OxSIDE", "OxTOOL"}, agents,
		"hook output, its side file and ox agent prime results count; a quoted tag does not")

	t.Run("a record from another session is refused", func(t *testing.T) {
		bad := filepath.Join(t.TempDir(), claudeID+".jsonl")
		writeLines(t, bad, claudeLine("user", `,"message":{"content":"hi"}`),
			`{"type":"user","sessionId":"00000000-0000-4000-8000-000000000000","timestamp":"2026-09-12T14:04:00Z"}`)
		_, err := InspectClaude(bad)
		require.ErrorContains(t, err, "another session")
	})
	t.Run("a side file outside the project folder is not read", func(t *testing.T) {
		outside := filepath.Join(t.TempDir(), "hook.txt")
		writeLines(t, outside, `<session-context agent_id="OxEVIL">`)
		p := filepath.Join(t.TempDir(), "proj", claudeID+".jsonl")
		writeLines(t, p, claudeLine("attachment", `,"attachment":{"content":"Full output saved to: `+outside+`"}`))
		s, err := InspectClaude(p)
		require.NoError(t, err)
		assert.Empty(t, s.Markers)
	})
}

func TestDiscoverClaude(t *testing.T) {
	root := t.TempDir()
	writeLines(t, filepath.Join(root, "-work-repo", claudeID+".jsonl"), "{}")
	writeLines(t, filepath.Join(root, "-work-repo", "agent-1a2b3c4d.jsonl"), "{}")
	writeLines(t, filepath.Join(root, "-work-repo", claudeID, "subagents", "agent-a.jsonl"), "{}")
	writeLines(t, filepath.Join(root, "-work-repo", claudeID, "subagents", "agent-b.jsonl"), "{}")

	got, err := DiscoverClaude(root)
	require.NoError(t, err)
	assert.Equal(t, []string{filepath.Join(root, "-work-repo", claudeID+".jsonl")}, got.Paths)
	assert.Equal(t, 3, got.Subagents)

	missing, err := DiscoverClaude(filepath.Join(root, "absent"))
	require.NoError(t, err)
	assert.Empty(t, missing.Paths)
}

func codexHeader(id, source string) string {
	return `{"type":"session_meta","timestamp":"2026-09-20T10:00:00.5Z","payload":{"id":"` + id + `","cwd":"/work/repo","source":` + source + `,"git":{"branch":"fix/lfs"}}}`
}

func TestInspectCodex(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sessions", "2026", "09", "20")
	path := filepath.Join(dir, "rollout-2026-09-20T10-00-00-"+codexID+".jsonl")
	prime := `<ox-prime><session-context agent_id=\"OxCDX1\" recording=\"true\" url=\"` + sesURL + `\"></ox-prime>`
	writeLines(t, path,
		codexHeader(codexID, `"vscode"`),
		`{"type":"turn_context","timestamp":"2026-09-20T10:00:01Z","payload":{"cwd":"/work/repo/.claude/worktrees/y"}}`,
		`{"type":"response_item","timestamp":"2026-09-20T10:00:02Z","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"<environment_context>cwd</environment_context>"}]}}`,
		`{"type":"response_item","timestamp":"2026-09-20T10:00:03Z","payload":{"type":"message","role":"developer","content":[{"type":"input_text","text":"`+prime+`"}]}}`,
		`{"type":"response_item","timestamp":"2026-09-20T10:00:04Z","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"why does the push fail?"}]}}`,
		`{"type":"event_msg","timestamp":"2026-09-20T10:00:05Z","payload":{"type":"task_started"}}`,
		`{"type":"response_item","timestamp":"2026-09-20T10:00:06Z","payload":{"type":"custom_tool_call","call_id":"c1","input":"ox agent prime"}}`,
		`{"type":"response_item","timestamp":"2026-09-20T10:00:07Z","payload":{"type":"custom_tool_call_output","call_id":"c1","output":"<session-context agent_id=\"OxCDX2\">"}}`,
		`{"type":"event_msg","timestamp":"2026-09-20T10:00:08Z","payload":{"type":"item_completed","item":{"type":"CommandExecution","command":["/bin/zsh","-lc","ox agent prime"],"aggregated_output":"<session-context agent_id=\"OxCDX3\">"}}}`,
		`{"type":"response_item","timestamp":"2026-09-20T10:00:09Z","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"quoting <session-context agent_id=\"OxQUOT\"> is not a marker"}]}}`,
		`{"type":"response_item","timestamp":"2026-09-20T10:00:10Z","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"The pointer is stale."}]}}`,
	)

	s, err := InspectCodex(path)
	require.NoError(t, err)
	assert.Equal(t, codexID, s.NativeID)
	assert.False(t, s.Internal)
	assert.True(t, s.InFlight, "a task started and never completed")
	assert.Equal(t, "fix/lfs", s.Branch)
	assert.Equal(t, []string{"/work/repo", "/work/repo/.claude/worktrees/y"}, s.CWDs)
	assert.Equal(t, 2, s.Prompts, "Codex's injected context messages are not prompts")
	assert.Equal(t, 1, s.Replies)
	agents := []string{}
	for _, m := range s.Markers {
		agents = append(agents, m.AgentID)
	}
	assert.ElementsMatch(t, []string{"OxCDX1", "OxCDX2", "OxCDX3"}, agents)

	refusals := []struct {
		name, first, file, want string
	}{
		{"header id must match the name", codexHeader("01a0ee54-4491-70e1-b771-000000000000", `"cli"`), codexID, "does not match"},
		{"first record must be session_meta", `{"type":"event_msg","timestamp":"2026-09-20T10:00:00Z","payload":{}}`, codexID, "lacks a session_meta"},
		{"unknown source is refused, not guessed", codexHeader(codexID, `{"future":{}}`), codexID, "unrecognized"},
	}
	for _, tt := range refusals {
		t.Run(tt.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "rollout-2026-09-20T10-00-00-"+tt.file+".jsonl")
			writeLines(t, p, tt.first)
			_, err := InspectCodex(p)
			require.ErrorContains(t, err, tt.want)
		})
	}
	for _, source := range []string{`{"subagent":{"other":"guardian"}}`, `{"subagent":{"thread_spawn":{"parent_thread_id":"x"}}}`} {
		t.Run("internal thread "+source, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "rollout-2026-09-20T10-00-00-"+codexID+".jsonl")
			writeLines(t, p, codexHeader(codexID, source))
			s, err := InspectCodex(p)
			require.NoError(t, err)
			assert.True(t, s.Internal)
		})
	}
}

func TestDiscoverCodex(t *testing.T) {
	home := t.TempDir()
	live := filepath.Join(home, "sessions", "2026", "09", "20", "rollout-a-"+codexID+".jsonl")
	archived := filepath.Join(home, "archived_sessions", "rollout-b-"+codexID+".jsonl")
	writeLines(t, live, "{}")
	writeLines(t, archived, "{}")
	writeLines(t, filepath.Join(home, "sessions", "history.jsonl"), "{}")
	require.NoError(t, os.Symlink(live, filepath.Join(home, "sessions", "rollout-link-"+codexID+".jsonl")))

	got, err := DiscoverCodex(home)
	require.NoError(t, err)
	assert.Equal(t, []string{archived, live}, got)
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
}

func TestScopeClassify(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	repo := filepath.Join(base, "repo")
	require.NoError(t, os.MkdirAll(repo, 0o755))
	git(t, repo, "init", "-q", "-b", "main")
	git(t, repo, "-c", "user.email=t@example.com", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "init")
	worktree := filepath.Join(base, "elsewhere", "wt")
	git(t, repo, "worktree", "add", "-q", worktree)
	gone := filepath.Join(base, "elsewhere", "gone")
	git(t, repo, "worktree", "add", "-q", gone)
	require.NoError(t, os.RemoveAll(gone)) // deleted without `git worktree remove`
	nested := filepath.Join(repo, "vendor", "other")
	require.NoError(t, os.MkdirAll(nested, 0o755))
	git(t, nested, "init", "-q")
	unrelated := filepath.Join(base, "unrelated")
	require.NoError(t, os.MkdirAll(unrelated, 0o755))

	scope, err := NewScope(worktree)
	require.NoError(t, err)
	tests := []struct {
		name string
		cwds []string
		want Membership
	}{
		{"main checkout and a subdirectory", []string{repo, filepath.Join(repo, "vendor")}, ThisRepo},
		{"a linked worktree", []string{worktree}, ThisRepo},
		{"a deleted but registered worktree", []string{filepath.Join(gone, "cmd")}, ThisRepo},
		{"a deleted directory under the main checkout", []string{filepath.Join(repo, ".claude", "worktrees", "old")}, ThisRepo},
		{"a nested repository is not this repository", []string{nested}, OtherRepo},
		{"an unrelated directory", []string{unrelated}, OtherRepo},
		{"a relative path proves nothing", []string{"repo"}, OtherRepo},
		{"this repo and another", []string{repo, unrelated}, MixedRepos},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, scope.Classify(tt.cwds))
		})
	}
}
