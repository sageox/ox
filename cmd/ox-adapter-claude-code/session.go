// session.go handles Claude Code session reading, parsing, and discovery.
//
// Claude Code stores sessions as JSONL in ~/.claude/projects/<path>/sessions/.
// Each line is a JSON object with a "type" field: "human" (user), "assistant",
// "tool_use", "tool_result", or "summary". Timestamps are in the "timestamp"
// field (ISO 8601). Tool entries use "tool_name", "tool_input", and "call_id"
// for correlation.
//
// Format reference: https://docs.anthropic.com/en/docs/claude-code
package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/sageox/ox/internal/session/claudesource"
	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/sageox/ox/pkg/adapterruntime"
)

// regex patterns for stripping system-injected tags from user content
var (
	reStripSystemReminder          = regexp.MustCompile(`(?s)<system-reminder>.*?</system-reminder>`)
	reStripSystemInstructionUndsc  = regexp.MustCompile(`(?s)<system_instruction>.*?</system_instruction>`)
	reStripSystemInstructionHyphen = regexp.MustCompile(`(?s)<system-instruction>.*?</system-instruction>`)
	reStripLocalCommandStdout      = regexp.MustCompile(`(?s)<local-command-stdout>.*?</local-command-stdout>`)
	reStripLocalCommandCaveat      = regexp.MustCompile(`(?s)<local-command-caveat>.*?</local-command-caveat>`)
	reStripCommandName             = regexp.MustCompile(`(?s)<command-name>.*?</command-name>`)
	reStripCommandMessage          = regexp.MustCompile(`(?s)<command-message>.*?</command-message>`)
	reStripCommandArgs             = regexp.MustCompile(`(?s)<command-args>.*?</command-args>`)
	reCommandName                  = regexp.MustCompile(`(?s)<command-name>\s*(.*?)\s*</command-name>`)
	reCommandArgs                  = regexp.MustCompile(`(?s)<command-args>\s*(.*?)\s*</command-args>`)
	reStripTaskNotification        = regexp.MustCompile(`(?s)<task-notification>.*?</task-notification>`)
)

// userContentClass indicates how a user message should be classified.
type userContentClass int

const (
	userContentUser userContentClass = iota
	userContentSystem
	userContentSkip
)

type sessionCandidate struct {
	path    string
	modTime time.Time
}

// claudeProjectHash converts a directory path to Claude Code's project hash format.
func claudeProjectHash(path string) string {
	h := strings.ReplaceAll(path, string(os.PathSeparator), "-")
	h = strings.ReplaceAll(h, "_", "-")
	h = strings.ReplaceAll(h, ".", "-")
	return h
}

func handleRead(p adapterprotocol.ReadParams) (*adapterprotocol.ReadResult, error) {
	entries, meta, err := readSessionFile(p.SessionFile)
	if err != nil {
		return nil, err
	}
	return &adapterprotocol.ReadResult{
		Entries:  entries,
		Metadata: meta,
	}, nil
}

func handleReadMetadata(p adapterprotocol.ReadParams) (*adapterprotocol.ReadMetadataResult, error) {
	f, err := os.Open(p.SessionFile)
	if err != nil {
		return nil, fmt.Errorf("failed to open session file: %w", err)
	}
	defer func() { _ = f.Close() }()

	meta := &adapterprotocol.ReadMetadataResult{}
	scanner := bufio.NewScanner(f)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 10*1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var entry claudeCodeEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			continue
		}
		if entry.Version != "" && meta.AgentVersion == "" {
			meta.AgentVersion = entry.Version
		}
		if entry.Type == "assistant" && entry.Message != nil && entry.Message.Model != "" {
			meta.Model = entry.Message.Model
		}
		if meta.AgentVersion != "" && meta.Model != "" {
			break
		}
	}

	return meta, nil
}

