package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/sageox/ox/pkg/adapterruntime"
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

// SessionLookup contains all parameters needed to locate an agent's session file.
// Constructed from RecordingState at stop/hook time, or from command context at start time.
// Replaces the old (agentID, since) signature that forced ambient repoRoot lookup.
type SessionLookup struct {
	RepoRoot       string    // absolute path to the project root (required, validated)
	AgentID        string    // agent identifier (required)
	Since          time.Time // filter to sessions created after this time (required)
	AgentSessionID string    // adapter-native session UUID if known (optional)
}

// Validate checks that SessionLookup fields are well-formed.
// RepoRoot must be absolute and contain a .sageox/ directory.
// Delegates path validation to adapterruntime.ValidateRepoRoot (single implementation,
// includes 500ms stat timeout for NFS resilience).
func (sl SessionLookup) Validate() error {
	if err := adapterruntime.ValidateRepoRoot(sl.RepoRoot); err != nil {
		return err
	}
	if sl.AgentID == "" {
		return fmt.Errorf("agentID is required")
	}
	return nil
}

// CanonicalAdapterName returns the canonical adapter name for a given name or alias.
// If the name is already canonical or unknown, it is returned as-is.
func CanonicalAdapterName(name string) string {
	lower := strings.ToLower(name)
	if canonical, ok := adapterAliases[lower]; ok {
		return canonical
	}
	return name
}

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

	// ToolOutput is the output from the tool (only for role="tool").
	// Only populated for error results to keep recordings lean.
	ToolOutput string

	// IsError indicates the tool call failed (only for role="tool")
	IsError bool

	// CallID correlates function_call with function_call_output (adapter-specific)
	CallID string

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

	// FindSessionFile locates the session file for correlation.
	// Called after ox agent prime to find the matching agent session.
	// The lookup struct carries all needed context (repoRoot, agentID, since)
	// so the adapter never needs to derive repoRoot from ambient state.
	FindSessionFile(lookup SessionLookup) (string, error)

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

// IncrementalReader is an optional interface for adapters that support
// offset-based incremental reading (used by hook-driven recording).
type IncrementalReader interface {
	ReadFromOffset(path string, offset int64) ([]RawEntry, int64, error)
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

// DetectAdapter finds the appropriate adapter for the current environment.
// First checks registered (built-in) adapters, then falls through to external
// adapter discovery if no registered adapter matches.
// Returns ErrNoAdapterDetected if no adapter can handle the environment.
func DetectAdapter() (Adapter, error) {
	// fast path: check already-registered adapters (built-ins loaded at init time)
	registryMu.RLock()
	for _, adapter := range registry {
		if adapter.Detect() {
			registryMu.RUnlock()
			return adapter, nil
		}
	}
	registryMu.RUnlock()

	// slow path: scan controlled directories (ADR-006) for ox-adapter-* binaries,
	// call `info` on each, and register them. This covers agents that only have
	// an external adapter binary (e.g., gemini, codex) with no built-in.
	if err := RegisterExternalAdapters(); err != nil {
		slog.Debug("external adapter discovery failed during detect", "error", err)
	}

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
	"gemini":      "gemini",
	"gemini-cli":  "gemini",
	"gemini cli":  "gemini",

	// generic adapter fallbacks (remove alias when deep adapter is added)
	"codex":           "codex",
	"amp":             "amp",
	"opencode":        "opencode",
	"pi":              "pi",
	"pi-coding-agent": "pi",
	"cursor":          "generic",
	"windsurf":        "generic",
	"copilot":         "generic",
	"aider":           "aider",
	"cody":            "generic",
	"continue":        "generic",
	"cline":           "generic",
	"goose":           "goose",
	"kiro":            "generic",
	"droid":           "droid",
}

// GetAdapter returns a specific adapter by name.
// Accepts canonical names ("claude-code"), display names ("Claude Code"),
// and shorthand ("claude"). Case-insensitive for aliases.
// Falls through to external adapter discovery if not found in registry.
// Returns ErrAdapterNotFound if no adapter with that name is registered.
func GetAdapter(name string) (Adapter, error) {
	if a := lookupAdapter(name); a != nil {
		return a, nil
	}

	// adapter not in registry — try discovering external binaries before failing.
	// this allows `ox session stop --adapter=gemini` to work even when gemini
	// is only installed as an external adapter binary, not a built-in.
	if err := RegisterExternalAdapters(); err != nil {
		slog.Debug("external adapter discovery failed during get", "error", err)
	}

	if a := lookupAdapter(name); a != nil {
		return a, nil
	}

	return nil, fmt.Errorf("%w: %s", ErrAdapterNotFound, name)
}

// lookupAdapter searches the registry for an adapter by exact name or alias.
func lookupAdapter(name string) Adapter {
	registryMu.RLock()
	defer registryMu.RUnlock()

	if adapter, exists := registry[name]; exists {
		return adapter
	}
	if canonical, ok := adapterAliases[strings.ToLower(name)]; ok {
		if adapter, exists := registry[canonical]; exists {
			return adapter
		}
	}
	return nil
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

// Unregister removes a specific adapter from the registry by name.
// Prefer this over ResetRegistry() in tests to avoid clearing other adapters.
func Unregister(name string) {
	registryMu.Lock()
	defer registryMu.Unlock()
	delete(registry, name)
}
