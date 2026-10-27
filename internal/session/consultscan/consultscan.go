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
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/internal/session/contexttrace"
	"github.com/sageox/ox/internal/session/contextuse"
)

// Roots are the on-disk SageOx repo roots whose files, when read, count as
// consulting team knowledge. Ledger is this repo's ledger clone; TeamContext is
// one path per team-context repo the user belongs to.
type Roots struct {
	Ledger      string
	TeamContext []string
}

// Entry is the minimal raw.jsonl shape the scanner needs: the entry's role,
// its text (assistant output, for citation detection), and the tool name and
// (string-encoded) input for tool entries. Callers pass entries in transcript
// order; the slice index is the turn the tag anchors to.
//
// Seq frame: the index is the entry's position among NON-header entries, which
// is the same frame the other trace writers use (`state.EntryCount` counts
// captured entries, never the header/footer line). LoadRawEntries drops header
// lines so a raw.jsonl index and an EntryCount seq compare directly.
type Entry struct {
	Type      string // user|assistant|system|tool (empty when unknown)
	Content   string // message text; only read for assistant entries
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
		if call, ok := findOxCall(cmd, rc.prefix); ok {
			return rc.refType, firstQuotedOrToken(cmd[call.start:call.end]), true
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
// every SageOx file read in it. Turn (Seq) is the entry index in the
// EntryCount frame (see LoadRawEntries). A missing file yields no events and
// no error — fail-open, like the rest of the trace layer.
func ScanRawFile(rawPath string, roots Roots) ([]contexttrace.Event, error) {
	entries, err := LoadRawEntries(rawPath)
	if err != nil || len(entries) == 0 {
		return nil, err
	}
	return Scan(entries, roots), nil
}

// LoadRawEntries reads a session's raw.jsonl into Entries in the seq frame the
// rest of the trace uses.
//
// raw.jsonl opens with a header line ({"type":"header"} or a {"_meta":…}
// record) and may close with a footer; neither is an entry, and the whisper /
// invoked / lifecycle writers anchor their Seq to `state.EntryCount`, which
// counts captured entries only. So those lines are dropped here rather than
// occupying an index — the "seqOffset" is exactly the number of such lines
// before the first entry, and subtracting it is what makes a `used` event's
// "after the surfacing turn" comparison meaningful. Malformed lines ARE kept
// (as empty entries) because the capture path counted them, so they still
// hold their slot. A missing file yields nil, nil.
//
// It is LoadRawEntriesFrom from the start of the file.
func LoadRawEntries(rawPath string) ([]Entry, error) {
	entries, _, _, err := LoadRawEntriesFrom(rawPath, 0, 0)
	return entries, err
}

// LoadRawEntriesFrom reads the entries of a growing raw.jsonl that start at
// byte offset, in the same frame as LoadRawEntries: the first returned entry
// has index startIndex. It returns the offset and index to pass to the next
// call, so tailing a file in any number of calls numbers every entry exactly
// as one LoadRawEntries call over the finished file does.
//
// A last line with no newline yet is consumed only when it is already a
// complete JSON record; otherwise it is likely still being written and is left
// for the next call (nextOffset stops at its start). A missing file yields no
// entries, the given offset and index, and no error; an offset past the end
// yields nothing.
func LoadRawEntriesFrom(rawPath string, offset int64, startIndex int) (entries []Entry, nextOffset int64, nextIndex int, err error) {
	if offset < 0 {
		return nil, offset, startIndex, fmt.Errorf("consultscan: negative raw.jsonl offset %d", offset)
	}
	f, err := os.Open(rawPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, offset, startIndex, nil
		}
		return nil, offset, startIndex, err
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, offset, startIndex, err
	}

	nextOffset = offset
	r := bufio.NewReaderSize(f, 64*1024) // transcript lines can be large
	for {
		line, rerr := r.ReadBytes('\n')
		if rerr != nil && !errors.Is(rerr, io.EOF) {
			return entries, nextOffset, startIndex + len(entries), rerr
		}
		terminated := rerr == nil
		trimmed := bytes.TrimSpace(line)
		if !terminated && (len(trimmed) == 0 || !json.Valid(trimmed)) {
			break // unterminated and incomplete: wait for the rest
		}
		nextOffset += int64(len(line))
		if e, ok := parseRawLine(trimmed); ok {
			entries = append(entries, e)
		}
		if !terminated {
			break
		}
	}
	return entries, nextOffset, startIndex + len(entries), nil
}

// parseRawLine turns one trimmed raw.jsonl line into an Entry, reporting false
// for blank and framing lines, which hold no seq slot.
func parseRawLine(line []byte) (Entry, bool) {
	if len(line) == 0 {
		return Entry{}, false
	}
	var e struct {
		Type      string          `json:"type"`
		Content   json.RawMessage `json:"content"`
		ToolName  string          `json:"tool_name"`
		ToolInput json.RawMessage `json:"tool_input"`
		Meta      json.RawMessage `json:"_meta"`
	}
	// Unparseable lines still occupy a turn slot so seqs stay aligned.
	if err := json.Unmarshal(line, &e); err == nil && isHeaderLine(e.Type, e.Meta) {
		return Entry{}, false
	}
	return Entry{
		Type:      e.Type,
		Content:   rawString(e.Content),
		ToolName:  e.ToolName,
		ToolInput: rawString(e.ToolInput),
	}, true
}

// isHeaderLine reports whether a raw.jsonl record is framing (header/footer/
// _meta) rather than a captured entry.
func isHeaderLine(typ string, meta json.RawMessage) bool {
	return typ == "header" || typ == "footer" || len(meta) > 0
}

// rawString returns a JSON value as text: a JSON string decoded, anything
// else (an object tool input, an array content block) as its raw JSON.
func rawString(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return string(raw)
}

// TagSessionReads scans a session's raw.jsonl for SageOx file reads and
// evidence of use of already-surfaced context, and appends the resulting
// turn-anchored consulted / used events to the session's context-trace.jsonl.
// This is the finalize-time home for the deterministic tagger: at session stop
// the local raw.jsonl holds real content, so the scan is a pure local read.
//
// Idempotent: any (type, ref, evidence, seq) already in the trace is skipped,
// so a daemon re-finalize or a CLI retry never double-writes. Returns the
// number of events appended.
func TagSessionReads(rawPath, sessionDir string, roots Roots) (int, error) {
	entries, err := LoadRawEntries(rawPath)
	if err != nil || len(entries) == 0 {
		return 0, err
	}
	w := contexttrace.NewWriter(sessionDir)
	existing, err := contexttrace.ReadEvents(w.Path())
	if err != nil {
		return 0, err
	}
	seen := make(map[string]bool, len(existing))
	for _, ev := range existing {
		seen[eventKey(ev)] = true
	}

	candidates := Scan(entries, roots)
	candidates = append(candidates, ScanUses(entries, contextuse.Surfaced(existing), roots)...)

	var fresh []contexttrace.Event
	for _, ev := range candidates {
		k := eventKey(ev)
		if seen[k] {
			continue
		}
		seen[k] = true
		fresh = append(fresh, ev)
	}
	if len(fresh) == 0 {
		return 0, nil
	}
	if err := w.AppendAll(fresh); err != nil {
		return 0, err
	}
	return len(fresh), nil
}

// eventKey is the idempotency key for a tagger-written event.
func eventKey(ev contexttrace.Event) string {
	return fmt.Sprintf("%s\x00%s\x00%s\x00%d", ev.Type, ev.Ref, ev.Evidence, ev.Seq)
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