func readSessionFile(path string) ([]adapterprotocol.RawEntry, *adapterprotocol.SessionMetadata, error) {
	meta := &adapterprotocol.SessionMetadata{}
	entries, _, err := adapterruntime.TailJSONL(path, 0, func(line []byte) ([]adapterprotocol.RawEntry, error) {
		parsed, err := parseLine(line)
		if err != nil {
			return nil, err
		}
		// a whole-file read cannot be chunked, and its consumers (import,
		// finalize) do not stream, so images stay out of it
		parsed = slices.DeleteFunc(parsed, func(e adapterprotocol.RawEntry) bool { return e.Role == adapterprotocol.RoleMedia })
		var raw claudeCodeEntry
		if json.Unmarshal(line, &raw) == nil {
			if raw.Version != "" && meta.AgentVersion == "" {
				meta.AgentVersion = raw.Version
			}
			if raw.Type == "assistant" && raw.Message != nil && raw.Message.Model != "" {
				meta.Model = raw.Message.Model
			}
		}
		return parsed, nil
	})
	return entries, meta, err
}

func readFromOffset(path string, offset int64) ([]adapterprotocol.RawEntry, int64, error) {
	return readPage(path, offset, maxReadResponseBytes)
}

// maxReadResponseBytes bounds one read-from-offset response as serialized. The
// daemon cancels an adapter whose one-shot output (stdout and stderr together)
// passes 64 MiB and keeps the old cursor, so a catch-up that ever serialized
// past it would fail on every retry and capture would never advance. Pasted
// images ride the response as bytes and tool results are whole file contents,
// so a backlog of either crosses it easily. Smaller pages cost one more
// adapter subprocess per page; 48 MiB keeps a 16 MiB margin for the JSON
// framing, stderr, and encoder drift, which the tests check against the real
// response.
const maxReadResponseBytes = 48 << 20

// responseFramingBytes covers what the response adds around its entries:
// {"entries":[ ... ],"new_offset":<int64>} and the trailing newline.
const responseFramingBytes = 64

// readPage reads complete records from offset while the response stays under
// limit bytes, and returns the offset of the first record it left for the next
// read. Records are never cut: the external adapter drains pages until the
// cursor reaches the end, so a stop or recovery still sees every record. A
// trailing partial record stays unconsumed, as in every read: a concurrent
// Claude write could otherwise make validation pass while the turn is dropped.
func readPage(path string, offset, limit int64) ([]adapterprotocol.RawEntry, int64, error) {
	room := limit - responseFramingBytes
	var used int64
	parse := func(line []byte) ([]adapterprotocol.RawEntry, error) {
		entries, err := parseLine(line)
		if err != nil {
			return nil, err
		}
		if n := responseBytes(entries); n > room {
			// no page could ever carry it; without this every read would stop
			// before it again, or carry it past the daemon's limit and fail
			entries = omitOversizedRecord(entries)
			log.Printf("read-from-offset: a %d-byte record exceeds the %d-byte response budget; its text and image bytes are replaced by a gap marker", n, room)
		}
		return entries, nil
	}
	fits := func(entries []adapterprotocol.RawEntry) bool {
		n := responseBytes(entries)
		if used+n > room {
			return false
		}
		used += n
		return true
	}
	return adapterruntime.TailJSONLUntil(path, offset, parse, fits)
}

// responseBytes is the size entries add to a response, encoded the way
// adapterruntime writes it (no HTML escaping). Each Encode ends in a newline,
// which stands in for the separating comma.
func responseBytes(entries []adapterprotocol.RawEntry) int64 {
	var w countingWriter
	enc := json.NewEncoder(&w)
	enc.SetEscapeHTML(false)
	for i := range entries {
		// a RawEntry holds only strings, bools, ints and bytes, which always
		// encode; the writer never fails
		_ = enc.Encode(&entries[i])
	}
	return w.n
}

type countingWriter struct{ n int64 }

func (w *countingWriter) Write(p []byte) (int, error) {
	w.n += int64(len(p))
	return len(p), nil
}

// omittedRecordText replaces the text of a record no response could carry.
// Only a record that serializes far larger than its line can reach this: a
// line is at most 10 MiB, and a tool input dense in "<", ">" or "&" is escaped
// twice, up to seven bytes for each one in the transcript.
const omittedRecordText = "[ox: this transcript record was too large to capture]"

