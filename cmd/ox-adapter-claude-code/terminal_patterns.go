// terminal_patterns.go wires Claude Code's terminal-error patterns into
// the shared adapterruntime detector. The catalog lives in
// terminal_patterns.yaml (embedded) so pattern updates are reviewable as
// data, not code. parseClaudeReset parses the "resets in ..." capture
// group from the usage-limit regex into an absolute time when the format
// is unambiguous; AM/PM and timezone-tagged clock strings are deliberately
// left unparsed (returned raw only) because they are easy to get wrong
// across day/DST boundaries — better an empty resets_at than a wrong one.
package main

import (
	_ "embed"
	"log"
	"regexp"
	"strings"
	"time"

	"github.com/sageox/ox/pkg/adapterruntime"
)

//go:embed terminal_patterns.yaml
var terminalPatternsYAML []byte

// reTimezoneToken matches uppercase 2-4 letter words used as timezone
// abbreviations ("PT", "UTC", "EST"). Presence is a signal we cannot
// safely interpret the clock string without timezone knowledge.
var reTimezoneToken = regexp.MustCompile(`\b[A-Z]{2,4}\b`)

// reCollapseAlphaDigit removes a single space at a digit/letter boundary
// so "2h 15m" normalizes to "2h15m" — the form time.ParseDuration
// accepts. Matches both digit-then-letter and letter-then-digit
// boundaries because compound durations cross both ways.
var reCollapseAlphaDigit = regexp.MustCompile(`([0-9a-zA-Z])\s+([0-9a-zA-Z])`)

// loadTerminalPatterns parses the embedded YAML catalog into runtime
// TerminalPattern values. The structured registry is nil because all
// structured patterns use the declarative json_path/equals form.
func loadTerminalPatterns() ([]adapterruntime.TerminalPattern, error) {
	parsers := map[string]func([]string) (string, *time.Time){
		"relative_duration_or_clock": parseClaudeReset,
	}
	return adapterruntime.LoadYAMLPatterns(terminalPatternsYAML, nil, parsers)
}

// parseClaudeReset extracts the reset-time substring captured by the
// usage_limit regex and tries to convert it to an absolute time.
//
// match comes from regexp.FindStringSubmatch: match[0] is the full
// match, match[1] is the first named capture group ("reset"). We only
// return a non-nil parsed time when the format is unambiguous.
//
// Accepted: relative durations ("3h", "2h15m", "in 3h") and 24h clock
// strings ("15:00"). 24h clock is interpreted in the local timezone
// using today's date; if the time is already past, we roll forward 24h.
//
// Rejected (raw only, parsed nil): AM/PM strings, anything containing
// a timezone abbreviation, and any format ParseDuration / Parse cannot
// recognize.
func parseClaudeReset(match []string) (string, *time.Time) {
	if len(match) < 2 {
		return "", nil
	}
	raw := strings.TrimSpace(match[1])
	if raw == "" {
		return "", nil
	}

	lower := strings.ToLower(raw)
	if strings.Contains(lower, "am") || strings.Contains(lower, "pm") {
		return raw, nil
	}
	if reTimezoneToken.MatchString(raw) {
		return raw, nil
	}

	normalized := raw
	if strings.HasPrefix(lower, "in ") {
		normalized = strings.TrimSpace(normalized[3:])
	}
	normalized = reCollapseAlphaDigit.ReplaceAllString(normalized, "$1$2")

	if dur, err := time.ParseDuration(normalized); err == nil {
		t := time.Now().Add(dur)
		return raw, &t
	}

	if clock, err := time.Parse("15:04", raw); err == nil {
		now := time.Now()
		t := time.Date(now.Year(), now.Month(), now.Day(), clock.Hour(), clock.Minute(), 0, 0, now.Location())
		if !t.After(now) {
			t = t.Add(24 * time.Hour)
		}
		return raw, &t
	}

	return raw, nil
}

// registerTerminalDetector loads the embedded pattern catalog and
// constructs a detector. Returns nil when the detector is disabled by
// env var or pattern loading fails — callers wire the nil into
// NewFileWatcherWithDetector, which treats it as "no detection".
func registerTerminalDetector() *adapterruntime.TerminalDetector {
	if adapterruntime.AdapterDisabledByEnv(adapterName) {
		return nil
	}
	patterns, err := loadTerminalPatterns()
	if err != nil {
		log.Printf("terminal detector: failed to load patterns: %v", err)
		return nil
	}
	return adapterruntime.NewTerminalDetector(adapterName, patterns, adapterruntime.DefaultSilenceWindow, adapterruntime.NopMetrics{})
}
