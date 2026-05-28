package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/sageox/ox/pkg/adapterruntime"
)

func TestParseClaudeReset(t *testing.T) {
	tests := []struct {
		name       string
		match      []string
		wantRaw    string
		wantParsed bool
		// approxDur, when > 0, asserts parsed is approximately now+approxDur (±5s).
		approxDur time.Duration
	}{
		{
			name:    "empty capture",
			match:   []string{"", ""},
			wantRaw: "",
		},
		{
			name:       "relative with in prefix",
			match:      []string{"full", "in 3h"},
			wantRaw:    "in 3h",
			wantParsed: true,
			approxDur:  3 * time.Hour,
		},
		{
			name:       "bare duration",
			match:      []string{"full", "3h"},
			wantRaw:    "3h",
			wantParsed: true,
			approxDur:  3 * time.Hour,
		},
		{
			name:       "compound duration with space",
			match:      []string{"full", "in 2h 15m"},
			wantRaw:    "in 2h 15m",
			wantParsed: true,
			approxDur:  2*time.Hour + 15*time.Minute,
		},
		{
			name:       "24h clock",
			match:      []string{"full", "15:00"},
			wantRaw:    "15:00",
			wantParsed: true,
		},
		{
			name:    "pm rejected",
			match:   []string{"full", "3pm"},
			wantRaw: "3pm",
		},
		{
			name:    "clock with am/pm and tz rejected",
			match:   []string{"full", "3:00pm UTC"},
			wantRaw: "3:00pm UTC",
		},
		{
			name:       "trims surrounding whitespace",
			match:      []string{"full", "  in 3h  "},
			wantRaw:    "in 3h",
			wantParsed: true,
			approxDur:  3 * time.Hour,
		},
		{
			name:    "unknown words",
			match:   []string{"full", "tomorrow"},
			wantRaw: "tomorrow",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			before := time.Now()
			raw, parsed := parseClaudeReset(tc.match)
			if raw != tc.wantRaw {
				t.Fatalf("raw: got %q want %q", raw, tc.wantRaw)
			}
			if tc.wantParsed {
				if parsed == nil {
					t.Fatalf("expected parsed time, got nil")
				}
				if tc.approxDur > 0 {
					expected := before.Add(tc.approxDur)
					diff := parsed.Sub(expected)
					if diff < -5*time.Second || diff > 5*time.Second {
						t.Fatalf("parsed time off by %v from expected %v", diff, expected)
					}
				}
			} else {
				if parsed != nil {
					t.Fatalf("expected nil parsed, got %v", parsed)
				}
			}
		})
	}
}

func TestLoadTerminalPatterns(t *testing.T) {
	patterns, err := loadTerminalPatterns()
	if err != nil {
		t.Fatalf("loadTerminalPatterns: %v", err)
	}
	if len(patterns) != 3 {
		t.Fatalf("got %d patterns, want 3", len(patterns))
	}
	wantIDs := map[string]bool{
		"claude_code.api_stop_reason.rate_limited": true,
		"claude_code.api_stop_reason.max_tokens":   true,
		"claude_code.system_error.usage_limit":     true,
	}
	for _, p := range patterns {
		if !wantIDs[p.ID] {
			t.Errorf("unexpected pattern id %q", p.ID)
		}
		delete(wantIDs, p.ID)
	}
	for id := range wantIDs {
		t.Errorf("missing pattern id %q", id)
	}
}

func newTestDetector(t *testing.T, window time.Duration) *adapterruntime.TerminalDetector {
	t.Helper()
	patterns, err := loadTerminalPatterns()
	if err != nil {
		t.Fatalf("loadTerminalPatterns: %v", err)
	}
	return adapterruntime.NewTerminalDetector("claude-code", patterns, window, adapterruntime.NopMetrics{})
}

func TestStructuredRateLimitMatch(t *testing.T) {
	d := newTestDetector(t, 10*time.Millisecond)
	entries := []adapterprotocol.RawEntry{
		{Role: adapterprotocol.RoleAssistant, Content: "I need to pause."},
	}
	raws := []json.RawMessage{
		json.RawMessage(`{"message":{"stop_reason":"rate_limited"}}`),
	}
	d.OnBatch("sess1", entries, 1, raws)
	// Deterministic: advance the clock past the silence window rather than
	// sleeping in real time (avoids CI scheduling flakiness).
	confirmed := d.Tick(time.Now().Add(time.Hour))
	if len(confirmed) != 1 {
		t.Fatalf("expected 1 confirmed match, got %d", len(confirmed))
	}
	if confirmed[0].Pattern.ID != "claude_code.api_stop_reason.rate_limited" {
		t.Fatalf("wrong pattern: %s", confirmed[0].Pattern.ID)
	}
}

func TestStructuredMaxTokensInformational(t *testing.T) {
	d := newTestDetector(t, 10*time.Millisecond)
	entries := []adapterprotocol.RawEntry{
		{Role: adapterprotocol.RoleAssistant, Content: "stopped"},
	}
	raws := []json.RawMessage{
		json.RawMessage(`{"message":{"stop_reason":"max_tokens"}}`),
	}
	d.OnBatch("sess1", entries, 1, raws)
	confirmed := d.Tick(time.Now().Add(time.Hour))
	if len(confirmed) != 0 {
		t.Fatalf("expected 0 confirmed (informational), got %d", len(confirmed))
	}
}