// omitOversizedRecord keeps a record's entries, their roles, tools and call
// ids, so the turn and its tool pairing survive as a declared gap; their text
// and image bytes do not.
func omitOversizedRecord(entries []adapterprotocol.RawEntry) []adapterprotocol.RawEntry {
	out := make([]adapterprotocol.RawEntry, len(entries))
	for i, e := range entries {
		for _, field := range []*string{&e.Content, &e.ToolInput, &e.ToolOutput} {
			if *field != "" {
				*field = omittedRecordText
			}
		}
		if e.Media != nil {
			m := *e.Media
			m.Data = nil
			if m.Skipped == "" {
				m.Skipped = adapterprotocol.MediaSkippedReadLimit
			}
			e.Media = &m
		}
		out[i] = e
	}
	return out
}

func findSessionFile(repoRoot, agentID, since, agentSessionID string) (string, int64, error) {
	if resolved, err := filepath.EvalSymlinks(repoRoot); err == nil {
		repoRoot = resolved
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", 0, fmt.Errorf("cannot determine home directory: %w", err)
	}

	projectsDir := filepath.Join(home, ".claude", "projects")
	projectHash := claudeProjectHash(repoRoot)
	projectDir := filepath.Join(projectsDir, projectHash)

	// direct lookup via agent session ID: Claude Code files are {sessionId}.jsonl
	if agentSessionID != "" {
		// Trust boundary: AgentSessionID arrives via the adapterprotocol JSON-RPC
		// channel from the daemon. Without this gate, a malicious project's
		// CLAUDE.md hook could pass "../../../sensitive-export" and the adapter
		// would happily read and return that file's bytes through the normal
		// session pipeline. The other 6 ox adapters already call this — claude-code
		// was the lone outlier flagged by /security-review.
		if err := adapterruntime.ValidateSessionID(agentSessionID); err != nil {
			return "", 0, err
		}
		candidate := filepath.Join(projectDir, agentSessionID+".jsonl")
		if info, err := os.Lstat(candidate); err == nil && info.Mode().IsRegular() {
			if err := claudesource.Validate(candidate, repoRoot, agentSessionID); err != nil {
				return "", 0, fmt.Errorf("session %s at %s has invalid repository metadata: %w", agentSessionID, candidate, err)
			}
			return candidate, findStartOffset(candidate, parseSessionSince(since)), nil
		}
		// Resumed Claude sessions can stay in their original project bucket.
		// A matching filename elsewhere is not evidence of repo ownership:
		// reject mixed/foreign cwd metadata rather than leak it into this Ledger.
		if other, err := findClaudeSessionInOtherBucket(projectsDir, projectHash, repoRoot, agentSessionID); err != nil {
			return "", 0, err
		} else if other != "" {
			return other, findStartOffset(other, parseSessionSince(since)), nil
		}
		return "", 0, fmt.Errorf("session %s not found for project %s", agentSessionID, repoRoot)
	}

	if _, err := os.Stat(projectDir); err != nil {
		return "", 0, fmt.Errorf("no sessions for project %s: %w", repoRoot, err)
	}

	sinceTime := time.Time{}
	if since != "" {
		if t, err := time.Parse(time.RFC3339, since); err == nil {
			sinceTime = t
		}
	}

	entries, err := os.ReadDir(projectDir)
	if err != nil {
		return "", 0, fmt.Errorf("failed to read project directory: %w", err)
	}

	// subtract a buffer to account for startup timing drift — Claude Code
	// may not write to the session file until after the first human message,
	// so its mtime can lag behind the recorded StartedAt timestamp.
	if !sinceTime.IsZero() {
		sinceTime = sinceTime.Add(-30 * time.Second)
	}

	var candidates []sessionCandidate
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if !sinceTime.IsZero() && info.ModTime().Before(sinceTime) {
			continue
		}
		candidates = append(candidates, sessionCandidate{
			path:    filepath.Join(projectDir, entry.Name()),
			modTime: info.ModTime(),
		})
	}

	if len(candidates) == 0 {
		return "", 0, fmt.Errorf("no sessions modified after %v", since)
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].modTime.After(candidates[j].modTime)
	})

	for _, c := range candidates {
		if (agentID == "" || sessionContainsAgentID(c.path, agentID)) && claudesource.Validate(c.path, repoRoot, "") == nil {
			if agentID == "" {
				return c.path, 0, nil
			}
			offset := findStartOffset(c.path, sinceTime)
			return c.path, offset, nil
		}
	}

	// Trust-boundary note: the previous fallback returned candidates[0].path
	// (the newest file) when the caller supplied an agentID but no candidate
	// contained it. Under concurrent ox sessions sharing a project root, that
	// silently handed session A's adapter a JSONL belonging to session B —
	// including secrets typed in B. The caller asked for a specific identity;
	// returning a different one violates the contract. Fail loud instead so
	// the caller can decide (retry, prompt the user, fall through to a
	// different lookup). See SECREVIEW llm-trust LOW.
	return "", 0, fmt.Errorf("no session file contains agentID %q", agentID)
}

