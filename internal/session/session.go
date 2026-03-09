package session

import "time"

// SessionEntryType represents the type of conversation turn.
type SessionEntryType string

const (
	// SessionEntryTypeUser is a user message or prompt
	SessionEntryTypeUser SessionEntryType = "user"

	// SessionEntryTypeAssistant is an assistant/AI response
	SessionEntryTypeAssistant SessionEntryType = "assistant"

	// SessionEntryTypeSystem is a system message (e.g., context injection)
	SessionEntryTypeSystem SessionEntryType = "system"

	// SessionEntryTypeTool is a tool call or result
	SessionEntryTypeTool SessionEntryType = "tool"
)

// SessionEntry represents a single conversation turn in the session.
type SessionEntry struct {
	// Timestamp is when this entry was recorded
	Timestamp time.Time `json:"ts"`

	// Type identifies the entry type (user, assistant, system, tool)
	Type SessionEntryType `json:"type"`

	// Content is the message or response content
	Content string `json:"content"`

	// ToolName is the name of the tool called (for tool entries)
	ToolName string `json:"tool_name,omitempty"`

	// ToolInput is the input passed to the tool (for tool entries)
	ToolInput string `json:"tool_input,omitempty"`

	// ToolOutput is the output from the tool (for tool entries)
	ToolOutput string `json:"tool_output,omitempty"`

	// CoworkerName identifies the coworker or subagent that contributed to this entry.
	// This includes both team coworkers (loaded via ox coworker load) and built-in
	// Claude Code subagents (invoked via Task tool, e.g., code-reviewer, debugger).
	CoworkerName string `json:"coworker_name,omitempty"`

	// CoworkerModel is the model tier used by the coworker (e.g., sonnet, opus, haiku).
	CoworkerModel string `json:"coworker_model,omitempty"`
}

// Session represents a complete recording session.
// The SessionMeta is stored as the first line of the JSONL file.
// Entries are stored as separate lines following the metadata.
type Session struct {
	// Meta is session context (first line in JSONL)
	Meta *SessionMeta `json:"_meta"`

	// Entries are conversation turns (stored as separate JSONL lines)
	Entries []SessionEntry `json:"-"`
}

// NewSession creates a new session with the given metadata.
func NewSession(meta *SessionMeta) *Session {
	return &Session{
		Meta:    meta,
		Entries: make([]SessionEntry, 0),
	}
}

// NewSessionEntry creates a new entry with the current timestamp.
func NewSessionEntry(entryType SessionEntryType, content string) SessionEntry {
	return SessionEntry{
		Timestamp: time.Now(),
		Type:      entryType,
		Content:   content,
	}
}

// NewToolSessionEntry creates a new tool entry with the current timestamp.
func NewToolSessionEntry(toolName, toolInput, toolOutput string) SessionEntry {
	return SessionEntry{
		Timestamp:  time.Now(),
		Type:       SessionEntryTypeTool,
		ToolName:   toolName,
		ToolInput:  toolInput,
		ToolOutput: toolOutput,
	}
}

// NewUserSessionEntry creates a user entry.
func NewUserSessionEntry(content string) SessionEntry {
	return NewSessionEntry(SessionEntryTypeUser, content)
}

// NewAssistantSessionEntry creates an assistant entry.
func NewAssistantSessionEntry(content string) SessionEntry {
	return NewSessionEntry(SessionEntryTypeAssistant, content)
}

// NewSystemSessionEntry creates a system entry.
func NewSystemSessionEntry(content string) SessionEntry {
	return NewSessionEntry(SessionEntryTypeSystem, content)
}

// NewCoworkerLoadEntry creates a system entry for coworker/subagent load events.
// This enables metrics on which coworkers are being used in sessions.
func NewCoworkerLoadEntry(name, model string) SessionEntry {
	content := "Loaded coworker: " + name
	if model != "" {
		content += " (model: " + model + ")"
	}
	return SessionEntry{
		Timestamp:     time.Now(),
		Type:          SessionEntryTypeSystem,
		Content:       content,
		CoworkerName:  name,
		CoworkerModel: model,
	}
}

// AddEntry appends an entry to the session.
func (s *Session) AddEntry(entry SessionEntry) {
	s.Entries = append(s.Entries, entry)
}

// AddUserEntry appends a user entry to the session.
func (s *Session) AddUserEntry(content string) {
	s.AddEntry(NewUserSessionEntry(content))
}

// AddAssistantEntry appends an assistant entry to the session.
func (s *Session) AddAssistantEntry(content string) {
	s.AddEntry(NewAssistantSessionEntry(content))
}

// AddSystemEntry appends a system entry to the session.
func (s *Session) AddSystemEntry(content string) {
	s.AddEntry(NewSystemSessionEntry(content))
}

// AddToolEntry appends a tool entry to the session.
func (s *Session) AddToolEntry(toolName, toolInput, toolOutput string) {
	s.AddEntry(NewToolSessionEntry(toolName, toolInput, toolOutput))
}

// EntryCount returns the number of entries in the session.
func (s *Session) EntryCount() int {
	return len(s.Entries)
}

// Close finalizes the session by closing the metadata.
func (s *Session) Close() {
	if s.Meta != nil {
		s.Meta.Close()
	}
}

// Footer generates a footer for this session.
func (s *Session) Footer() *SessionFooter {
	if s.Meta == nil {
		return NewSessionFooter(time.Now(), len(s.Entries))
	}
	return NewSessionFooter(s.Meta.StartedAt, len(s.Entries))
}

// IsValid returns true if the entry type is recognized.
func (e SessionEntryType) IsValid() bool {
	switch e {
	case SessionEntryTypeUser, SessionEntryTypeAssistant, SessionEntryTypeSystem, SessionEntryTypeTool:
		return true
	default:
		return false
	}
}

// String returns the string representation of the entry type.
func (e SessionEntryType) String() string {
	return string(e)
}

// Entry is an alias for SessionEntry for backward compatibility and convenience.
type Entry = SessionEntry

// EntryType is an alias for SessionEntryType for backward compatibility.
type EntryType = SessionEntryType

// EntryType constants for backward compatibility.
const (
	EntryTypeUser      = SessionEntryTypeUser
	EntryTypeAssistant = SessionEntryTypeAssistant
	EntryTypeSystem    = SessionEntryTypeSystem
	EntryTypeTool      = SessionEntryTypeTool
)

// Function aliases for backward compatibility
func NewEntry(entryType SessionEntryType, content string) SessionEntry {
	return NewSessionEntry(entryType, content)
}

func NewToolEntry(toolName, toolInput, toolOutput string) SessionEntry {
	return NewToolSessionEntry(toolName, toolInput, toolOutput)
}

func NewUserEntry(content string) SessionEntry {
	return NewUserSessionEntry(content)
}

func NewAssistantEntry(content string) SessionEntry {
	return NewAssistantSessionEntry(content)
}

func NewSystemEntry(content string) SessionEntry {
	return NewSystemSessionEntry(content)
}
