// Package consultscan tags session turns where the agent consulted SageOx
// knowledge by READING a file out of the ledger or a team-context repo.
//
// This is the deterministic, grade-A half of the knowledge-flow instrumentation
// (epic ox-bcgb): the agent opening a file inside a SageOx repo IS a consulted
// indicator — provable from the recorded transcript, no LLM, no self-report.
// Each detected read becomes a turn-anchored `consulted` context-trace event
// (mechanism=retrieval) so recap can build a receipted chain from "you read the
// team's decision X in turn N" to the work that followed.
package consultscan

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/internal/session/contexttrace"
)

// Roots are the on-disk SageOx repo roots whose files, when read, count as
// consulting team knowledge. Ledger is this repo's ledger clone; TeamContext is
// one path per team-context repo the user belongs to.
type Roots struct {
	Ledger      string
	TeamContext []string
}

// Entry is the minimal raw.jsonl shape the scanner needs — the tool name and
// its (string-encoded) input. Callers pass entries in transcript order; the
// slice index is the turn the tag anchors to.
type Entry struct {
	ToolName  string
	ToolInput string
}

// readToolNames are the file-reading tools across the agents ox records. A read
// of a SageOx path via any of these is a consultation.
var readToolNames = map[string]bool{
	"Read":         true,
	"read_file":    true,
	"read_page":    false, // browser reads are not file reads
	"NotebookRead": true,
	"view":         true,
}

// shellToolNames are the tools that run a shell command — where an `ox`
// retrieval invocation shows up as the command text.
var shellToolNames = map[string]bool{
	"Bash": true, "shell": true, "run_command": true, "execute_command": true,
}

// retrievalCommands maps an `ox` retrieval invocation (as it appears in a shell
// command) to the kind of knowledge it pulls. Running one of these in a turn is
// a consultation, even though the specific result isn't known from the command.
var retrievalCommands = []struct{ prefix, refType string }{
	{"ox query", "query"},
	{"ox agent team-ctx", "team-context"},
	{"ox code search", "code"},
	{"ox decision enrich", "decision"},
}

// Scan returns a turn-anchored `consulted` event for every turn that pulled
// SageOx knowledge — a file read inside a SageOx root, or an `ox` retrieval
// command. Deterministic and pure: no I/O, no writes. The event's Seq is the
// entry's index in the session (raw.jsonl line order).
func Scan(entries []Entry, roots Roots) []contexttrace.Event {
	var events []contexttrace.Event
	for i, e := range entries {
		switch {
		case readToolNames[e.ToolName]:
			path := extractPath(e.ToolInput)
			if path == "" {
				continue
			}
			root, refType := classify(path, roots)
			if root == "" {
				continue
			}
			events = append(events, contexttrace.Event{
				Type:      contexttrace.EventConsulted,
				Source:    contexttrace.SourceOnDemand,
				Mechanism: contexttrace.MechanismRetrieval,
				Seq:       i,
				Ref:       relOrBase(root, path),
				RefType:   refType,
			})
		case shellToolNames[e.ToolName]:
			refType, query, ok := detectRetrievalCommand(e.ToolInput)
			if !ok {
				continue
			}
			events = append(events, contexttrace.Event{
				Type:      contexttrace.EventConsulted,
				Source:    contexttrace.SourceOnDemand,
				Mechanism: contexttrace.MechanismRetrieval,
				Seq:       i,
				RefType:   refType,
				Ref:       query,
				Query:     query,
			})
		}
	}
	return events
}

// detectRetrievalCommand reports whether a shell tool input runs an `ox`
// retrieval command, returning the knowledge kind and the query/subject text.
func detectRetrievalCommand(toolInput string) (refType, query string, ok bool) {
	cmd := extractCommand(toolInput)
	if cmd == "" {
		return "", "", false
	}
	for _, rc := range retrievalCommands {
		if idx := strings.Index(cmd, rc.prefix); idx >= 0 {
			rest := strings.TrimSpace(cmd[idx+len(rc.prefix):])
			return rc.refType, firstQuotedOrToken(rest), true
		}
	}
	return "", "", false
}

// extractCommand pulls the shell command out of a tool input (JSON object with a
// "command"/"cmd" key, or a bare command string).
func extractCommand(toolInput string) string {
	s := strings.TrimSpace(toolInput)
	if strings.HasPrefix(s, "{") {
		var m map[string]any
		if err := json.Unmarshal([]byte(s), &m); err == nil {
			for _, k := range []string{"command", "cmd"} {
				if v, ok := m[k].(string); ok && v != "" {
					return v
				}
			}
			return ""
		}
	}
	return s
}

// firstQuotedOrToken returns the query/subject a retrieval command targets: the
// first quoted string anywhere in s (queries are usually quoted, and may sit
// after flags like --topic), else the first non-flag token. Capped so a receipt
// stays short.
func firstQuotedOrToken(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	for _, q := range []byte{'"', '\''} {
		if i := strings.IndexByte(s, q); i >= 0 {
			if end := strings.IndexByte(s[i+1:], q); end >= 0 {
				return truncateQuery(s[i+1 : i+1+end])
			}
		}
	}
	for _, tok := range strings.Fields(s) {
		if !strings.HasPrefix(tok, "-") {
			return truncateQuery(tok)
		}
	}
	return truncateQuery(strings.Fields(s)[0])
}

