package uxfriction

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// CommandMapping represents a full command remap from learned patterns.
// Mappings can be literal strings or regex patterns with capture groups.
//
// Example literal mapping:
//
//	Pattern: "daemons list --every"
//	Target:  "daemons show --all"
//
// Example regex mapping:
//
//	Pattern: "agent close ([a-zA-Z0-9-]+)"
//	Target:  "agent $1 session stop"
//	HasRegex: true
type CommandMapping struct {
	// Pattern is the input pattern to match. For literal patterns, this is an
	// exact command (minus "ox" prefix). For regex patterns, this is a regex.
	Pattern string `json:"pattern"`

	// Target is the corrected command. For regex patterns, $1, $2, etc. are
	// replaced with capture group values.
	Target string `json:"target"`

	// HasRegex is true if Pattern is a regex rather than a literal string.
	HasRegex bool `json:"has_regex"`

	// AutoExecute is true if this mapping is safe to auto-execute without confirmation.
	// Only high-confidence, curated patterns should have this enabled.
	AutoExecute bool `json:"auto_execute"`

	// Count is the number of times this pattern has been seen (for analytics).
	Count int `json:"count"`

	// Confidence is the match confidence (0.0-1.0). Must be >= AutoExecuteThreshold
	// for auto-execute to trigger.
	Confidence float64 `json:"confidence"`

	// Description is an optional human-readable explanation.
	Description string `json:"description"`

	// compiledRegex is the compiled regex, populated lazily on first match attempt.
	compiledRegex *regexp.Regexp
}

// ApplyMapping applies the mapping to input, returning corrected command.
// For regex patterns, captures are substituted into target ($1, $2, etc).
// Returns the corrected command and true if matched, empty string and false otherwise.
func (m *CommandMapping) ApplyMapping(input string) (string, bool) {
	if !m.HasRegex {
		// literal match - return target as-is
		return m.Target, true
	}

	// compile regex if not already done
	if m.compiledRegex == nil {
		re, err := regexp.Compile(m.Pattern)
		if err != nil {
			return "", false
		}
		m.compiledRegex = re
	}

	match := m.compiledRegex.FindStringSubmatch(input)
	if match == nil {
		return "", false
	}

	// substitute captures into target
	result := m.Target
	for i, capture := range match[1:] {
		placeholder := fmt.Sprintf("$%d", i+1)
		result = strings.ReplaceAll(result, placeholder, capture)
	}

	return result, true
}

// TokenMapping represents a single-token correction from learned patterns.
// Unlike CommandMapping, this corrects a single token (command name, flag name)
// rather than the entire command string.
type TokenMapping struct {
	// Pattern is the misspelled token (e.g., "depliy").
	Pattern string `json:"pattern"`

	// Target is the corrected token (e.g., "deploy").
	Target string `json:"target"`

	// Kind is the failure category this mapping applies to.
	// Token mappings are kind-specific to avoid incorrect corrections.
	Kind FailureKind `json:"kind"`

	// Count is the number of times this pattern has been seen (for analytics).
	Count int `json:"count"`

	// Confidence is the match confidence (0.0-1.0).
	Confidence float64 `json:"confidence"`
}

// CatalogData contains all learned mappings for serialization.
// This is the wire format for catalog updates from the server.
type CatalogData struct {
	// Version is the catalog version identifier.
	Version string `json:"version"`

	// Commands contains full command remappings.
	Commands []CommandMapping `json:"commands"`

	// Tokens contains single-token corrections.
	Tokens []TokenMapping `json:"tokens"`
}

// Catalog provides lookup for learned command and token mappings.
// Implementations must be thread-safe for concurrent access.
type Catalog interface {
	// LookupCommand finds a command mapping for the given input.
	// The input is normalized (ox prefix stripped, flags sorted) before lookup.
	// Returns nil if no mapping found.
	LookupCommand(input string) *CommandMapping

	// LookupToken finds a token mapping for the given token and failure kind.
	// The lookup is case-insensitive.
	// Returns nil if no mapping found.
	LookupToken(token string, kind FailureKind) *TokenMapping

	// Update replaces all catalog data with new data.
	// This is an atomic operation.
	Update(data CatalogData) error

	// Version returns the current catalog version.
	Version() string
}