func parseSessionSince(since string) time.Time {
	t, _ := time.Parse(time.RFC3339, since)
	return t
}

func findClaudeSessionInOtherBucket(projectsDir, ownBucket, repoRoot, sessionID string) (string, error) {
	dirs, err := os.ReadDir(projectsDir)
	if err != nil {
		return "", err
	}
	var match string
	var rejected []string
	for _, dir := range dirs {
		if !dir.IsDir() || dir.Name() == ownBucket {
			continue
		}
		path := filepath.Join(projectsDir, dir.Name(), sessionID+".jsonl")
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if err := claudesource.Validate(path, repoRoot, sessionID); err != nil {
			rejected = append(rejected, fmt.Sprintf("%s: %v", path, err))
			continue
		}
		if match != "" {
			return "", fmt.Errorf("session %s has multiple valid project buckets", sessionID)
		}
		match = path
	}
	if match == "" && len(rejected) > 0 {
		// the transcript exists but is not this repo's: say why, or a stop
		// reports "not found" and leaves nothing to act on
		return "", fmt.Errorf("session %s found but not accepted for project %s: %s", sessionID, repoRoot, strings.Join(rejected, "; "))
	}
	return match, nil
}

func sessionContainsAgentID(path, agentID string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()

	// no line cap: a transcript line holding a screenshot can pass 1 MiB, and
	// a scanner would stop there without reading the rest of the session
	reader := bufio.NewReaderSize(f, 64*1024)
	needle := []byte(agentID)
	for {
		line, err := reader.ReadBytes('\n')
		if bytes.Contains(line, needle) {
			return true
		}
		if err != nil {
			return false
		}
	}
}

// findStartOffset locates the byte offset where entries after sinceTime begin.
func findStartOffset(path string, sinceTime time.Time) int64 {
	if sinceTime.IsZero() {
		return 0
	}
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer func() { _ = f.Close() }()

	// no line cap: stopping at the first line over a scanner's limit would
	// return that line's offset as the recording's start
	reader := bufio.NewReaderSize(f, 64*1024)
	var offset int64
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			// an unterminated tail is still being written: start no later than it
			return offset
		}
		var entry struct {
			Timestamp string `json:"timestamp"`
		}
		if json.Unmarshal(line, &entry) == nil && entry.Timestamp != "" {
			if t, err := time.Parse(time.RFC3339, entry.Timestamp); err == nil {
				if t.After(sinceTime) {
					return offset
				}
			}
		}
		offset += int64(len(line))
	}
}

// --- line parsing ---

// parseTS converts a raw timestamp string to time.Time for use with entry constructors.
func parseTS(s string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}

func parseLine(line []byte) ([]adapterprotocol.RawEntry, error) {
	var raw claudeCodeEntry
	if err := json.Unmarshal(line, &raw); err != nil {
		return nil, err
	}

	switch raw.Type {
	case "user", "assistant":
		// process these
	default:
		return nil, nil
	}

	switch raw.Type {
	case "user":
		return parseUserEntry(&raw)
	case "assistant":
		return parseAssistantEntry(&raw)
	}

	return nil, nil
}

