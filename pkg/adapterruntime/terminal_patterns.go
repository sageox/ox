package adapterruntime

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/sageox/ox/pkg/adapterprotocol"
	"gopkg.in/yaml.v3"
)

// DefaultSilenceWindow is the time the detector waits after a pattern
// match before confirming the session is terminal. If new entries
// arrive in this window — and they are not themselves matches —
// the pending match is revoked. Defeats the failure mode where a
// rendered prior transcript quotes a rate-limit line.
const DefaultSilenceWindow = 20 * time.Second

// MaxRawMessageBytes caps how much of the matched line is forwarded to
// the daemon over the wire. Long lines are truncated to this length to
// keep telemetry / audit records compact and to avoid leaking secrets
// that may sit on the same line (the boundary guard in upstream code
// strips before write, but defense-in-depth).
const MaxRawMessageBytes = 512

// PatternSource categorizes how a match was found.
type PatternSource string

const (
	SourceStructured PatternSource = "structured"
	SourceRegex      PatternSource = "regex"
	SourceExitCode   PatternSource = "exit_code"
)

// StructuredCheck inspects a raw line (parsed by the adapter to a RawEntry,
// plus the original JSON bytes for free-form path lookups). Returns a
// non-nil Match when the entry matches the structured signal, or nil
// otherwise. Structured checks are the preferred detection path: they
// look at vendor-supplied fields (e.g. message.stop_reason) rather than
// guessing at free-text wording that vendors change quietly.
type StructuredCheck func(entry adapterprotocol.RawEntry, raw json.RawMessage) *Match

// TerminalPattern declares one detection rule for an adapter. Patterns
// are evaluated in registration order; the first match wins for a given
// entry. Structured patterns are always checked before regex patterns
// on the same entry — see TerminalDetector.checkEntry.
type TerminalPattern struct {
	ID            string
	Reason        string // empty means "log + metric, do not finalize"
	Source        PatternSource
	Structured    StructuredCheck
	Substrings    []string // cheap pre-filter for regex patterns; ANY substring must be present before Re is evaluated
	Re            *regexp.Regexp
	Roles         []string // for regex patterns: gate to these entry.Role values (default {"system"})
	ParseResetsAt func(match []string) (raw string, parsed *time.Time)
}

// Match is what a TerminalPattern.Check returns when an entry matches.
// The detector populates additional fields (Source, ConfirmedAt) before
// emitting the terminal_error event.
type Match struct {
	PatternID   string
	Reason      string
	RawMessage  string
	ResetsAtRaw string
	ResetsAt    *time.Time
}

// Metrics is the observability surface for the detector. The watcher
// (or test harness) injects an implementation. Production wires this
// to OTLP counters / gauges; the default NopMetrics drops everything.
//
// PatternHit fires for every match found (before silence-window
// confirmation). PatternConfirmed fires when the silence window
// elapses without a revoking entry. PatternRevoked fires when a
// non-matching entry arrives before the window elapses (false
// positive caught). ResetsAtParseFailure fires when a pattern matched
// but ParseResetsAt could not produce an absolute timestamp.
type Metrics interface {
	PatternHit(adapter, patternID string, source PatternSource, reason string)
	PatternConfirmed(adapter, patternID string)
	PatternRevoked(adapter, patternID string)
	ResetsAtParseFailure(adapter, patternID string)
	SilenceWindowObservedMs(adapter, patternID string, observed time.Duration)
}

// NopMetrics is a Metrics that discards all events.
type NopMetrics struct{}

func (NopMetrics) PatternHit(string, string, PatternSource, string)      {}
func (NopMetrics) PatternConfirmed(string, string)                       {}
func (NopMetrics) PatternRevoked(string, string)                         {}
func (NopMetrics) ResetsAtParseFailure(string, string)                   {}
func (NopMetrics) SilenceWindowObservedMs(string, string, time.Duration) {}