func TestRegexSystemRoleOnly(t *testing.T) {
	cases := []struct {
		name      string
		role      string
		wantMatch bool
	}{
		{"system matches", adapterprotocol.RoleSystem, true},
		{"assistant does not match", adapterprotocol.RoleAssistant, false},
		{"user does not match", adapterprotocol.RoleUser, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newTestDetector(t, 10*time.Millisecond)
			entries := []adapterprotocol.RawEntry{
				{Role: tc.role, Content: "5-hour limit reached · resets 3pm"},
			}
			d.OnBatch("sess-"+tc.role, entries, 1, nil)
			confirmed := d.Tick(time.Now().Add(time.Hour))
			if tc.wantMatch && len(confirmed) == 0 {
				t.Fatalf("expected match, got none")
			}
			if !tc.wantMatch && len(confirmed) != 0 {
				t.Fatalf("expected no match, got %d", len(confirmed))
			}
		})
	}
}

func TestRegexSubstringPreFilter(t *testing.T) {
	d := newTestDetector(t, 10*time.Millisecond)
	entries := []adapterprotocol.RawEntry{
		{Role: adapterprotocol.RoleSystem, Content: "hello world"},
	}
	d.OnBatch("sess1", entries, 1, nil)
	confirmed := d.Tick(time.Now().Add(time.Hour))
	if len(confirmed) != 0 {
		t.Fatalf("expected no match, got %d", len(confirmed))
	}
}

func TestSilenceWindowRevoke(t *testing.T) {
	d := newTestDetector(t, 1*time.Hour)
	hit := []adapterprotocol.RawEntry{
		{Role: adapterprotocol.RoleSystem, Content: "5-hour limit reached ∙ resets in 3h"},
	}
	d.OnBatch("sess1", hit, 1, nil)
	revoker := []adapterprotocol.RawEntry{
		{Role: adapterprotocol.RoleSystem, Content: "continuing normally"},
	}
	d.OnBatch("sess1", revoker, 2, nil)
	confirmed := d.Tick(time.Now().Add(2 * time.Hour))
	if len(confirmed) != 0 {
		t.Fatalf("expected revoked (0), got %d", len(confirmed))
	}
}

// fixtureLine is the minimal shape we read from testdata JSONL fixtures.
// Mirrors claudeCodeEntry but kept local to avoid coupling tests to the
// production type's evolution.
type fixtureLine struct {
	Type    string `json:"type"`
	IsMeta  bool   `json:"isMeta"`
	Message *struct {
		Role    string      `json:"role"`
		Content interface{} `json:"content"`
	} `json:"message"`
	Timestamp string                 `json:"timestamp"`
	Meta      map[string]interface{} `json:"_meta"`
}

// loadFixture reads a JSONL fixture, skips the leading _meta line, and
// returns parallel slices of parsed RawEntries and the raw line bytes
// each entry came from. Lines that yield multiple entries replicate the
// raw bytes for each — the structured check looks at the same JSON for
// every entry from that line.
func loadFixture(t *testing.T, path string) ([]adapterprotocol.RawEntry, []json.RawMessage) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var entries []adapterprotocol.RawEntry
	var raws []json.RawMessage
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var probe fixtureLine
		if err := json.Unmarshal([]byte(line), &probe); err != nil {
			continue
		}
		if probe.Meta != nil {
			continue
		}
		parsed, err := parseLine([]byte(line))
		if err != nil || len(parsed) == 0 {
			continue
		}
		for _, e := range parsed {
			entries = append(entries, e)
			raws = append(raws, json.RawMessage(line))
		}
	}
	return entries, raws
}

func TestGoldenCorpus(t *testing.T) {
	dir := filepath.Join("testdata", "terminal")
	files, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read testdata: %v", err)
	}
	for _, f := range files {
		if f.IsDir() || !strings.HasSuffix(f.Name(), ".jsonl") {
			continue
		}
		name := f.Name()
		t.Run(name, func(t *testing.T) {
			entries, raws := loadFixture(t, filepath.Join(dir, name))
			if len(entries) == 0 {
				t.Fatalf("fixture produced no entries")
			}
			d := newTestDetector(t, 10*time.Millisecond)
			// Feed the batch. For revoke-style fixtures the parallel
			// raws are still aligned per entry, so the detector sees the
			// same order a live watcher would.
			d.OnBatch("fixture-session", entries, 1, raws)
			confirmed := d.Tick(time.Now().Add(1 * time.Hour))
			isPositive := strings.HasSuffix(name, ".positive.jsonl")
			if isPositive && len(confirmed) == 0 {
				t.Fatalf("positive fixture %s: expected match, got none", name)
			}
			if !isPositive && len(confirmed) != 0 {
				t.Fatalf("negative fixture %s: expected no match, got %d", name, len(confirmed))
			}
		})
	}
}
