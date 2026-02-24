package uxfriction

import (
	"encoding/json"
	"os"
	"strings"

	"github.com/sageox/ox/pkg/agentx"
)

// AutoExecuteThreshold is the minimum confidence for auto-execute.
// Only catalog entries with auto_execute: true AND confidence >= this threshold
// will be auto-executed. Levenshtein suggestions never auto-execute.
//
// This threshold is intentionally high (0.85) to avoid surprising users with
// incorrect auto-corrections. The value was chosen to balance:
//   - High enough to avoid false positives
//   - Low enough to catch confident catalog matches
//
// Adjustments should be data-driven based on analytics of correction accuracy.
const AutoExecuteThreshold = 0.85

// PathBucket values for categorizing working directory
const (
	PathBucketHome  = "home"
	PathBucketRepo  = "repo"
	PathBucketOther = "other"
)

// Result contains the outcome of handling a CLI error.
// Deprecated: Use AutoExecuteResult for full auto-execute support.
type Result struct {
	// Event is the friction event for analytics.
	Event *FrictionEvent

	// Suggestion is the correction suggestion (may be nil).
	Suggestion *Suggestion
}

// Handler processes CLI errors and generates friction events with suggestions.
// It uses a CLIAdapter to parse errors and a Catalog to look up corrections.
type Handler struct {
	adapter CLIAdapter
	engine  *SuggestionEngine
}

// NewHandler creates a Handler with the given adapter and catalog.
// The adapter is used to parse CLI errors and retrieve valid command/flag names.
// The catalog provides learned mappings for high-confidence corrections.
func NewHandler(adapter CLIAdapter, catalog Catalog) *Handler {
	return &Handler{
		adapter: adapter,
		engine:  NewSuggestionEngine(catalog),
	}
}

// Handle processes CLI args and error, returning a Result with event and suggestion.
// Returns nil if the error is not a parseable CLI error.
//
// Deprecated: Use HandleWithAutoExecute for full auto-execute support.
// This method is retained for backward compatibility but doesn't return
// auto-execute decisions or corrected args.
func (h *Handler) Handle(args []string, err error) *Result {
	result := h.HandleWithAutoExecute(args, err)
	if result == nil {
		return nil
	}
	return &Result{
		Event:      result.Event,
		Suggestion: result.Suggestion,
	}
}

// HandleWithAutoExecute processes CLI args and error, returning an AutoExecuteResult
// with execution decision based on catalog mappings and confidence thresholds.
//
// The method:
//  1. Parses the error using the CLIAdapter
//  2. Looks up suggestions in order: command remap → token fix → Levenshtein
//  3. Determines if auto-execute is appropriate (catalog match + high confidence)
//  4. Builds a friction event for analytics
//
// Returns nil if the error cannot be parsed by the adapter.
func (h *Handler) HandleWithAutoExecute(args []string, err error) *AutoExecuteResult {
	parsed := h.adapter.ParseError(err)
	if parsed == nil {
		return nil
	}

	// build full command string from args
	fullCommand := strings.Join(args, " ")

	// get valid options based on failure kind
	var validOptions []string
	switch parsed.Kind {
	case FailureUnknownCommand:
		validOptions = h.adapter.CommandNames()
	case FailureUnknownFlag:
		validOptions = h.adapter.FlagNames(parsed.Command)
	default:
		// for other failure kinds, use commands as fallback
		validOptions = h.adapter.CommandNames()
	}

	// get suggestion from engine
	ctx := SuggestContext{
		Kind:         parsed.Kind,
		BadToken:     parsed.BadToken,
		ValidOptions: validOptions,
		ParentCmd:    parsed.Command,
	}
	suggestion, mapping := h.engine.SuggestForCommandWithMapping(fullCommand, ctx)

	// detect actor
	actor, agentType := DetectActor()

	// build friction event
	event := NewFrictionEvent(parsed.Kind)
	event.Command = parsed.Command
	event.Subcommand = parsed.Subcommand
	event.Actor = string(actor)
	if actor == ActorAgent && agentType != "" {
		event.AgentType = agentType
	}
	if orchType := agentx.OrchestratorType(); orchType != "" {
		event.Orchestrator = orchType
	}
	event.PathBucket = detectPathBucket()
	event.Input = RedactInput(args)
	event.ErrorMsg = RedactError(parsed.RawMessage, MaxErrorLength)
	event.Truncate()

	// determine action based on mapping and confidence
	action := ActionSuggestOnly
	var correctedArgs []string

	if suggestion != nil && mapping != nil {
		// only auto-execute for catalog mappings with auto_execute flag
		if mapping.AutoExecute && suggestion.Confidence >= AutoExecuteThreshold {
			action = ActionAutoExecute
			correctedArgs = parseArgs(suggestion.Corrected)
		}
	}

	return &AutoExecuteResult{
		Suggestion:    suggestion,
		Event:         event,
		Action:        action,
		CorrectedArgs: correctedArgs,
		Mapping:       mapping,
	}
}