// ConfirmedMatch is what Tick returns when a pending match's silence
// window has elapsed. Translates 1:1 to a terminal_error event.
type ConfirmedMatch struct {
	SessionID   string
	Pattern     TerminalPattern
	Match       Match
	EntrySeq    int64
	DetectedAt  time.Time
	ConfirmedAt time.Time
}

// pendingMatch holds the state of a match awaiting silence-window
// confirmation. firstSeenSeq is the entries-batch Seq the originating
// match was observed in; the daemon's terminal-error handler gates
// finalize on "entries with Seq <= firstSeenSeq have been persisted".
type pendingMatch struct {
	pattern      TerminalPattern
	match        Match
	firstSeenAt  time.Time
	firstSeenSeq int64
}

// TerminalDetector evaluates a list of patterns against every entry the
// watcher pushes, holds matches in a silence-window pending state, and
// emits confirmed matches via Tick.
//
// Thread-safe: the watcher calls OnBatch from its read goroutine and
// Tick from a heartbeat goroutine concurrently.
type TerminalDetector struct {
	adapterName   string
	patterns      []TerminalPattern
	silenceWindow time.Duration
	metrics       Metrics

	mu      sync.Mutex
	pending map[string]*pendingMatch // sessionID -> match awaiting confirmation
}

// NewTerminalDetector constructs a detector with the given patterns.
// adapterName is used for metric labeling. Passing nil patterns yields
// a no-op detector that is safe to call (handy for the disabled case).
func NewTerminalDetector(adapterName string, patterns []TerminalPattern, silenceWindow time.Duration, metrics Metrics) *TerminalDetector {
	if metrics == nil {
		metrics = NopMetrics{}
	}
	if silenceWindow <= 0 {
		silenceWindow = DefaultSilenceWindow
	}
	return &TerminalDetector{
		adapterName:   adapterName,
		patterns:      patterns,
		silenceWindow: silenceWindow,
		metrics:       metrics,
		pending:       make(map[string]*pendingMatch),
	}
}

// Enabled reports whether the detector has any patterns to evaluate.
// Callers can skip OnBatch/Tick wiring entirely when false.
func (d *TerminalDetector) Enabled() bool {
	return d != nil && len(d.patterns) > 0
}

