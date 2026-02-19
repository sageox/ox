package session

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// History schema validation errors.
var (
	// ErrHistoryMissingMeta is returned when the first line is not a valid _meta entry
	ErrHistoryMissingMeta = errors.New("history missing _meta line")

	// ErrHistoryInvalidMeta is returned when _meta is missing required fields
	ErrHistoryInvalidMeta = errors.New("history _meta missing required fields")

	// ErrHistoryInvalidEntryType is returned when entry type is not recognized
	ErrHistoryInvalidEntryType = errors.New("history entry has invalid type")

	// ErrHistoryNonMonotonicSeq is returned when seq numbers are not strictly increasing
	ErrHistoryNonMonotonicSeq = errors.New("history entry seq not monotonically increasing")

	// ErrHistoryDuplicateSeq is returned when two entries have the same seq number
	ErrHistoryDuplicateSeq = errors.New("history entry has duplicate seq")

	// ErrHistoryMissingSeq is returned when an entry is missing the seq field
	ErrHistoryMissingSeq = errors.New("history entry missing seq field")

	// ErrHistoryEmptyInput is returned when input reader is nil
	ErrHistoryEmptyInput = errors.New("history input is empty")
)

// HistorySchemaVersion is the current version of the history capture schema.
const HistorySchemaVersion = "1"

// HistorySourcePlanningHistory is the source marker for planning history entries.
const HistorySourcePlanningHistory = "planning_history"

// HistoryMeta is the metadata header for captured history.
// This is the first line of a captured history JSONL file.
type HistoryMeta struct {
	// SchemaVersion identifies the history format version
	SchemaVersion string `json:"schema_version"`

	// CapturedAt is when this history was captured
	CapturedAt time.Time `json:"captured_at"`

	// StartedAt is when the original conversation began (if known)
	StartedAt time.Time `json:"started_at,omitempty"`

	// Source identifies how this history was generated
	Source string `json:"source"` // "agent_reconstruction"

	// AgentID is the short agent ID (e.g., "Oxa7b3")
	AgentID string `json:"agent_id"`

	// SessionID is an alias for AgentID for compatibility
	SessionID string `json:"session_id,omitempty"`

	// AgentType identifies the AI agent (e.g., "claude-code", "cursor")
	AgentType string `json:"agent_type,omitempty"`

	// SessionTitle is a human-readable title for this session
	SessionTitle string `json:"session_title,omitempty"`

	// Username is the authenticated user who created this session
	Username string `json:"username,omitempty"`

	// MessageCount is the total number of entries captured
	MessageCount int `json:"message_count,omitempty"`

	// TimeRange is the time span of captured history
	TimeRange *HistoryTimeRange `json:"time_range,omitempty"`
}

// HistoryTimeRange represents the time span of captured history.
type HistoryTimeRange struct {
	Earliest time.Time `json:"earliest"`
	Latest   time.Time `json:"latest"`
}

// HistoryEntry is a single entry in captured history.
type HistoryEntry struct {
	// Seq is the sequence number (monotonically increasing)
	Seq int `json:"seq"`

	// Type identifies the entry type (user, assistant, system, tool)
	Type string `json:"type"`

	// Content is the message or response content
	Content string `json:"content"`

	// Timestamp is when this entry occurred (if known)
	Timestamp time.Time `json:"ts,omitempty"`

	// Source identifies the origin of this entry
	Source string `json:"source,omitempty"` // "planning_history"

	// ToolName is the name of the tool called (for tool entries)
	ToolName string `json:"tool_name,omitempty"`

	// ToolInput is the input passed to the tool (for tool entries)
	ToolInput string `json:"tool_input,omitempty"`

	// ToolOutput is the output from the tool (for tool entries)
	ToolOutput string `json:"tool_output,omitempty"`

	// Summary is a brief summary of this entry (optional)
	Summary string `json:"summary,omitempty"`

	// IsPlan indicates this entry contains a plan or decision
	IsPlan bool `json:"is_plan,omitempty"`
}

// ValidEntryTypes defines the allowed entry types for history entries.
var ValidEntryTypes = map[string]bool{
	"user":      true,
	"assistant": true,
	"system":    true,
	"tool":      true,
}

