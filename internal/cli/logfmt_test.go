package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestParseSlogLine(t *testing.T) {
	tests := []struct {
		name    string
		line    string
		want    SlogLine
		wantOK  bool
	}{
		{
			name: "info with quoted msg",
			line: `time=2026-03-09T18:27:44.282-07:00 level=INFO msg="daemon starting" version=0.8.0`,
			want: SlogLine{
				Time:    "2026-03-09T18:27:44.282-07:00",
				Level:   "INFO",
				Message: "daemon starting",
				Attrs:   "version=0.8.0",
			},
			wantOK: true,
		},
		{
			name: "warn with long msg and path attr",
			line: `time=2026-03-09T18:27:44.282-07:00 level=WARN msg="manifest: file not found, using fallback" path=/Users/ryan/.local/share/sageox/sync.manifest`,
			want: SlogLine{
				Time:    "2026-03-09T18:27:44.282-07:00",
				Level:   "WARN",
				Message: "manifest: file not found, using fallback",
				Attrs:   "path=/Users/ryan/.local/share/sageox/sync.manifest",
			},
			wantOK: true,
		},
		{
			name: "error with quoted error attr",
			line: `time=2026-03-09T18:27:46.503-07:00 level=ERROR msg="failed to sync" error="connection refused"`,
			want: SlogLine{
				Time:    "2026-03-09T18:27:46.503-07:00",
				Level:   "ERROR",
				Message: "failed to sync",
				Attrs:   `error="connection refused"`,
			},
			wantOK: true,
		},
		{
			name: "debug no attrs",
			line: `time=2026-03-09T18:27:44.282-07:00 level=DEBUG msg="tick"`,
			want: SlogLine{
				Time:    "2026-03-09T18:27:44.282-07:00",
				Level:   "DEBUG",
				Message: "tick",
				Attrs:   "",
			},
			wantOK: true,
		},
		{
			name: "unquoted msg",
			line: `time=2026-03-09T18:27:44.282-07:00 level=INFO msg=starting version=1.0`,
			want: SlogLine{
				Time:    "2026-03-09T18:27:44.282-07:00",
				Level:   "INFO",
				Message: "starting",
				Attrs:   "version=1.0",
			},
			wantOK: true,
		},
		{
			name: "unquoted msg no attrs",
			line: `time=2026-03-09T18:27:44.282-07:00 level=INFO msg=done`,
			want: SlogLine{
				Time:    "2026-03-09T18:27:44.282-07:00",
				Level:   "INFO",
				Message: "done",
				Attrs:   "",
			},
			wantOK: true,
		},
		{
			name:   "empty line",
			line:   "",
			wantOK: false,
		},
		{
			name:   "non-slog line",
			line:   "some random log output",
			wantOK: false,
		},
		{
			name:   "missing level",
			line:   "time=2026-03-09T18:27:44.282-07:00 msg=hello",
			wantOK: false,
		},
		{
			name:   "missing msg",
			line:   "time=2026-03-09T18:27:44.282-07:00 level=INFO",
			wantOK: false,
		},
		{
			name: "msg with escaped quotes",
			line: `time=2026-03-09T18:27:44.282-07:00 level=INFO msg="said \"hello\"" key=val`,
			want: SlogLine{
				Time:    "2026-03-09T18:27:44.282-07:00",
				Level:   "INFO",
				Message: `said \"hello\"`,
				Attrs:   "key=val",
			},
			wantOK: true,
		},
		{
			name: "multiple attrs",
			line: `time=2026-03-09T18:27:44.282-07:00 level=INFO msg="sync complete" repos=3 duration=1.23s`,
			want: SlogLine{
				Time:    "2026-03-09T18:27:44.282-07:00",
				Level:   "INFO",
				Message: "sync complete",
				Attrs:   "repos=3 duration=1.23s",
			},
			wantOK: true,
		},
		{
			name: "utc timestamp",
			line: `time=2026-03-09T01:27:44.282Z level=INFO msg="utc log"`,
			want: SlogLine{
				Time:    "2026-03-09T01:27:44.282Z",
				Level:   "INFO",
				Message: "utc log",
				Attrs:   "",
			},
			wantOK: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ParseSlogLine(tt.line)
			if ok != tt.wantOK {
				t.Fatalf("ParseSlogLine() ok = %v, want %v", ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if got.Time != tt.want.Time {
				t.Errorf("Time = %q, want %q", got.Time, tt.want.Time)
			}
			if got.Level != tt.want.Level {
				t.Errorf("Level = %q, want %q", got.Level, tt.want.Level)
			}
			if got.Message != tt.want.Message {
				t.Errorf("Message = %q, want %q", got.Message, tt.want.Message)
			}
			if got.Attrs != tt.want.Attrs {
				t.Errorf("Attrs = %q, want %q", got.Attrs, tt.want.Attrs)
			}
		})
	}
}

func TestCompactTimestamp(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "iso8601 with offset",
			raw:  "2026-03-09T18:27:44.282-07:00",
			want: "18:27:44.282",
		},
		{
			name: "utc z suffix",
			raw:  "2026-03-09T01:27:44.282Z",
			want: "01:27:44.282",
		},
		{
			name: "no fractional seconds",
			raw:  "2026-03-09T18:27:44-07:00",
			want: "18:27:44.000",
		},
		{
			name: "nanoseconds truncated to ms",
			raw:  "2026-03-09T18:27:44.282456789-07:00",
			want: "18:27:44.282",
		},
		{
			name: "unparseable falls back to raw",
			raw:  "not-a-timestamp",
			want: "not-a-timestamp",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := compactTimestamp(tt.raw)
			if got != tt.want {
				t.Errorf("compactTimestamp(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

func TestFormatSlogLine_Passthrough(t *testing.T) {
	// non-slog lines should pass through unchanged
	lines := []string{
		"",
		"some random text",
		"not a slog line at all",
	}
	for _, line := range lines {
		got := FormatSlogLine(line)
		if got != line {
			t.Errorf("FormatSlogLine(%q) = %q, want passthrough", line, got)
		}
	}
}

func TestFormatSlogLine_ContainsLevel(t *testing.T) {
	// verify that formatted output contains the level text
	line := `time=2026-03-09T18:27:44.282-07:00 level=WARN msg="test warning" key=val`
	got := FormatSlogLine(line)
	if !strings.Contains(got, "WARN") {
		t.Errorf("formatted output should contain WARN, got: %q", got)
	}
	if !strings.Contains(got, "test warning") {
		t.Errorf("formatted output should contain message, got: %q", got)
	}
	if !strings.Contains(got, "18:27:44.282") {
		t.Errorf("formatted output should contain compact timestamp, got: %q", got)
	}
}

func TestStreamFormattedLogs(t *testing.T) {
	input := strings.Join([]string{
		`time=2026-03-09T18:27:44.282-07:00 level=INFO msg="first line"`,
		`time=2026-03-09T18:27:45.019-07:00 level=WARN msg="second line"`,
		`not a slog line`,
		`time=2026-03-09T18:27:46.503-07:00 level=ERROR msg="third line"`,
	}, "\n")

	var buf bytes.Buffer
	StreamFormattedLogs(strings.NewReader(input), &buf)

	output := buf.String()
	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")

	if len(lines) != 4 {
		t.Fatalf("expected 4 output lines, got %d: %v", len(lines), lines)
	}

	// slog lines should be formatted (contain compact timestamp)
	if !strings.Contains(lines[0], "18:27:44.282") {
		t.Errorf("line 0 should contain compact timestamp: %q", lines[0])
	}
	if !strings.Contains(lines[1], "18:27:45.019") {
		t.Errorf("line 1 should contain compact timestamp: %q", lines[1])
	}

	// non-slog line should pass through
	if !strings.Contains(lines[2], "not a slog line") {
		t.Errorf("line 2 should be passthrough: %q", lines[2])
	}
}