// OnBatch evaluates entries (in order) against the registered patterns.
// If any entry matches, the match is stored as pending for this session.
// If a non-matching entry arrives while a match is pending, the pending
// match is revoked — vendor messages that look like a rate-limit but are
// followed by more conversational output were a false positive.
//
// rawLines is parallel to entries (same length) carrying the original
// JSON bytes for structured-pass JSON-path checks. Pass nil for adapters
// that cannot supply raws; only structured patterns that look at
// RawEntry fields will fire in that case.
//
// seq is the per-session monotonically increasing batch identifier from
// the entries event. The first session-batch should be 1, never 0
// (0 reserved for "unknown").
func (d *TerminalDetector) OnBatch(sessionID string, entries []adapterprotocol.RawEntry, seq int64, rawLines []json.RawMessage) {
	if !d.Enabled() || sessionID == "" || len(entries) == 0 {
		return
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	for i, entry := range entries {
		var raw json.RawMessage
		if i < len(rawLines) {
			raw = rawLines[i]
		}
		if pattern, match := d.checkEntry(entry, raw); match != nil {
			// New match. If something was already pending and this is
			// a different pattern, the newer match supersedes — same
			// session, same vendor, terminal condition still applies.
			d.pending[sessionID] = &pendingMatch{
				pattern:      pattern,
				match:        *match,
				firstSeenAt:  time.Now(),
				firstSeenSeq: seq,
			}
			d.metrics.PatternHit(d.adapterName, pattern.ID, pattern.Source, pattern.Reason)
		} else if existing, ok := d.pending[sessionID]; ok {
			// Non-matching entry arrived while a match was pending.
			// False positive: revoke and let the session continue.
			d.metrics.PatternRevoked(d.adapterName, existing.pattern.ID)
			delete(d.pending, sessionID)
		}
	}
}

// Tick checks every pending match and returns the ones whose silence
// window has elapsed. Caller (typically the watcher heartbeat) emits
// a terminal_error event for each ConfirmedMatch.
func (d *TerminalDetector) Tick(now time.Time) []ConfirmedMatch {
	if !d.Enabled() {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	var confirmed []ConfirmedMatch
	for sessionID, p := range d.pending {
		elapsed := now.Sub(p.firstSeenAt)
		if elapsed < d.silenceWindow {
			continue
		}
		// Skip empty-Reason patterns: those are informational only
		// (e.g. log max_tokens stop_reason for telemetry) — they
		// should not finalize the session.
		if p.pattern.Reason != "" {
			confirmed = append(confirmed, ConfirmedMatch{
				SessionID:   sessionID,
				Pattern:     p.pattern,
				Match:       p.match,
				EntrySeq:    p.firstSeenSeq,
				DetectedAt:  p.firstSeenAt,
				ConfirmedAt: now,
			})
			d.metrics.PatternConfirmed(d.adapterName, p.pattern.ID)
			d.metrics.SilenceWindowObservedMs(d.adapterName, p.pattern.ID, elapsed)
		}
		delete(d.pending, sessionID)
	}
	return confirmed
}

// Forget drops any pending state for the given session. The watcher
// calls this on Unwatch so a re-Watch under the same agentID does not
// inherit a pending match from a previous session.
func (d *TerminalDetector) Forget(sessionID string) {
	if !d.Enabled() {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.pending, sessionID)
}

// checkEntry walks the pattern list and returns the first match. caller must hold d.mu.
func (d *TerminalDetector) checkEntry(entry adapterprotocol.RawEntry, raw json.RawMessage) (TerminalPattern, *Match) {
	// Structured patterns first (preferred path).
	for _, p := range d.patterns {
		if p.Source != SourceStructured || p.Structured == nil {
			continue
		}
		if m := p.Structured(entry, raw); m != nil {
			m.PatternID = p.ID
			if m.Reason == "" {
				m.Reason = p.Reason
			}
			truncateRawMessage(m)
			return p, m
		}
	}
	// Regex fallback.
	for _, p := range d.patterns {
		if p.Source != SourceRegex || p.Re == nil {
			continue
		}
		if !roleAllowed(entry.Role, p.Roles) {
			continue
		}
		// Cheap substring pre-filter — short-circuits the regex when
		// the line cannot possibly match. Empty Substrings means
		// "always run the regex".
		if !substringPresent(entry.Content, p.Substrings) {
			continue
		}
		subs := p.Re.FindStringSubmatch(entry.Content)
		if subs == nil {
			continue
		}
		m := &Match{
			PatternID:  p.ID,
			Reason:     p.Reason,
			RawMessage: entry.Content,
		}
		if p.ParseResetsAt != nil {
			raw, parsed := p.ParseResetsAt(subs)
			m.ResetsAtRaw = raw
			m.ResetsAt = parsed
			if raw != "" && parsed == nil {
				d.metrics.ResetsAtParseFailure(d.adapterName, p.ID)
			}
		}
		truncateRawMessage(m)
		return p, m
	}
	return TerminalPattern{}, nil
}

func truncateRawMessage(m *Match) {
	if len(m.RawMessage) > MaxRawMessageBytes {
		m.RawMessage = m.RawMessage[:MaxRawMessageBytes]
	}
}

// roleAllowed returns true when entry's role is in allowed. Default
// allowed for regex patterns is {"system"} when the pattern declares
// no Roles (the safer default — system messages are adapter-authored,
// assistant messages can contain user-quoted text).
func roleAllowed(role string, allowed []string) bool {
	if len(allowed) == 0 {
		return role == adapterprotocol.RoleSystem
	}
	for _, a := range allowed {
		if role == a {
			return true
		}
	}
	return false
}

func substringPresent(content string, substrings []string) bool {
	if len(substrings) == 0 {
		return true
	}
	lower := strings.ToLower(content)
	for _, s := range substrings {
		if strings.Contains(lower, strings.ToLower(s)) {
			return true
		}
	}
	return false
}

// --- YAML pattern catalog ---

// YAMLCatalog is the on-disk representation of an adapter's pattern set.
// Adapters embed a YAML file via go:embed and pass the bytes to
// LoadYAMLPatterns to get back TerminalPattern values registered with
// a Go-resident StructuredCheck registry.
type YAMLCatalog struct {
	Version  int                `yaml:"version"`
	Patterns []YAMLPatternEntry `yaml:"patterns"`
}

// YAMLPatternEntry is a single pattern in the catalog file. Structured
// patterns reference a named check function from a registry (string
// indirection so the YAML stays declarative); regex patterns inline
// their regex + parser hint.
type YAMLPatternEntry struct {
	ID         string            `yaml:"id"`
	Source     PatternSource     `yaml:"source"`
	Reason     string            `yaml:"reason"`
	Roles      []string          `yaml:"roles,omitempty"`
	Substrings []string          `yaml:"substrings,omitempty"`
	Re         string            `yaml:"re,omitempty"`
	Structured YAMLStructuredRef `yaml:"structured,omitempty"`
	ParseHint  string            `yaml:"parse_resets_at,omitempty"` // name of a registered ParseResetsAt
}

// YAMLStructuredRef declares which named structured check to bind.
// Resolved against the catalog's structuredRegistry at load time.
type YAMLStructuredRef struct {
	JSONPath string `yaml:"json_path,omitempty"` // simple dotted path for the bundled equality check
	Equals   string `yaml:"equals,omitempty"`
	Check    string `yaml:"check,omitempty"` // named entry in StructuredRegistry (custom checks)
}

// LoadYAMLPatterns parses a YAML catalog and returns TerminalPattern
// values. structuredRegistry resolves named structured checks declared
// via YAMLStructuredRef.Check. parserRegistry resolves named
// ParseResetsAt entries (e.g. "relative_duration_or_clock"). Returns an
// error if any regex fails to compile or any named lookup fails —
// adapter init must surface this rather than silently skipping bad
// patterns.
func LoadYAMLPatterns(
	data []byte,
	structuredRegistry map[string]StructuredCheck,
	parserRegistry map[string]func(match []string) (string, *time.Time),
) ([]TerminalPattern, error) {
	var catalog YAMLCatalog
	// Strict decode: unknown / mistyped keys in the catalog fail fast
	// rather than silently dropping a pattern entry. A typo like
	// "subtsrings:" would otherwise compile to a pattern with no
	// pre-filter and never be noticed.
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&catalog); err != nil {
		return nil, fmt.Errorf("parse YAML catalog: %w", err)
	}
	if catalog.Version != 1 {
		return nil, fmt.Errorf("unsupported catalog version %d (expected 1)", catalog.Version)
	}
	patterns := make([]TerminalPattern, 0, len(catalog.Patterns))
	for i, e := range catalog.Patterns {
		if e.ID == "" {
			return nil, fmt.Errorf("pattern at index %d: missing id", i)
		}
		p := TerminalPattern{
			ID:         e.ID,
			Reason:     e.Reason,
			Source:     e.Source,
			Roles:      e.Roles,
			Substrings: e.Substrings,
		}
		switch e.Source {
		case SourceStructured:
			switch {
			case e.Structured.Check != "":
				fn, ok := structuredRegistry[e.Structured.Check]
				if !ok {
					return nil, fmt.Errorf("pattern %q: unknown structured check %q", e.ID, e.Structured.Check)
				}
				p.Structured = fn
			case e.Structured.JSONPath != "":
				p.Structured = jsonPathEqualsCheck(e.Structured.JSONPath, e.Structured.Equals)
			default:
				return nil, fmt.Errorf("pattern %q: structured source needs json_path or check", e.ID)
			}
		case SourceRegex:
			if e.Re == "" {
				return nil, fmt.Errorf("pattern %q: regex source needs re", e.ID)
			}
			re, err := regexp.Compile(e.Re)
			if err != nil {
				return nil, fmt.Errorf("pattern %q: compile regex: %w", e.ID, err)
			}
			p.Re = re
			if e.ParseHint != "" {
				fn, ok := parserRegistry[e.ParseHint]
				if !ok {
					return nil, fmt.Errorf("pattern %q: unknown parser %q", e.ID, e.ParseHint)
				}
				p.ParseResetsAt = fn
			}
		default:
			return nil, fmt.Errorf("pattern %q: unknown source %q", e.ID, e.Source)
		}
		patterns = append(patterns, p)
	}
	return patterns, nil
}

// jsonPathEqualsCheck builds a structured check that walks a simple
// dotted JSON path (e.g. "message.stop_reason") and returns a match
// when the value equals the given string. Designed for the common
// case so YAML authors do not need to register a Go function for
// every vendor signal.
func jsonPathEqualsCheck(path, want string) StructuredCheck {
	parts := strings.Split(path, ".")
	return func(_ adapterprotocol.RawEntry, raw json.RawMessage) *Match {
		if len(raw) == 0 {
			return nil
		}
		var node any
		if err := json.Unmarshal(raw, &node); err != nil {
			return nil
		}
		for _, part := range parts {
			obj, ok := node.(map[string]any)
			if !ok {
				return nil
			}
			node, ok = obj[part]
			if !ok {
				return nil
			}
		}
		got, ok := node.(string)
		if !ok || got != want {
			return nil
		}
		return &Match{RawMessage: path + "=" + got}
	}
}

// ToTerminalErrorData converts a ConfirmedMatch to the wire payload.
func (c ConfirmedMatch) ToTerminalErrorData() adapterprotocol.TerminalErrorData {
	return adapterprotocol.TerminalErrorData{
		Reason:      c.Match.Reason,
		Source:      string(c.Pattern.Source),
		PatternID:   c.Pattern.ID,
		RawMessage:  c.Match.RawMessage,
		ResetsAtRaw: c.Match.ResetsAtRaw,
		ResetsAt:    c.Match.ResetsAt,
		EntrySeq:    c.EntrySeq,
		DetectedAt:  c.DetectedAt,
		ConfirmedAt: c.ConfirmedAt,
	}
}

// AdapterDisabledByEnv reports whether OX_DISABLE_TERMINAL_DETECTION
// names the given adapter. Adapter binaries call this BEFORE
// constructing a TerminalDetector — when true, skip detector
// instantiation. Comma-separated, case-insensitive, whitespace-tolerant.
// Empty env var (the common case) returns false for any adapter.
func AdapterDisabledByEnv(adapterName string) bool {
	raw := os.Getenv("OX_DISABLE_TERMINAL_DETECTION")
	if raw == "" {
		return false
	}
	target := strings.ToLower(strings.TrimSpace(adapterName))
	for _, part := range strings.Split(raw, ",") {
		if strings.ToLower(strings.TrimSpace(part)) == target {
			return true
		}
	}
	return false
}

// LogConfirmedMatch is a debug helper for adapter authors who want to
// see what fired without standing up a metrics implementation.
func LogConfirmedMatch(logger *slog.Logger, adapter string, c ConfirmedMatch) {
	if logger == nil {
		return
	}
	logger.Info("terminal_error confirmed",
		"adapter", adapter,
		"session_id", c.SessionID,
		"pattern_id", c.Pattern.ID,
		"source", c.Pattern.Source,
		"reason", c.Match.Reason,
		"entry_seq", c.EntrySeq,
		"silence_ms", c.ConfirmedAt.Sub(c.DetectedAt).Milliseconds(),
	)
}