// FrictionCatalog implements Catalog with thread-safe in-memory storage.
// It supports both literal command mappings (O(1) lookup) and regex patterns
// (O(n) iterative matching).
type FrictionCatalog struct {
	mu sync.RWMutex

	// version is the current catalog version.
	version string

	// commands maps normalized command patterns to mappings (literal patterns only).
	// Normalization strips "ox" prefix and sorts flags for consistent matching.
	commands map[string]*CommandMapping

	// regexCommands stores regex patterns that need iterative matching.
	// These are tried after literal patterns fail.
	regexCommands []*CommandMapping

	// tokens maps "token:kind" to mappings for efficient lookup.
	// The key format is "lowercased_token:failure_kind".
	tokens map[string]*TokenMapping
}

// NewFrictionCatalog creates an empty FrictionCatalog ready for updates.
func NewFrictionCatalog() *FrictionCatalog {
	return &FrictionCatalog{
		version:       "",
		commands:      make(map[string]*CommandMapping),
		regexCommands: nil,
		tokens:        make(map[string]*TokenMapping),
	}
}

// LookupCommand finds a command mapping for the given input.
// Tries literal patterns first (O(1)), then regex patterns (O(n)).
func (c *FrictionCatalog) LookupCommand(input string) *CommandMapping {
	c.mu.RLock()
	defer c.mu.RUnlock()

	// try literal patterns first (fast path)
	normalized := normalizeCommand(input)
	if mapping := c.commands[normalized]; mapping != nil {
		return mapping
	}

	// try regex patterns (slower, iterative)
	// strip "ox" prefix for regex matching
	normalizedInput := normalized
	if normalizedInput == "" {
		normalizedInput = strings.TrimPrefix(strings.TrimSpace(input), "ox ")
	}

	for _, mapping := range c.regexCommands {
		// compiledRegex is pre-compiled by Update(); skip if somehow nil
		if mapping.compiledRegex == nil {
			continue
		}
		if mapping.compiledRegex.MatchString(normalizedInput) {
			return mapping
		}
	}

	return nil
}

// LookupToken finds a token mapping for the given token and failure kind.
func (c *FrictionCatalog) LookupToken(token string, kind FailureKind) *TokenMapping {
	c.mu.RLock()
	defer c.mu.RUnlock()

	key := tokenKey(token, kind)
	return c.tokens[key]
}

// Update replaces all catalog data with new data.
func (c *FrictionCatalog) Update(data CatalogData) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	// separate literal and regex patterns
	commands := make(map[string]*CommandMapping)
	var regexCommands []*CommandMapping

	for i := range data.Commands {
		mapping := &data.Commands[i]
		if mapping.HasRegex {
			// pre-compile regex for validation
			re, err := regexp.Compile(mapping.Pattern)
			if err != nil {
				// skip invalid regex patterns
				continue
			}
			mapping.compiledRegex = re
			regexCommands = append(regexCommands, mapping)
		} else {
			normalized := normalizeCommand(mapping.Pattern)
			commands[normalized] = mapping
		}
	}

	// rebuild token map
	tokens := make(map[string]*TokenMapping, len(data.Tokens))
	for i := range data.Tokens {
		mapping := &data.Tokens[i]
		key := tokenKey(mapping.Pattern, mapping.Kind)
		tokens[key] = mapping
	}

	c.version = data.Version
	c.commands = commands
	c.regexCommands = regexCommands
	c.tokens = tokens

	return nil
}

// Version returns the current catalog version.
func (c *FrictionCatalog) Version() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.version
}

// normalizeCommand strips "ox" prefix and sorts flags for consistent matching.
// Example: "ox agent list --verbose -a" -> "agent list -a --verbose"
func normalizeCommand(input string) string {
	input = strings.TrimSpace(input)
	if input == "" {
		return ""
	}

	parts := strings.Fields(input)
	if len(parts) == 0 {
		return ""
	}

	// strip "ox" prefix if present
	if parts[0] == "ox" {
		parts = parts[1:]
	}

	if len(parts) == 0 {
		return ""
	}

	// separate positional args from flags
	var positionals []string
	var flags []string

	for _, part := range parts {
		if strings.HasPrefix(part, "-") {
			flags = append(flags, part)
		} else {
			positionals = append(positionals, part)
		}
	}

	// sort flags for consistent matching
	sort.Strings(flags)

	// reconstruct: positionals first, then sorted flags
	result := make([]string, 0, len(positionals)+len(flags))
	result = append(result, positionals...)
	result = append(result, flags...)

	return strings.Join(result, " ")
}

// tokenKey creates a lookup key for token mappings.
func tokenKey(token string, kind FailureKind) string {
	return strings.ToLower(token) + ":" + string(kind)
}