func parseUserEntry(raw *claudeCodeEntry) ([]adapterprotocol.RawEntry, error) {
	ts := parseTS(raw.Timestamp)

	if raw.IsMeta {
		content := extractRawUserText(raw)
		cleaned := strings.TrimSpace(stripSystemTags(content))
		if cleaned == "" {
			cleaned = content
		}
		return []adapterprotocol.RawEntry{adapterruntime.SystemEntry(ts, cleaned)}, nil
	}

	var entries []adapterprotocol.RawEntry

	content, class := classifyUserContent(raw)
	images := extractImages(raw, ts)
	if class == userContentSkip && len(images) > 0 && !hasToolResultBlock(raw) {
		// a screenshot with no words is still the person speaking: the live
		// stream opens on a user turn, and a media line alone is not one
		class, content = userContentUser, imageOnlyPrompt
	}
	switch class {
	case userContentSkip:
		// still extract tool_result blocks below
	case userContentSystem:
		entries = append(entries, adapterruntime.SystemEntry(ts, content))
	default:
		entries = append(entries, adapterruntime.UserEntry(ts, content))
	}

	// pasted screenshots follow the turn's text, so a viewer places them at it
	entries = append(entries, images...)

	// extract tool_result blocks from user content arrays
	entries = append(entries, extractToolResults(raw, ts)...)

	if len(entries) == 0 {
		return nil, nil
	}
	return entries, nil
}

func hasToolResultBlock(raw *claudeCodeEntry) bool {
	content, _ := raw.Message.Content.([]interface{})
	for _, item := range content {
		if block, ok := item.(map[string]interface{}); ok && block["type"] == "tool_result" {
			return true
		}
	}
	return false
}

// imageOnlyPrompt stands in for the text of a turn that is only a pasted image.
const imageOnlyPrompt = "[pasted image]"

// extractImages returns one media entry per base64 image block in a user
// message: a screenshot pasted into the prompt. Bytes are carried once, to the
// daemon, which stages them for upload; they are never part of any text entry.
// An image over the size cap or of a type the live session does not accept is
// returned as a skipped entry, so the loss is declared downstream.
func extractImages(raw *claudeCodeEntry, ts time.Time) []adapterprotocol.RawEntry {
	if raw.Message == nil {
		return nil
	}
	content, ok := raw.Message.Content.([]interface{})
	if !ok {
		return nil
	}
	var entries []adapterprotocol.RawEntry
	for position, item := range content {
		block, ok := item.(map[string]interface{})
		if !ok || block["type"] != "image" {
			continue
		}
		source, _ := block["source"].(map[string]interface{})
		if kind, _ := source["type"].(string); kind != "base64" {
			continue // a URL source carries no bytes to upload
		}
		mime, _ := source["media_type"].(string)
		encoded, _ := source["data"].(string)
		entries = append(entries, adapterruntime.MediaEntry(ts, imageRef(position, mime, encoded)))
	}
	return entries
}

