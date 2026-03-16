package cli

import (
	"bufio"
	"fmt"
	"io"
	"strings"
	"time"
)

// SlogLine holds parsed fields from a slog TextHandler line.
type SlogLine struct {
	Time    string // raw timestamp value
	Level   string // DEBUG, INFO, WARN, ERROR
	Message string // msg value (unquoted)
	Attrs   string // remaining key=value pairs
}

// ParseSlogLine parses a slog TextHandler line into its components.
// Returns false if the line doesn't match the expected format.
func ParseSlogLine(line string) (SlogLine, bool) {
	// slog TextHandler format: time=<ts> level=<level> msg=<msg> [key=value ...]
	rest, ok := strings.CutPrefix(line, "time=")
	if !ok {
		return SlogLine{}, false
	}

	// extract timestamp (everything before " level=")
	idx := strings.Index(rest, " level=")
	if idx < 0 {
		return SlogLine{}, false
	}
	ts := rest[:idx]
	rest = rest[idx+len(" level="):]

	// extract level (everything before " msg=")
	idx = strings.Index(rest, " msg=")
	if idx < 0 {
		return SlogLine{}, false
	}
	level := rest[:idx]
	rest = rest[idx+len(" msg="):]

	// extract message — may be quoted
	var msg, attrs string
	if strings.HasPrefix(rest, "\"") {
		// find closing quote (handle escaped quotes)
		i := 1
		for i < len(rest) {
			if rest[i] == '\\' {
				i += 2 // skip escaped char
				continue
			}
			if rest[i] == '"' {
				msg = rest[1:i]
				if i+1 < len(rest) {
					attrs = strings.TrimLeft(rest[i+1:], " ")
				}
				break
			}
			i++
		}
		if i >= len(rest) {
			// unterminated quote — use everything as msg
			msg = rest[1:]
		}
	} else {
		// unquoted msg — take until next space
		if idx := strings.IndexByte(rest, ' '); idx >= 0 {
			msg = rest[:idx]
			attrs = strings.TrimLeft(rest[idx+1:], " ")
		} else {
			msg = rest
		}
	}

	return SlogLine{
		Time:    ts,
		Level:   level,
		Message: msg,
		Attrs:   attrs,
	}, true
}

// FormatSlogLine parses a raw slog log line and returns a colorized version.
// Malformed lines are returned as-is.
func FormatSlogLine(line string) string {
	parsed, ok := ParseSlogLine(line)
	if !ok {
		return line
	}

	// compact timestamp to time-only with milliseconds
	ts := compactTimestamp(parsed.Time)

	// color the level
	levelStr := fmt.Sprintf("%-5s", parsed.Level)
	var coloredLevel string
	switch parsed.Level {
	case "DEBUG":
		coloredLevel = StyleDim.Render(levelStr)
	case "INFO":
		coloredLevel = StyleInfo.Render(levelStr)
	case "WARN":
		coloredLevel = StyleWarning.Render(levelStr)
	case "ERROR":
		coloredLevel = StyleError.Render(levelStr)
	default:
		coloredLevel = levelStr
	}

	// build output: timestamp  level  message  [attrs]
	var b strings.Builder
	b.WriteString(StyleDim.Render(ts))
	b.WriteString("  ")
	b.WriteString(coloredLevel)
	b.WriteString("  ")
	b.WriteString(parsed.Message)
	if parsed.Attrs != "" {
		b.WriteString("  ")
		b.WriteString(StyleDim.Render(parsed.Attrs))
	}

	return b.String()
}

// compactTimestamp converts an ISO8601 timestamp to time-only with milliseconds.
// Falls back to the raw value if parsing fails.
func compactTimestamp(raw string) string {
	// slog uses time.RFC3339Nano-like format
	for _, layout := range []string{
		"2006-01-02T15:04:05.999999999-07:00",
		"2006-01-02T15:04:05.999999999Z07:00",
		"2006-01-02T15:04:05.999999999Z",
		time.RFC3339Nano,
		time.RFC3339,
	} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t.Format("15:04:05.000")
		}
	}
	return raw
}

// StreamFormattedLogs reads log lines from r, colorizes each, and writes to w.
// Blocks until r is closed or an error occurs.
func StreamFormattedLogs(r io.Reader, w io.Writer) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		fmt.Fprintln(w, FormatSlogLine(scanner.Text()))
	}
}
