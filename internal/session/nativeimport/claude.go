package nativeimport

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ClaudeProjectsDir is where Claude Code keeps transcripts:
// $CLAUDE_CONFIG_DIR/projects, or ~/.claude/projects.
func ClaudeProjectsDir() (string, error) {
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return filepath.Join(dir, "projects"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude", "projects"), nil
}

// ClaudeDiscovery is what a scan of the projects directory found.
type ClaudeDiscovery struct {
	Paths     []string // <projects>/<folder>/<uuid>.jsonl, the only session transcripts
	Subagents int      // subagent threads, counted and never opened
}

// DiscoverClaude lists top-level <uuid>.jsonl transcripts, depth 2 exactly.
// Subagent threads live deeper (<folder>/<uuid>/subagents/*.jsonl) or under
// non-UUID names (agent-*.jsonl); they are counted, never read.
func DiscoverClaude(projectsDir string) (ClaudeDiscovery, error) {
	var out ClaudeDiscovery
	folders, err := os.ReadDir(projectsDir)
	if os.IsNotExist(err) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	for _, folder := range folders {
		if !folder.IsDir() {
			continue
		}
		dir := filepath.Join(projectsDir, folder.Name())
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			name := entry.Name()
			switch {
			case entry.IsDir():
				out.Subagents += countSubagentThreads(filepath.Join(dir, name))
			case entry.Type().IsRegular() && strings.HasSuffix(name, ".jsonl"):
				if IsNativeID(strings.TrimSuffix(name, ".jsonl")) {
					out.Paths = append(out.Paths, filepath.Join(dir, name))
				} else {
					out.Subagents++
				}
			}
		}
	}
	return out, nil
}

func countSubagentThreads(dir string) int {
	entries, err := os.ReadDir(filepath.Join(dir, "subagents"))
	if err != nil {
		return 0
	}
	n := 0
	for _, entry := range entries {
		if entry.Type().IsRegular() && strings.HasSuffix(entry.Name(), ".jsonl") {
			n++
		}
	}
	return n
}

// persistedOutput is Claude Code's note when hook output was too large to keep
// inline: the transcript holds a preview and the full text lives in a file.
var persistedOutput = regexp.MustCompile(`Full output saved to: (\S+?\.txt)`)

type claudeRecord struct {
	Type        string          `json:"type"`
	SessionID   string          `json:"sessionId"`
	Timestamp   string          `json:"timestamp"`
	CWD         string          `json:"cwd"`
	GitBranch   string          `json:"gitBranch"`
	IsMeta      bool            `json:"isMeta"`
	IsSidechain bool            `json:"isSidechain"`
	IsCompact   bool            `json:"isCompactSummary"`
	Message     json.RawMessage `json:"message"`
	Attachment  json.RawMessage `json:"attachment"`
}

type claudeMessage struct {
	Content json.RawMessage `json:"content"`
}

type claudePart struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	ToolUseID string          `json:"tool_use_id"`
	Input     json.RawMessage `json:"input"`
	Content   json.RawMessage `json:"content"`
}