// imageRef validates one pasted image against the live session's limits.
func imageRef(position int, mime, encoded string) adapterprotocol.MediaRef {
	ref := adapterprotocol.MediaRef{Position: position, MIME: mime}
	if !adapterprotocol.AllowedMediaMIME(mime) {
		ref.Skipped = adapterprotocol.MediaSkippedUnsupportedType
		return ref
	}
	// checked before decoding so a huge block is never expanded
	if int64(base64.StdEncoding.DecodedLen(len(encoded))) > adapterprotocol.MaxMediaBytes {
		ref.Skipped = adapterprotocol.MediaSkippedTooLarge
		ref.Size = int64(base64.StdEncoding.DecodedLen(len(encoded)))
		return ref
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(data) == 0 {
		ref.Skipped = adapterprotocol.MediaSkippedMalformed
		return ref
	}
	sum := sha256.Sum256(data)
	ref.SHA256, ref.Size, ref.Data = hex.EncodeToString(sum[:]), int64(len(data)), data
	return ref
}

// extractToolResults extracts tool_result content blocks from a user message.
// In Claude's format, tool results appear as content blocks in the user turn
// following an assistant's tool_use.
func extractToolResults(raw *claudeCodeEntry, ts time.Time) []adapterprotocol.RawEntry {
	if raw.Message == nil {
		return nil
	}

	content, ok := raw.Message.Content.([]interface{})
	if !ok {
		return nil
	}

	var entries []adapterprotocol.RawEntry
	for _, item := range content {
		block, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		if block["type"] != "tool_result" {
			continue
		}

		toolOutput := extractToolResultContent(block)
		isError, _ := block["is_error"].(bool)
		callID, _ := block["tool_use_id"].(string)
		if callID != "" {
			entries = append(entries, adapterruntime.ToolResultWithID(ts, toolOutput, isError, callID))
		} else {
			entries = append(entries, adapterruntime.ToolResultEntry(ts, toolOutput, isError))
		}
	}
	return entries
}

// extractToolResultContent extracts text from a tool_result content block.
// The content field can be a string or an array of content blocks.
func extractToolResultContent(block map[string]interface{}) string {
	// content can be a direct string
	if s, ok := block["content"].(string); ok {
		return s
	}

	// or an array of content blocks
	contentArr, ok := block["content"].([]interface{})
	if !ok {
		return ""
	}

	var parts []string
	for _, item := range contentArr {
		if sub, ok := item.(map[string]interface{}); ok {
			if sub["type"] == "text" {
				if text, ok := sub["text"].(string); ok {
					parts = append(parts, text)
				}
			}
		}
	}
	return strings.Join(parts, "\n")
}

// Claude Code writes this assistant line itself when a conversation is
// resumed (claude --continue / --resume), tagged with the model "<synthetic>".
// No model said it, so it is not a turn: captured, it showed teammates a
// stray "No response requested." between the two parts of a resumed session.
const (
	syntheticModel    = "<synthetic>"
	resumePlaceholder = "No response requested."
)

// isResumePlaceholder reports whether raw is exactly that placeholder: the
// synthetic model and one text block holding only it. Any other synthetic
// line (an API error Claude Code reports, say) is still captured.
func isResumePlaceholder(raw *claudeCodeEntry) bool {
	if raw.Message == nil || raw.Message.Model != syntheticModel {
		return false
	}
	blocks, ok := raw.Message.Content.([]interface{})
	if !ok || len(blocks) != 1 {
		return false
	}
	block, ok := blocks[0].(map[string]interface{})
	if !ok || block["type"] != "text" {
		return false
	}
	text, _ := block["text"].(string)
	return strings.TrimSpace(text) == resumePlaceholder
}

func parseAssistantEntry(raw *claudeCodeEntry) ([]adapterprotocol.RawEntry, error) {
	if raw.Message == nil || isResumePlaceholder(raw) {
		return nil, nil
	}

	var entries []adapterprotocol.RawEntry
	content, ok := raw.Message.Content.([]interface{})
	if !ok {
		return nil, nil
	}

	ts := parseTS(raw.Timestamp)
	for _, item := range content {
		block, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		blockType, _ := block["type"].(string)

		switch blockType {
		case "text":
			text, _ := block["text"].(string)
			if text != "" {
				entries = append(entries, adapterruntime.AssistantEntry(ts, text))
			}
		case "tool_use":
			toolName, _ := block["name"].(string)
			callID, _ := block["id"].(string)
			input := block["input"]
			inputJSON, _ := json.Marshal(input)
			if callID != "" {
				entries = append(entries, adapterruntime.ToolUseWithID(ts, toolName, string(inputJSON), callID))
			} else {
				entries = append(entries, adapterruntime.ToolUseEntry(ts, toolName, string(inputJSON)))
			}
		}
	}

	return entries, nil
}

func classifyUserContent(raw *claudeCodeEntry) (string, userContentClass) {
	if raw.Message == nil {
		return "", userContentSkip
	}

	switch content := raw.Message.Content.(type) {
	case string:
		return classifyTextContent(content)
	case []interface{}:
		var textParts []string
		hasToolResult := false
		hasText := false

		for _, item := range content {
			block, ok := item.(map[string]interface{})
			if !ok {
				continue
			}
			switch block["type"] {
			case "text":
				if text, ok := block["text"].(string); ok {
					hasText = true
					textParts = append(textParts, text)
				}
			case "tool_result":
				hasToolResult = true
			}
		}

		if hasToolResult && !hasText {
			return "", userContentSkip
		}

		joined := strings.Join(textParts, "\n")
		return classifyTextContent(joined)
	}

	return "", userContentSkip
}

func extractRawUserText(raw *claudeCodeEntry) string {
	if raw.Message == nil {
		return ""
	}
	switch content := raw.Message.Content.(type) {
	case string:
		return content
	case []interface{}:
		var parts []string
		for _, item := range content {
			if block, ok := item.(map[string]interface{}); ok {
				if block["type"] == "text" {
					if text, ok := block["text"].(string); ok {
						parts = append(parts, text)
					}
				}
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

func classifyTextContent(text string) (string, userContentClass) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return "", userContentSkip
	}

	// A slash command that carries its own request ("/review the auth change")
	// is a person asking for work, so it is their prompt, written as typed.
	// Stripping its tags left nothing, so a session driven only by skills never
	// counted as prompted and never went live. A bare command ("/clear",
	// "/compact") stays system: it asks for nothing, and counting it would open
	// a live stream for a session that may never get a prompt.
	if prompt, ok := slashCommandPrompt(text); ok {
		return prompt, userContentUser
	}

	if strings.Contains(text, "<!-- ox-hash:") {
		return text, userContentSystem
	}

	if strings.HasPrefix(trimmed, "Entered plan mode.") ||
		strings.HasPrefix(trimmed, "Plan mode is active.") ||
		strings.HasPrefix(trimmed, "Plan mode still active") {
		return text, userContentSystem
	}

	if strings.HasPrefix(trimmed, "This session is being continued from a previous conversation") {
		return text, userContentSystem
	}

	if strings.HasPrefix(trimmed, "\u23fa ") || strings.HasPrefix(trimmed, "⏺ ") ||
		strings.HasPrefix(trimmed, "\u23bf") || strings.HasPrefix(trimmed, "⎿") {
		return text, userContentSystem
	}

	if strings.HasPrefix(trimmed, "[Request interrupted") {
		return text, userContentSystem
	}

	cleaned := stripSystemTags(text)
	cleanedTrimmed := strings.TrimSpace(cleaned)
	if cleanedTrimmed == "" {
		return text, userContentSystem
	}

	return cleanedTrimmed, userContentUser
}

// slashCommandPrompt returns "/name args" for a slash-command message whose
// <command-args> is non-empty, the shape the server and web viewer count as a
// prompt (sageox-monorepo packages/sessionturns isRealPrompt).
func slashCommandPrompt(text string) (string, bool) {
	// Only a message that IS a command envelope: one that quotes command tags
	// inside a question ("why did <command-name>... fail?") keeps its own text
	// through the normal path, so the person's words are never replaced.
	rest := reStripCommandName.ReplaceAllString(text, "")
	rest = reStripCommandMessage.ReplaceAllString(rest, "")
	rest = reStripCommandArgs.ReplaceAllString(rest, "")
	if strings.TrimSpace(rest) != "" {
		return "", false
	}
	name := reCommandName.FindStringSubmatch(text)
	args := reCommandArgs.FindStringSubmatch(text)
	if name == nil || args == nil || name[1] == "" || args[1] == "" {
		return "", false
	}
	cmd := name[1]
	if !strings.HasPrefix(cmd, "/") {
		cmd = "/" + cmd
	}
	return cmd + " " + args[1], true
}

func stripSystemTags(text string) string {
	text = reStripSystemReminder.ReplaceAllString(text, "")
	text = reStripSystemInstructionUndsc.ReplaceAllString(text, "")
	text = reStripSystemInstructionHyphen.ReplaceAllString(text, "")
	text = reStripLocalCommandStdout.ReplaceAllString(text, "")
	text = reStripLocalCommandCaveat.ReplaceAllString(text, "")
	text = reStripCommandName.ReplaceAllString(text, "")
	text = reStripCommandMessage.ReplaceAllString(text, "")
	text = reStripCommandArgs.ReplaceAllString(text, "")
	text = reStripTaskNotification.ReplaceAllString(text, "")
	return text
}

// --- types ---

type claudeCodeEntry struct {
	Type      string             `json:"type"`
	Timestamp string             `json:"timestamp"`
	Version   string             `json:"version"`
	Message   *claudeCodeMessage `json:"message"`
	IsMeta    bool               `json:"isMeta"`
}

type claudeCodeMessage struct {
	Role    string      `json:"role"`
	Content interface{} `json:"content"`
	Model   string      `json:"model"`
}
