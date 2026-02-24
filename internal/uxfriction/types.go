// Package uxfriction provides CLI friction detection and correction.
//
// This package implements a system for detecting CLI usage errors (typos,
// unknown commands, unknown flags) and providing helpful corrections. It
// supports both human-friendly text output and structured JSON for AI agents.
//
// # Core Concepts
//
//   - FrictionEvent: Captures a CLI usage failure for analytics
//   - Suggestion: Represents a correction suggestion with confidence score
//   - Handler: Processes CLI errors and generates suggestions
//   - Catalog: Stores learned command/token mappings for high-confidence corrections
//
// # Suggestion Chain Priority
//
// When handling a CLI error, suggestions are tried in this order:
//  1. Full command remap from catalog (highest confidence)
//  2. Token-level catalog lookup
//  3. Levenshtein distance fallback (for typos)
//
// # Auto-Execute
//
// High-confidence catalog matches with AutoExecute=true can be automatically
// executed without user confirmation. This enables "desire path" support where
// common patterns are seamlessly corrected.
//
// # Actor Detection
//
// The system distinguishes between human and agent actors to:
//   - Format output appropriately (text vs JSON)
//   - Track analytics by actor type
//   - Potentially adjust behavior based on actor patterns
package uxfriction

import (
	"encoding/json"
	"time"
)

// FailureKind categorizes CLI usage failures for analytics and suggestion routing.
type FailureKind string

const (
	// FailureUnknownCommand indicates an unknown command was entered.
	FailureUnknownCommand FailureKind = "unknown-command"

	// FailureUnknownFlag indicates an unknown flag was provided.
	FailureUnknownFlag FailureKind = "unknown-flag"

	// FailureMissingRequired indicates a required argument or flag is missing.
	FailureMissingRequired FailureKind = "missing-required"

	// FailureInvalidArg indicates an argument has an invalid value.
	FailureInvalidArg FailureKind = "invalid-arg"

	// FailureParseError indicates a general CLI parsing failure.
	FailureParseError FailureKind = "parse-error"
)

// Actor identifies who initiated the command (human user or AI agent).
type Actor string

const (
	// ActorHuman indicates a human user typed the command.
	ActorHuman Actor = "human"

	// ActorAgent indicates an AI coding agent generated the command.
	ActorAgent Actor = "agent"

	// ActorUnknown indicates the actor could not be determined.
	ActorUnknown Actor = "unknown"
)

// SuggestionType indicates the source/method used to generate a suggestion.
// Different types have different confidence levels and behaviors.
type SuggestionType string

const (
	// SuggestionCommandRemap indicates a full command remap from the catalog.
	// These have the highest confidence and may trigger auto-execute.
	SuggestionCommandRemap SuggestionType = "command-remap"

	// SuggestionTokenFix indicates a single token correction from the catalog.
	// Used when a specific token (command/flag name) is misspelled.
	SuggestionTokenFix SuggestionType = "token-fix"

	// SuggestionLevenshtein indicates an edit-distance based guess.
	// Primarily useful for human typos; never auto-executes.
	SuggestionLevenshtein SuggestionType = "levenshtein"
)

// FrictionEvent captures a CLI usage failure for analytics.
// Events are privacy-preserving: inputs are redacted, errors are truncated.
// These events are submitted to the daemon for aggregation and pattern analysis.
//
// Field limits:
//   - Input: max 500 characters
//   - ErrorMsg: max 200 characters
//
// Use Truncate() to enforce these limits before submission.
type FrictionEvent struct {
	// Timestamp in ISO8601 format (RFC3339 UTC).
	Timestamp string `json:"ts"`

	// Kind categorizes the failure type (unknown-command, unknown-flag, invalid-arg, parse-error).
	Kind FailureKind `json:"kind"`

	// Command is the top-level command (e.g., "agent" in "ox agent prime").
	Command string `json:"command,omitempty"`

	// Subcommand is the subcommand if applicable (e.g., "prime" in "ox agent prime").
	Subcommand string `json:"subcommand,omitempty"`

	// Actor identifies who ran the command (human or agent).
	Actor string `json:"actor"`

	// AgentType is the specific agent type when Actor is "agent" (e.g., "claude-code").
	// Omitted when Actor is "human".
	AgentType string `json:"agent_type,omitempty"`

	// Orchestrator is the orchestrator managing the agent (e.g., "openclaw", "conductor").
	// Omitted when no orchestrator is detected.
	Orchestrator string `json:"orchestrator,omitempty"`

	// PathBucket categorizes the working directory (home, repo, other).
	PathBucket string `json:"path_bucket"`

	// Input is the redacted command input (max 500 chars).
	Input string `json:"input"`

	// ErrorMsg is the redacted, truncated error message (max 200 chars).
	ErrorMsg string `json:"error_msg"`

	// Suggestion is an optional suggested correction.
	Suggestion string `json:"suggestion,omitempty"`
}