// InspectClaude reads one Claude Code transcript structurally. The filename's
// UUID is the session ID.
//
// Claude Code writes a resumed or forked conversation to a new file named for
// its new session ID, and opens it with a copy of the conversation it
// continues, whose records keep the earlier session's ID. That leading block
// is not this session: it is skipped here, and Offset marks where the
// session's own records begin so the import reads only those. Another
// session's record after the first of its own means the file mixes sessions,
// and is refused.
func InspectClaude(path string) (Session, error) {
	id := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	if !IsNativeID(id) {
		return Session{}, fmt.Errorf("transcript name is not a session ID")
	}
	s := Session{Agent: AgentClaude, NativeID: id, Path: path}
	cwds := cwdSet{}
	primeCalls := map[string]bool{}
	inspect := func(rec claudeRecord, line []byte) {
		s.observe(parseTimestamp(rec.Timestamp))
		cwds.add(rec.CWD)
		if rec.GitBranch != "" {
			s.Branch = rec.GitBranch
		}
		switch rec.Type {
		case "user":
			s.inspectClaudeUser(rec, primeCalls)
		case "assistant":
			s.inspectClaudeAssistant(rec, primeCalls)
		case "attachment", "system":
			s.inspectClaudeHookOutput(line, filepath.Dir(path))
		}
	}
	type pendingRecord struct {
		rec  claudeRecord
		line []byte
	}
	// Records that name no session belong to whichever block they sit in, so
	// the ones before any record names a session wait until one does.
	var pending []pendingRecord
	var pos int64
	own, inherited := false, false
	info, err := readRecords(path, func(lineNo int, line []byte) error {
		start := pos
		pos += int64(len(line))
		var rec claudeRecord
		if json.Unmarshal(line, &rec) != nil {
			return fmt.Errorf("invalid JSON at line %d", lineNo)
		}
		switch {
		case rec.SessionID == id:
			if !own {
				own = true
				if inherited {
					s.Offset = start
				}
				for _, p := range pending {
					inspect(p.rec, p.line)
				}
				pending = nil
			}
		case rec.SessionID != "":
			if own {
				return fmt.Errorf("record at line %d belongs to another session", lineNo)
			}
			inherited, pending = true, nil
			return nil
		case !own:
			if !inherited {
				pending = append(pending, pendingRecord{rec, line})
			}
			return nil
		}
		inspect(rec, line)
		return nil
	})
	if err != nil {
		return Session{}, err
	}
	if !own {
		if inherited {
			return Session{}, fmt.Errorf("transcript holds only the conversation it continues")
		}
		for _, p := range pending {
			inspect(p.rec, p.line)
		}
	}
	s.Size, s.ModTime, s.CWDs = info.Size(), info.ModTime(), cwds.sorted()
	if s.StartedAt.IsZero() {
		return Session{}, fmt.Errorf("transcript has no timestamps")
	}
	return s, nil
}

func (s *Session) inspectClaudeUser(rec claudeRecord, primeCalls map[string]bool) {
	if rec.IsSidechain || rec.IsCompact {
		return
	}
	var msg claudeMessage
	if json.Unmarshal(rec.Message, &msg) != nil {
		return
	}
	var text string
	if json.Unmarshal(msg.Content, &text) == nil {
		if !rec.IsMeta && strings.TrimSpace(text) != "" {
			s.Prompts++
		}
		return
	}
	var parts []claudePart
	if json.Unmarshal(msg.Content, &parts) != nil {
		return
	}
	prompt := false
	for _, part := range parts {
		switch part.Type {
		case "text":
			prompt = prompt || !rec.IsMeta
		case "tool_result":
			if primeCalls[part.ToolUseID] {
				var content any
				if json.Unmarshal(part.Content, &content) == nil {
					s.Markers = append(s.Markers, markersIn(content)...)
				}
			}
		}
	}
	if prompt {
		s.Prompts++
	}
}

func (s *Session) inspectClaudeAssistant(rec claudeRecord, primeCalls map[string]bool) {
	if rec.IsSidechain {
		return
	}
	var msg claudeMessage
	if json.Unmarshal(rec.Message, &msg) != nil {
		return
	}
	var parts []claudePart
	if json.Unmarshal(msg.Content, &parts) != nil {
		return
	}
	replied := false
	for _, part := range parts {
		switch part.Type {
		case "text":
			replied = true
		case "tool_use":
			var input struct {
				Command string `json:"command"`
			}
			if json.Unmarshal(part.Input, &input) == nil && strings.Contains(input.Command, "ox agent prime") {
				primeCalls[part.ID] = true
			}
		}
	}
	if replied {
		s.Replies++
	}
}

// inspectClaudeHookOutput reads ox's marker from hook output, including the
// side file Claude Code writes when the output was too large to keep inline.
func (s *Session) inspectClaudeHookOutput(line []byte, dir string) {
	if !strings.Contains(string(line), "session-context") && !strings.Contains(string(line), "Full output saved to") {
		return
	}
	var rec any
	if json.Unmarshal(line, &rec) != nil {
		return
	}
	s.Markers = append(s.Markers, markersIn(rec)...)
	WalkStrings(rec, func(text string) {
		for _, match := range persistedOutput.FindAllStringSubmatch(text, -1) {
			s.Markers = append(s.Markers, readSideFileMarkers(match[1], dir)...)
		}
	})
}

// readSideFileMarkers reads a persisted hook output file, but only one that
// lives under the transcript's own project folder.
func readSideFileMarkers(path, projectDir string) []Marker {
	path = filepath.Clean(path)
	rel, err := filepath.Rel(projectDir, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 1<<20))
	if err != nil {
		return nil
	}
	return ParseMarkers(string(data))
}
