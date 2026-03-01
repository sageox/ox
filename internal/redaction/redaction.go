package redaction

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// LLMClient abstracts LLM calls for redaction.
type LLMClient interface {
	Redact(ctx context.Context, content string, guidance string) (string, error)
}

// RedactOpts configures a redaction call.
type RedactOpts struct {
	Integration string // optional: "slack", "jira", etc.
}

// Redactor handles observation redaction via LLM.
type Redactor struct {
	teamContextDir string
	llmClient      LLMClient
	mu             sync.RWMutex
	guidanceCache  map[string]string // key: integration name ("" = team default)
}

// NewRedactor creates a redactor backed by the given LLM client.
// teamContextDir is the path to the team context checkout.
func NewRedactor(teamContextDir string, client LLMClient) *Redactor {
	return &Redactor{
		teamContextDir: teamContextDir,
		llmClient:      client,
		guidanceCache:  make(map[string]string),
	}
}

// Redact processes a single content string through LLM redaction.
// Returns error if LLM is unavailable — caller should block the write.
func (r *Redactor) Redact(ctx context.Context, content string, opts RedactOpts) (string, error) {
	if content == "" {
		return "", nil
	}

	guidance, err := r.LoadGuidance(opts.Integration)
	if err != nil {
		return "", fmt.Errorf("redaction: load guidance: %w", err)
	}

	result, err := r.llmClient.Redact(ctx, content, guidance)
	if err != nil {
		// retry once
		slog.Warn("redaction: first attempt failed, retrying", "error", err)
		result, err = r.llmClient.Redact(ctx, content, guidance)
		if err != nil {
			return "", fmt.Errorf("redaction: llm failed after retry: %w", err)
		}
	}

	return result, nil
}

const entrySeparator = "\n---ENTRY_SEPARATOR---\n"

// RedactBatch processes multiple entries in a single LLM call.
func (r *Redactor) RedactBatch(ctx context.Context, entries []string, opts RedactOpts) ([]string, error) {
	if len(entries) == 0 {
		return nil, nil
	}

	// filter empty entries
	var nonEmpty []string
	emptyIdx := make(map[int]bool)
	for i, e := range entries {
		if e == "" {
			emptyIdx[i] = true
		} else {
			nonEmpty = append(nonEmpty, e)
		}
	}

	if len(nonEmpty) == 0 {
		return make([]string, len(entries)), nil
	}

	combined := strings.Join(nonEmpty, entrySeparator)

	redacted, err := r.Redact(ctx, combined, opts)
	if err != nil {
		return nil, err
	}

	parts := strings.Split(redacted, entrySeparator)

	// rebuild result with empty entries in original positions
	result := make([]string, len(entries))
	partIdx := 0
	for i := range entries {
		if emptyIdx[i] {
			result[i] = ""
		} else if partIdx < len(parts) {
			result[i] = strings.TrimSpace(parts[partIdx])
			partIdx++
		}
	}

	return result, nil
}

// LoadGuidance loads and merges REDACT.md files, caching the result.
func (r *Redactor) LoadGuidance(integration string) (string, error) {
	cacheKey := integration

	r.mu.RLock()
	if cached, ok := r.guidanceCache[cacheKey]; ok {
		r.mu.RUnlock()
		return cached, nil
	}
	r.mu.RUnlock()

	r.mu.Lock()
	defer r.mu.Unlock()

	// double-check after acquiring write lock
	if cached, ok := r.guidanceCache[cacheKey]; ok {
		return cached, nil
	}

	// load team-wide defaults
	teamPath := filepath.Join(r.teamContextDir, "docs", "governance", "REDACT.md")
	teamGuidance, err := os.ReadFile(teamPath)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("redaction: cannot read team REDACT.md", "path", teamPath, "error", err)
		}
	}

	var parts []string
	if len(teamGuidance) > 0 {
		parts = append(parts, string(teamGuidance))
	}

	// load integration-specific guidance
	if integration != "" {
		intPath := filepath.Join(r.teamContextDir, "data", integration, "REDACT.md")
		intGuidance, err := os.ReadFile(intPath)
		if err != nil {
			if !os.IsNotExist(err) {
				slog.Warn("redaction: cannot read integration REDACT.md", "path", intPath, "error", err)
			}
		}
		if len(intGuidance) > 0 {
			parts = append(parts, "## Integration-Specific Overrides (take precedence)\n\n"+string(intGuidance))
		}
	}

	var guidance string
	if len(parts) > 0 {
		guidance = strings.Join(parts, "\n\n")
	} else {
		slog.Warn("redaction: no REDACT.md files found, using default guidance")
		guidance = DefaultGuidance()
	}

	r.guidanceCache[cacheKey] = guidance
	return guidance, nil
}

// InvalidateCache clears the guidance cache. Call after sync.
func (r *Redactor) InvalidateCache() {
	r.mu.Lock()
	r.guidanceCache = make(map[string]string)
	r.mu.Unlock()
}

// DefaultGuidance returns hardcoded redaction rules used when
// no REDACT.md files are available.
func DefaultGuidance() string {
	return `# Redaction Guidance

## Always Redact
- API keys, secrets, tokens → replace with [REDACTED_SECRET]
- Email addresses → replace with [email]
- Phone numbers → replace with [phone]
- Physical addresses → replace with [address]
- Customer/person names → replace with generic labels (Person A, Person B)
- Social security numbers, credit card numbers → replace with [REDACTED_PII]

## Preserve
- Technical decisions and rationale
- Code references and file paths
- Architecture patterns and design choices
- Error messages (redact any PII within them)
- Internal project codenames`
}
