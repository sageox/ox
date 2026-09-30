// Package nativeimport finds the Claude Code and Codex sessions stored on this
// machine and derives the identity ox uses to import each one exactly once.
//
// Discovery reads native files structurally: identity, timestamps, working
// directories, counts and ox's own session markers. It never keeps prompt or
// tool content; conversion into raw.jsonl goes through the session adapters.
package nativeimport

import (
	"regexp"
	"sort"
	"strings"
	"time"
)

// Agent names the native tool a session came from. The value is also the
// agent segment of an imported session's Ledger name.
type Agent string

const (
	AgentClaude Agent = "claude"
	AgentCodex  Agent = "codex"
)

// maxRecordBytes bounds one JSONL record. Native records can carry large tool
// results, so the limit is generous; it only stops unbounded allocation on a
// corrupt file.
const maxRecordBytes = 64 * 1024 * 1024

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// IsNativeID reports whether id is a canonical lowercase UUID, the only form
// Claude Code and Codex use for session IDs.
func IsNativeID(id string) bool { return uuidPattern.MatchString(id) }

// Session is one native session file, read structurally.
type Session struct {
	Agent    Agent
	NativeID string // lowercase UUID; the filename and every record agree on it
	Path     string
	Size     int64
	ModTime  time.Time

	StartedAt    time.Time // first timestamped record, UTC
	LastActivity time.Time // latest timestamped record, UTC

	CWDs   []string // every working directory the session used
	Branch string   // last git branch the native tool recorded, if any

	Prompts int // user prompts
	Replies int // assistant text replies

	InFlight bool // Codex: a turn started and has not completed
	Internal bool // Codex: a subagent or internal thread, not user history

	Markers []Marker // ox's own session-context markers, in file order
}

// HasConversation reports whether the session holds at least one prompt and
// one reply. A transcript without both is never imported (#1106).
func (s Session) HasConversation() bool { return s.Prompts > 0 && s.Replies > 0 }

// Messages is the prompt and reply count shown in the preview.
func (s Session) Messages() int { return s.Prompts + s.Replies }

type cwdSet map[string]struct{}

func (c cwdSet) add(dir string) {
	dir = strings.TrimSpace(dir)
	if dir != "" {
		c[dir] = struct{}{}
	}
}

func (c cwdSet) sorted() []string {
	out := make([]string, 0, len(c))
	for dir := range c {
		out = append(out, dir)
	}
	sort.Strings(out)
	return out
}

func (s *Session) observe(ts time.Time) {
	if ts.IsZero() {
		return
	}
	ts = ts.UTC()
	if s.StartedAt.IsZero() {
		s.StartedAt = ts
	}
	if ts.After(s.LastActivity) {
		s.LastActivity = ts
	}
}

func parseTimestamp(value string) time.Time {
	if value == "" {
		return time.Time{}
	}
	ts, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}
	}
	return ts
}