func truncateQuery(s string) string {
	const max = 120
	if len(s) > max {
		return s[:max]
	}
	return s
}

// ScanRawFile reads a session's raw.jsonl and returns the consulted events for
// every SageOx file read in it. Turn (Seq) is the line index. A missing file
// yields no events and no error — fail-open, like the rest of the trace layer.
// Malformed lines are skipped but still advance the turn index, so seqs stay
// aligned with the transcript.
func ScanRawFile(rawPath string, roots Roots) ([]contexttrace.Event, error) {
	f, err := os.Open(rawPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var entries []Entry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024) // transcript lines can be large
	for sc.Scan() {
		var e struct {
			ToolName  string `json:"tool_name"`
			ToolInput string `json:"tool_input"`
		}
		// Unparseable lines still occupy a turn slot so seqs stay aligned.
		_ = json.Unmarshal(sc.Bytes(), &e)
		entries = append(entries, Entry{ToolName: e.ToolName, ToolInput: e.ToolInput})
	}
	return Scan(entries, roots), nil
}

// TagSessionReads scans a session's raw.jsonl for SageOx file reads and appends
// the resulting turn-anchored consulted events to the session's
// context-trace.jsonl. This is the finalize-time home for the file-read tagger:
// at session stop the local raw.jsonl holds real content, so the scan is a pure
// local read. Best-effort and idempotency-agnostic (callers run it once at
// finalize); returns the number of turns tagged.
func TagSessionReads(rawPath, sessionDir string, roots Roots) (int, error) {
	events, err := ScanRawFile(rawPath, roots)
	if err != nil || len(events) == 0 {
		return 0, err
	}
	w := contexttrace.NewWriter(sessionDir)
	n := 0
	for _, ev := range events {
		if err := w.Append(ev); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// extractPath pulls the target file path out of a tool input. Tool inputs are
// recorded as strings; most are JSON objects carrying file_path/path/
// notebook_path, but some agents record the bare path — both are handled.
func extractPath(toolInput string) string {
	s := strings.TrimSpace(toolInput)
	if s == "" {
		return ""
	}
	if strings.HasPrefix(s, "{") {
		var m map[string]any
		if err := json.Unmarshal([]byte(s), &m); err == nil {
			for _, k := range []string{"file_path", "path", "notebook_path", "filePath"} {
				if v, ok := m[k].(string); ok && v != "" {
					return v
				}
			}
			return "" // JSON object with no recognizable path key
		}
	}
	return s // bare path
}

// classify returns the SageOx root a path lives under and the knowledge kind it
// represents (for the event's RefType), or "" when the path is outside every
// root. Ledger reads are sessions; team-context reads are classified by subpath
// (discussion / doc), defaulting to doc.
func classify(path string, roots Roots) (root, refType string) {
	clean := filepath.Clean(path)
	if roots.Ledger != "" && underRoot(clean, roots.Ledger) {
		return roots.Ledger, ledgerRefType(clean, roots.Ledger)
	}
	for _, tc := range roots.TeamContext {
		if tc != "" && underRoot(clean, tc) {
			return tc, teamContextRefType(clean, tc)
		}
	}
	return "", ""
}

// ledgerRefType classifies a read inside the ledger. Session recordings live
// under sessions/; everything else (plans, data) is a doc for now.
func ledgerRefType(path, ledger string) string {
	rel := relOrBase(ledger, path)
	switch {
	case strings.HasPrefix(rel, "sessions/"):
		return "session"
	case strings.HasPrefix(rel, "data/plans/"):
		return "plan"
	default:
		return "doc"
	}
}

// teamContextRefType classifies a read inside a team-context repo by subpath.
func teamContextRefType(path, tc string) string {
	rel := relOrBase(tc, path)
	switch {
	case strings.HasPrefix(rel, "discussions/"):
		return "discussion"
	case strings.Contains(rel, "memory/") || strings.HasPrefix(rel, "agent-context/"):
		return "kb"
	default:
		return "doc"
	}
}

// underRoot reports whether path is inside root (root itself does not count —
// only files under it), using a boundary-safe prefix so /a/b never matches
// /a/bc.
func underRoot(path, root string) bool {
	root = filepath.Clean(root)
	if path == root {
		return false
	}
	return strings.HasPrefix(path, root+string(filepath.Separator))
}

// relOrBase returns path relative to root, or the base name if it can't be made
// relative — so the receipt stays readable and never leaks an absolute path.
func relOrBase(root, path string) string {
	if rel, err := filepath.Rel(root, path); err == nil && !strings.HasPrefix(rel, "..") {
		return rel
	}
	return filepath.Base(path)
}
