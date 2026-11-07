package nativeimport

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	parentID      = "a1969243-7a0d-4f80-bc97-b71beefcb3e6"
	grandparentID = "045c4e3b-1162-45ed-8ece-f0217e4bcead"
)

// claudeTurns is one session's records as Claude Code writes them: ox's
// SessionStart hook output with that session's recording link, a prompt and a
// reply. A resumed transcript copies an earlier session's turns verbatim.
func claudeTurns(id, agentID, cwd, branch, day string) []string {
	rec := func(typ, at, extra string) string {
		return `{"type":"` + typ + `","sessionId":"` + id + `","timestamp":"` + day + `T` + at + `Z","cwd":"` + cwd +
			`","gitBranch":"` + branch + `"` + extra + `}`
	}
	return []string{
		rec("attachment", "09:00:00", `,"attachment":{"type":"hook_success","hookEvent":"SessionStart","content":"<session-context agent_id=\"`+agentID+`\" recording=\"true\" url=\"`+sesURL+`\">"}`),
		rec("user", "09:00:10", `,"message":{"content":"prompt of `+id+`"}`),
		rec("assistant", "09:01:00", `,"message":{"content":[{"type":"text","text":"reply of `+id+`"}]}`),
	}
}

// Claude Code writes a resumed or forked conversation to a new file named for
// the new session and opens it with a copy of the conversation it continues,
// still under that conversation's ID. The import must take only the file's own
// records, read from where they begin, and refuse a file that mixes sessions.
//
// Failure prevented: every resumed transcript refused as "belongs to another
// session", so it could never be imported; or, once accepted, the parent's
// turns uploaded a second time under the new session's name, its start date
// taken from the parent, and its ox link matching the parent's recording so
// the new session reads as already recorded (#1398's import half).
func TestInspectClaudeReadsOnlyTheRecordsOfTheSessionItIsNamedFor(t *testing.T) {
	const (
		repo = "/work/repo"
		old  = "/work/repo/.claude/worktrees/old"
	)
	own := claudeTurns(claudeID, "OxOWNS", repo, "main", "2026-09-12")
	parent := claudeTurns(parentID, "OxPRNT", old, "parent-branch", "2026-09-10")
	grandparent := claudeTurns(grandparentID, "OxGRND", old, "parent-branch", "2026-09-08")
	ownAt := func(typ, at, extra string) string {
		return `{"type":"` + typ + `","sessionId":"` + claudeID + `","timestamp":"2026-09-12T` + at + `Z","cwd":"` + repo +
			`","gitBranch":"main"` + extra + `}`
	}
	compacted := []string{
		ownAt("system", "10:00:00", `,"subtype":"compact_boundary"`),
		ownAt("user", "10:00:01", `,"isCompactSummary":true,"message":{"content":"summary of everything before"}`),
		ownAt("user", "10:05:00", `,"message":{"content":"and one more thing"}`),
		ownAt("assistant", "10:06:00", `,"message":{"content":[{"type":"text","text":"done"}]}`),
	}
	cat := func(blocks ...[]string) []string {
		var out []string
		for _, b := range blocks {
			out = append(out, b...)
		}
		return out
	}
	unnamed := func(lines []string) []string {
		out := make([]string, len(lines))
		for i, line := range lines {
			out[i] = strings.Replace(line, `"sessionId":"`+claudeID+`",`, "", 1)
		}
		return out
	}
	const (
		snapshot = `{"type":"file-history-snapshot","messageId":"m1"}`
		summary  = `{"type":"summary","summary":"no session id or timestamp"}`
	)

	tests := []struct {
		name      string
		inherited []string // the leading block the file opens with
		own       []string // the file's own records, from the first that names it
		prompts   int
		replies   int
		lastAt    time.Time
		err       string
	}{
		{name: "resumed: the parent's turns, then its own",
			inherited: cat(parent, []string{snapshot}), own: own,
			prompts: 1, replies: 1, lastAt: time.Date(2026, 9, 12, 9, 1, 0, 0, time.UTC)},
		{name: "resumed twice: each ancestor in turn, then its own",
			inherited: cat(grandparent, parent), own: own,
			prompts: 1, replies: 1, lastAt: time.Date(2026, 9, 12, 9, 1, 0, 0, time.UTC)},
		{name: "compacted after a resume",
			inherited: parent, own: cat(own, compacted),
			prompts: 2, replies: 2, lastAt: time.Date(2026, 9, 12, 10, 6, 0, 0, time.UTC)},
		{name: "never resumed: a leading record naming no session is its own",
			inherited: nil, own: cat([]string{summary}, own),
			prompts: 1, replies: 1, lastAt: time.Date(2026, 9, 12, 9, 1, 0, 0, time.UTC)},
		{name: "no record names a session: all of it is its own",
			inherited: nil, own: unnamed(own),
			prompts: 1, replies: 1, lastAt: time.Date(2026, 9, 12, 9, 1, 0, 0, time.UTC)},

		{name: "another session after its own", own: cat(own, parent),
			err: "record at line 4 belongs to another session"},
		{name: "an inherited ID again after its own", inherited: parent, own: cat(own, parent[1:2]),
			err: "record at line 7 belongs to another session"},
		{name: "only the inherited copy so far", inherited: parent,
			err: "holds only the conversation it continues"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "-work-repo", claudeID+".jsonl")
			writeLines(t, path, cat(tt.inherited, tt.own)...)

			s, err := InspectClaude(path)
			if tt.err != "" {
				require.ErrorContains(t, err, tt.err)
				return
			}
			require.NoError(t, err)

			offset := 0
			if len(tt.inherited) > 0 {
				offset = len(strings.Join(tt.inherited, "\n")) + 1
			}
			assert.EqualValues(t, offset, s.Offset, "the import reads from the first of the file's own records")
			assert.Equal(t, time.Date(2026, 9, 12, 9, 0, 0, 0, time.UTC), s.StartedAt, "it starts when its own turns do")
			assert.Equal(t, tt.lastAt, s.LastActivity)
			assert.Equal(t, tt.prompts, s.Prompts, "only its own prompts count")
			assert.Equal(t, tt.replies, s.Replies, "only its own replies count")
			assert.Equal(t, []string{repo}, s.CWDs, "only where it worked itself")
			assert.Equal(t, "main", s.Branch)
			var agents []string
			for _, m := range s.Markers {
				agents = append(agents, m.AgentID)
			}
			assert.Equal(t, []string{"OxOWNS"}, agents, "an earlier session's recording link is not this session's")
		})
	}
}