// CapturedHistory holds the full parsed history.
type CapturedHistory struct {
	Meta    *HistoryMeta
	Entries []HistoryEntry
}

// HistoryValidationResult contains the result of validating a history file.
type HistoryValidationResult struct {
	Valid      bool     `json:"valid"`
	Errors     []string `json:"errors,omitempty"`
	EntryCount int      `json:"entry_count"`
	HasMeta    bool     `json:"has_meta"`
}

// ValidateHistoryJSONLReader reads and validates a captured history JSONL stream.
func ValidateHistoryJSONLReader(reader io.Reader) (*CapturedHistory, error) {
	if reader == nil {
		return nil, fmt.Errorf("validate history: %w", ErrHistoryEmptyInput)
	}

	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	history := &CapturedHistory{
		Entries: make([]HistoryEntry, 0),
	}

	lineNum := 0
	lastSeq := 0
	metaParsed := false

	for scanner.Scan() {
		line := scanner.Bytes()
		lineNum++

		if len(line) == 0 {
			continue
		}

		if !metaParsed {
			meta, err := parseHistoryMetaLine(line)
			if err != nil {
				return nil, fmt.Errorf("validate history line=%d: %w", lineNum, err)
			}
			history.Meta = meta
			metaParsed = true
			continue
		}

		entry, err := parseAndValidateHistoryEntry(line, lineNum)
		if err != nil {
			return nil, err
		}

		if entry.Seq <= lastSeq && len(history.Entries) > 0 {
			return nil, fmt.Errorf("validate history line=%d: %w: seq=%d must be greater than previous seq=%d",
				lineNum, ErrHistoryNonMonotonicSeq, entry.Seq, lastSeq)
		}
		lastSeq = entry.Seq

		if !ValidEntryTypes[entry.Type] {
			return nil, fmt.Errorf("validate history line=%d: %w: type=%q not in [user, assistant, system, tool]",
				lineNum, ErrHistoryInvalidEntryType, entry.Type)
		}

		history.Entries = append(history.Entries, *entry)
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("validate history read error: %w", err)
	}

	if !metaParsed {
		return nil, fmt.Errorf("validate history: %w", ErrHistoryMissingMeta)
	}

	return history, nil
}

func parseHistoryMetaLine(line []byte) (*HistoryMeta, error) {
	var wrapper struct {
		Meta *HistoryMeta `json:"_meta"`
	}

	if err := json.Unmarshal(line, &wrapper); err == nil && wrapper.Meta != nil {
		return validateHistoryMetaFields(wrapper.Meta)
	}

	var meta HistoryMeta
	if err := json.Unmarshal(line, &meta); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrHistoryInvalidMeta, err)
	}

	return validateHistoryMetaFields(&meta)
}

func validateHistoryMetaFields(meta *HistoryMeta) (*HistoryMeta, error) {
	if meta.SchemaVersion == "" {
		return nil, fmt.Errorf("%w: schema_version", ErrHistoryInvalidMeta)
	}
	if meta.Source == "" {
		return nil, fmt.Errorf("%w: source", ErrHistoryInvalidMeta)
	}
	if meta.AgentID == "" {
		return nil, fmt.Errorf("%w: agent_id", ErrHistoryInvalidMeta)
	}

	return meta, nil
}

func parseAndValidateHistoryEntry(line []byte, lineNum int) (*HistoryEntry, error) {
	var entry HistoryEntry
	if err := json.Unmarshal(line, &entry); err != nil {
		return nil, fmt.Errorf("validate history line=%d: parse error: %w", lineNum, err)
	}

	if entry.Seq <= 0 {
		return nil, fmt.Errorf("validate history line=%d: %w", lineNum, ErrHistoryMissingSeq)
	}
	if entry.Type == "" {
		return nil, fmt.Errorf("validate history line=%d: type field is required", lineNum)
	}
	if entry.Content == "" {
		return nil, fmt.Errorf("validate history line=%d: content field is required", lineNum)
	}

	return &entry, nil
}

// NewHistoryMeta creates metadata for captured history.
func NewHistoryMeta(agentID, source string) *HistoryMeta {
	return &HistoryMeta{
		SchemaVersion: HistorySchemaVersion,
		CapturedAt:    time.Now(),
		Source:        source,
		AgentID:       agentID,
	}
}

