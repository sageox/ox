package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

var (
	// ErrNoAdapterDetected is returned when no adapter can handle the current environment
	ErrNoAdapterDetected = errors.New("no adapter detected for current environment")

	// ErrAdapterNotFound is returned when a specific adapter is not registered
	ErrAdapterNotFound = errors.New("adapter not found")

	// ErrSessionNotFound is returned when a session file cannot be located
	ErrSessionNotFound = errors.New("session file not found")

	// ErrWatchNotSupported is returned when an adapter does not support real-time watching
	ErrWatchNotSupported = errors.New("watch not supported for this adapter")
)

// RawEntry represents a conversation turn from any agent
type RawEntry struct {
	// Timestamp when this entry was created
	Timestamp time.Time

	// Role identifies the speaker: "user", "assistant", "system", "tool"
	Role string

	// Content is the message text or tool output
	Content string

	// ToolName is the name of the tool invoked (only for role="tool")
	ToolName string

	// ToolInput is the input provided to the tool (only for role="tool")
	ToolInput string

	// Raw contains the original data for debugging and auditing
	Raw json.RawMessage
}

// SessionMetadata contains metadata extracted from agent session files.
// This captures which agent and model were used for the session.
type SessionMetadata struct {
	// AgentVersion is the version of the coding agent (e.g., "1.0.3" for Claude Code)
	AgentVersion string

	// Model is the LLM model used (e.g., "claude-sonnet-4-20250514")
	Model string
}

// Adapter reads conversation data from a coding agent's session files
type Adapter interface {
	// Name returns the adapter name (e.g., "claude-code")
	Name() string

	// Detect checks if this adapter can handle the current environment
	// Returns true if the agent's session files are present and readable
	Detect() bool

	// FindSessionFile locates the session file for correlation
	// Called after ox agent prime to find the matching agent session
	// agentID is the unique identifier written during prime
	// since filters to sessions created after this time
	FindSessionFile(agentID string, since time.Time) (string, error)

	// Read reads all entries from a session file
	// Returns entries in chronological order
	Read(sessionPath string) ([]RawEntry, error)

	// ReadMetadata extracts session metadata (agent version, model) from a session file.
	// Returns nil if metadata cannot be determined.
	ReadMetadata(sessionPath string) (*SessionMetadata, error)

	// Watch monitors a session file for new entries (for real-time capture)
	// The returned channel receives entries as they appear
	// The channel is closed when ctx is canceled or an error occurs
	Watch(ctx context.Context, sessionPath string) (<-chan RawEntry, error)
}

// registry holds all registered adapters
var (
	registry   = make(map[string]Adapter)
	registryMu sync.RWMutex
)

// Register adds an adapter to the registry
// Panics if an adapter with the same name is already registered
func Register(adapter Adapter) {
	registryMu.Lock()
	defer registryMu.Unlock()

	name := adapter.Name()
	if _, exists := registry[name]; exists {
		panic(fmt.Sprintf("adapter already registered: %s", name))
	}
	registry[name] = adapter
}

// DetectAdapter finds the appropriate adapter for the current environment
// Iterates through all registered adapters and returns the first one that detects
// Returns ErrNoAdapterDetected if no adapter can handle the environment
func DetectAdapter() (Adapter, error) {
	registryMu.RLock()
	defer registryMu.RUnlock()

	for _, adapter := range registry {
		if adapter.Detect() {
			return adapter, nil
		}
	}
	return nil, ErrNoAdapterDetected
}

// adapterAliases maps common display names and shorthand to canonical adapter names.
// Deep adapters have their own display name aliases. Agents without a deep adapter
// fall back to the generic adapter (remove the alias when a deep adapter is added).
var adapterAliases = map[string]string{
	// deep adapter display names
	"claude code": "claude-code",
	"claude":      "claude-code",

	// generic adapter fallbacks (remove alias when deep adapter is added)
	"codex":    "generic",
	"amp":      "generic",
	"cursor":   "generic",
	"windsurf": "generic",
	"copilot":  "generic",
	"aider":    "generic",
	"cody":     "generic",
	"continue": "generic",
	"cline":    "generic",
	"goose":    "generic",
	"kiro":     "generic",
	"opencode": "generic",
	"droid":    "generic",
}

// GetAdapter returns a specific adapter by name.
// Accepts canonical names ("claude-code"), display names ("Claude Code"),
// and shorthand ("claude"). Case-insensitive for aliases.
// Returns ErrAdapterNotFound if no adapter with that name is registered.
func GetAdapter(name string) (Adapter, error) {
	registryMu.RLock()
	defer registryMu.RUnlock()

	// try exact match first
	if adapter, exists := registry[name]; exists {
		return adapter, nil
	}

	// try case-insensitive alias lookup
	if canonical, ok := adapterAliases[strings.ToLower(name)]; ok {
		if adapter, exists := registry[canonical]; exists {
			return adapter, nil
		}
	}

	return nil, fmt.Errorf("%w: %s", ErrAdapterNotFound, name)
}

// ListAdapters returns the names of all registered adapters
func ListAdapters() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()

	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	return names
}

// ResetRegistry clears all registered adapters (for testing only)
func ResetRegistry() {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry = make(map[string]Adapter)
}