// Field length limits for FrictionEvent.
const (
	MaxInputLength = 500
	MaxErrorLength = 200
)

// NewFrictionEvent creates a FrictionEvent with current timestamp.
func NewFrictionEvent(kind FailureKind) *FrictionEvent {
	return &FrictionEvent{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Kind:      kind,
	}
}

// Truncate enforces field length limits on the event.
// Call this before submission to ensure compliance with API limits.
func (f *FrictionEvent) Truncate() {
	if len(f.Input) > MaxInputLength {
		f.Input = f.Input[:MaxInputLength]
	}
	if len(f.ErrorMsg) > MaxErrorLength {
		f.ErrorMsg = f.ErrorMsg[:MaxErrorLength]
	}
}

// MarshalJSON returns JSON bytes ready for transmission.
func (f *FrictionEvent) MarshalJSON() ([]byte, error) {
	type Alias FrictionEvent
	return json.Marshal((*Alias)(f))
}

// Suggestion represents a correction suggestion for a CLI error.
// It includes the source type, confidence score, and the corrected command.
type Suggestion struct {
	// Type indicates how the suggestion was generated (catalog, token, or levenshtein).
	Type SuggestionType

	// Original is what the user/agent typed.
	Original string

	// Corrected is the suggested correction.
	Corrected string

	// Confidence is the suggestion confidence score (0.0-1.0).
	// Higher values indicate more reliable suggestions.
	// Auto-execute requires confidence >= AutoExecuteThreshold (0.85).
	Confidence float64

	// Description is an optional human-readable explanation.
	Description string
}

// SuggestContext provides context for generating suggestions.
// It captures the failure details and available valid options for matching.
type SuggestContext struct {
	// Kind is the type of failure (unknown command, unknown flag, etc.).
	Kind FailureKind

	// BadToken is the problematic token that caused the error.
	BadToken string

	// ValidOptions are valid commands/flags to match against.
	ValidOptions []string

	// ParentCmd is the parent command if applicable (e.g., "agent" for "agent prime").
	ParentCmd string
}

// ParsedError contains structured error information extracted from CLI parsing.
// CLIAdapter implementations parse raw errors into this structure.
type ParsedError struct {
	// Kind is the classified failure type.
	Kind FailureKind

	// BadToken is the problematic token that caused the error.
	BadToken string

	// Command is the parent command if known.
	Command string

	// Subcommand is the subcommand if known.
	Subcommand string

	// RawMessage is the original error message.
	RawMessage string
}

// ExecuteAction indicates what action to take with a suggestion.
type ExecuteAction string

const (
	// ActionSuggestOnly shows the suggestion but doesn't execute.
	// Used for lower-confidence suggestions or when auto-execute is disabled.
	ActionSuggestOnly ExecuteAction = "suggest"

	// ActionAutoExecute executes the corrected command automatically.
	// Only used for high-confidence catalog matches with AutoExecute=true.
	ActionAutoExecute ExecuteAction = "auto_execute"
)

// AutoExecuteResult contains the outcome of friction handling with execution decision.
// It includes the suggestion, friction event for analytics, and the action to take.
type AutoExecuteResult struct {
	// Suggestion contains the correction suggestion (may be nil if no suggestion found).
	Suggestion *Suggestion

	// Event contains the friction event for analytics submission.
	Event *FrictionEvent

	// Action indicates whether to suggest only or auto-execute.
	Action ExecuteAction

	// CorrectedArgs contains the args to re-execute with (if Action == ActionAutoExecute).
	// These are the parsed arguments from the corrected command string.
	CorrectedArgs []string

	// Mapping contains the original catalog mapping (if suggestion came from catalog).
	// Nil for Levenshtein suggestions.
	Mapping *CommandMapping
}

// FrictionResponse represents the API response from friction event submission.
type FrictionResponse struct {
	// Accepted is the number of events successfully processed.
	Accepted int `json:"accepted"`

	// Catalog contains catalog data if updated or client version is stale.
	// nil if catalog version matches X-Catalog-Version header.
	Catalog *CatalogData `json:"catalog,omitempty"`
}