// parseArgs splits a command string into args.
// Currently uses simple whitespace splitting; quoted strings may need
// enhancement if command arguments contain spaces.
func parseArgs(command string) []string {
	return strings.Fields(command)
}

// detectPathBucket categorizes the current working directory.
// Returns "home" if in home directory, "repo" if in a git repo, "other" otherwise.
func detectPathBucket() string {
	cwd, err := os.Getwd()
	if err != nil {
		return PathBucketOther
	}

	// check if in home directory
	home, err := os.UserHomeDir()
	if err == nil && strings.HasPrefix(cwd, home) {
		// check if this is a git repo
		if isGitRepo(cwd) {
			return PathBucketRepo
		}
		return PathBucketHome
	}

	// check if it's a git repo outside home
	if isGitRepo(cwd) {
		return PathBucketRepo
	}

	return PathBucketOther
}

// isGitRepo checks if the given directory is inside a git repository.
func isGitRepo(dir string) bool {
	// walk up to find .git directory
	for {
		gitPath := dir + "/.git"
		if info, err := os.Stat(gitPath); err == nil && info.IsDir() {
			return true
		}

		parent := dir[:strings.LastIndex(dir, "/")]
		if parent == "" || parent == dir {
			break
		}
		dir = parent
	}
	return false
}

// DetectActor determines if the current context is human, agent, or CI.
// Returns the actor type and agent type string (empty if not an agent).
//
// Detection order:
//  1. Check for coding agent context (CLAUDE_CODE, etc.)
//  2. Check for CI environment (CI env var)
//  3. Default to human
//
// The agent type is the specific agent identifier (e.g., "claude-code", "cursor", "ci").
func DetectActor() (Actor, string) {
	// check for coding agent context
	if agentx.IsAgentContext() {
		agent := agentx.CurrentAgent()
		if agent != nil {
			return ActorAgent, string(agent.Type())
		}
		return ActorAgent, ""
	}

	// check for CI environment as fallback (treat CI as agent)
	if ci := os.Getenv("CI"); ci != "" {
		return ActorAgent, "ci"
	}

	return ActorHuman, ""
}

// FormatSuggestion formats a suggestion for output.
// If jsonMode is true, returns a JSON object with type, suggestion, and confidence.
// Otherwise returns human-friendly text ("Did you mean this?\n    <suggestion>").
//
// Returns empty string if the suggestion is nil.
func FormatSuggestion(s *Suggestion, jsonMode bool) string {
	if s == nil {
		return ""
	}

	if jsonMode {
		output := map[string]any{
			"type":       string(s.Type),
			"suggestion": s.Corrected,
			"confidence": s.Confidence,
		}
		if s.Description != "" {
			output["description"] = s.Description
		}

		data, err := json.Marshal(output)
		if err != nil {
			return ""
		}
		return string(data)
	}

	// human-friendly format
	return "Did you mean this?\n    " + s.Corrected
}