// NewHistoryEntry creates a new history entry.
func NewHistoryEntry(seq int, entryType, content string) *HistoryEntry {
	return &HistoryEntry{
		Seq:       seq,
		Type:      entryType,
		Content:   content,
		Timestamp: time.Now(),
	}
}

// IsValidEntryType returns true if the given type is valid.
func IsValidEntryType(entryType string) bool {
	return ValidEntryTypes[entryType]
}

// Duration returns the duration of the captured history.
func (h *CapturedHistory) Duration() time.Duration {
	if h.Meta == nil || h.Meta.TimeRange == nil {
		return 0
	}
	if h.Meta.TimeRange.Latest.Before(h.Meta.TimeRange.Earliest) {
		return 0
	}
	return h.Meta.TimeRange.Latest.Sub(h.Meta.TimeRange.Earliest)
}

// EntryCount returns the number of entries in the history.
func (h *CapturedHistory) EntryCount() int {
	return len(h.Entries)
}

// GetEntriesByType returns all entries of a given type.
func (h *CapturedHistory) GetEntriesByType(entryType string) []HistoryEntry {
	result := make([]HistoryEntry, 0)
	for _, entry := range h.Entries {
		if entry.Type == entryType {
			result = append(result, entry)
		}
	}
	return result
}

// GetPlanEntries returns all entries marked as plans.
func (h *CapturedHistory) GetPlanEntries() []HistoryEntry {
	result := make([]HistoryEntry, 0)
	for _, entry := range h.Entries {
		if entry.IsPlan {
			result = append(result, entry)
		}
	}
	return result
}

// ToSessionEntry converts a HistoryEntry to a SessionEntry.
func (e *HistoryEntry) ToSessionEntry() SessionEntry {
	entryType := SessionEntryType(e.Type)
	if !entryType.IsValid() {
		entryType = SessionEntryTypeSystem
	}

	return SessionEntry{
		Timestamp:  e.Timestamp,
		Type:       entryType,
		Content:    e.Content,
		ToolName:   e.ToolName,
		ToolInput:  e.ToolInput,
		ToolOutput: e.ToolOutput,
	}
}

// HistoryEntryFromSessionEntry creates a HistoryEntry from a SessionEntry.
func HistoryEntryFromSessionEntry(e SessionEntry, seq int, source string) HistoryEntry {
	return HistoryEntry{
		Timestamp:  e.Timestamp,
		Type:       string(e.Type),
		Content:    e.Content,
		Seq:        seq,
		Source:     source,
		ToolName:   e.ToolName,
		ToolInput:  e.ToolInput,
		ToolOutput: e.ToolOutput,
	}
}

// ValidateHistoryMeta checks that required fields are present in the metadata.
func ValidateHistoryMeta(meta *HistoryMeta) error {
	if meta == nil {
		return ErrHistoryMissingMeta
	}
	_, err := validateHistoryMetaFields(meta)
	return err
}

// ValidateHistory validates a CapturedHistory and returns detailed results.
func ValidateHistory(h *CapturedHistory) *HistoryValidationResult {
	result := &HistoryValidationResult{
		Valid:   true,
		Errors:  make([]string, 0),
		HasMeta: h.Meta != nil && h.Meta.SchemaVersion != "",
	}

	if err := ValidateHistoryMeta(h.Meta); err != nil {
		result.Valid = false
		result.Errors = append(result.Errors, err.Error())
	}

	seenSeq := make(map[int]bool)
	lastSeq := -1

	for i, entry := range h.Entries {
		result.EntryCount++

		if !ValidEntryTypes[entry.Type] {
			result.Valid = false
			result.Errors = append(result.Errors, fmt.Sprintf("entry[%d]: invalid type: %s", i, entry.Type))
		}

		if entry.Seq > 0 {
			if seenSeq[entry.Seq] {
				result.Valid = false
				result.Errors = append(result.Errors, fmt.Sprintf("entry[%d]: %s: seq=%d", i, ErrHistoryDuplicateSeq.Error(), entry.Seq))
			}
			seenSeq[entry.Seq] = true

			if entry.Seq <= lastSeq {
				result.Valid = false
				result.Errors = append(result.Errors, fmt.Sprintf("entry[%d]: %s: seq=%d, last_seq=%d", i, ErrHistoryNonMonotonicSeq.Error(), entry.Seq, lastSeq))
			}
			lastSeq = entry.Seq
		}
	}

	return result
}

