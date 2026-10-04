package model

import "time"

// ByteRange is a half-open range in the original, uncompressed receiver spool.
type ByteRange [2]int64

// NativeRanges records exactly which native session bytes were selected.
type NativeRanges struct {
	ID          string      `json:"id"`
	Source      string      `json:"source,omitempty"`
	SpansBytes  []ByteRange `json:"spans_bytes"`
	EventsBytes []ByteRange `json:"events_bytes"`
}

// Metadata describes the scrubbed sidecars. A nil observation is unknown, not
// false or an inferred version of the process that happened to finalize later.
type Metadata struct {
	NativeSessions              []NativeRanges   `json:"native_sessions"`
	StoppedAt                   time.Time        `json:"stopped_at"`
	ClaudeCodeVersion           *string          `json:"claude_code_version"`
	Entrypoint                  *string          `json:"entrypoint"`
	TerminalType                *string          `json:"terminal_type"`
	SpansLines                  int64            `json:"spans_lines"`
	EventsLines                 int64            `json:"events_lines"`
	Spans                       int64            `json:"spans"`
	Events                      int64            `json:"events"`
	ReceiverUpBeforeFirstPrompt *bool            `json:"receiver_up_before_first_prompt"`
	PausedBytesSkipped          int64            `json:"paused_bytes_skipped"`
	TrailingBytesSkipped        int64            `json:"trailing_bytes_skipped"`
	LateBytes                   int64            `json:"late_bytes"`
	Scrubbed                    map[string]int64 `json:"scrubbed"`
	ReceiverVersion             *string          `json:"receiver_version"`
	SettingsTag                 *string          `json:"settings_tag"`
	Errors                      []string         `json:"errors,omitempty"`
}