// ComputeTimeRange returns the earliest and latest timestamps in the history.
func (h *CapturedHistory) ComputeTimeRange() *HistoryTimeRange {
	if len(h.Entries) == 0 {
		return nil
	}

	var earliest, latest time.Time

	for _, entry := range h.Entries {
		if entry.Timestamp.IsZero() {
			continue
		}
		if earliest.IsZero() || entry.Timestamp.Before(earliest) {
			earliest = entry.Timestamp
		}
		if latest.IsZero() || entry.Timestamp.After(latest) {
			latest = entry.Timestamp
		}
	}

	if earliest.IsZero() && latest.IsZero() {
		return nil
	}

	return &HistoryTimeRange{
		Earliest: earliest,
		Latest:   latest,
	}
}

// ParseHistoryFile parses a JSONL history file from a file path.
func ParseHistoryFile(path string) (*CapturedHistory, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open history file: %w", err)
	}
	defer f.Close()

	return ValidateHistoryJSONLReader(f)
}

// ValidateHistoryFile parses and validates a history file.
func ValidateHistoryFile(path string) (*HistoryValidationResult, error) {
	history, err := ParseHistoryFile(path)
	if err != nil {
		return &HistoryValidationResult{
			Valid:  false,
			Errors: []string{err.Error()},
		}, nil
	}

	return ValidateHistory(history), nil
}

// ValidateHistoryJSONL validates a JSONL string as a history.
func ValidateHistoryJSONL(jsonl string) (*HistoryValidationResult, error) {
	history, err := ValidateHistoryJSONLReader(strings.NewReader(jsonl))
	if err != nil {
		return &HistoryValidationResult{
			Valid:  false,
			Errors: []string{err.Error()},
		}, nil
	}

	return ValidateHistory(history), nil
}

// ToSessionEntries converts all history entries to session entries.
func (h *CapturedHistory) ToSessionEntries() []SessionEntry {
	entries := make([]SessionEntry, 0, len(h.Entries))
	for _, he := range h.Entries {
		entries = append(entries, he.ToSessionEntry())
	}
	return entries
}

// HasPlanningHistory returns true if any entries have planning_history source.
func (h *CapturedHistory) HasPlanningHistory() bool {
	for _, e := range h.Entries {
		if e.Source == HistorySourcePlanningHistory {
			return true
		}
	}
	return false
}

// PlanningHistoryCount returns the number of entries with planning_history source.
func (h *CapturedHistory) PlanningHistoryCount() int {
	count := 0
	for _, e := range h.Entries {
		if e.Source == HistorySourcePlanningHistory {
			count++
		}
	}
	return count
}

// ParseHistoryEntry parses a JSONL line into a HistoryEntry or HistoryMeta.
func ParseHistoryEntry(line []byte) (*HistoryMeta, *HistoryEntry, error) {
	var probe struct {
		Meta *struct {
			SchemaVersion string `json:"schema_version"`
		} `json:"_meta"`
	}

	if err := json.Unmarshal(line, &probe); err == nil && probe.Meta != nil {
		var metaWrapper struct {
			Meta *HistoryMeta `json:"_meta"`
		}
		if err := json.Unmarshal(line, &metaWrapper); err != nil {
			return nil, nil, fmt.Errorf("parse history meta: %w", err)
		}
		return metaWrapper.Meta, nil, nil
	}

	var entry HistoryEntry
	if err := json.Unmarshal(line, &entry); err != nil {
		return nil, nil, fmt.Errorf("parse history entry: %w", err)
	}

	return nil, &entry, nil
}

// ValidateHistoryEntry checks if a history entry is valid.
func ValidateHistoryEntry(entry *HistoryEntry) error {
	if entry == nil {
		return ErrNilEntry
	}

	if entry.Type == "" {
		return fmt.Errorf("entry type is required")
	}

	if !ValidEntryTypes[entry.Type] {
		return fmt.Errorf("invalid entry type: %s (expected user, assistant, system, or tool)", entry.Type)
	}

	if entry.Type == "tool" && entry.ToolName == "" {
		return fmt.Errorf("tool entries require tool_name")
	}

	return nil
}
