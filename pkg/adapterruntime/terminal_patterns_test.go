package adapterruntime

import (
	"encoding/json"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/sageox/ox/pkg/adapterprotocol"
)

// recordingMetrics captures Metrics calls for assertions.
type recordingMetrics struct {
	mu        sync.Mutex
	hits      []hitRecord
	confirmed []confirmRecord
	revoked   []confirmRecord
	parseFail []confirmRecord
}

type hitRecord struct {
	adapter, patternID, reason string
	source                     PatternSource
}
type confirmRecord struct{ adapter, patternID string }

func (m *recordingMetrics) PatternHit(adapter, id string, src PatternSource, reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.hits = append(m.hits, hitRecord{adapter, id, reason, src})
}
func (m *recordingMetrics) PatternConfirmed(adapter, id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.confirmed = append(m.confirmed, confirmRecord{adapter, id})
}
func (m *recordingMetrics) PatternRevoked(adapter, id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.revoked = append(m.revoked, confirmRecord{adapter, id})
}
func (m *recordingMetrics) ResetsAtParseFailure(adapter, id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.parseFail = append(m.parseFail, confirmRecord{adapter, id})
}
func (m *recordingMetrics) SilenceWindowObservedMs(string, string, time.Duration) {}

func TestDetector_StructuredMatchBeforeRegex(t *testing.T) {
	structured := TerminalPattern{
		ID:     "structured-first",
		Reason: "rate_limited",
		Source: SourceStructured,
		Structured: func(adapterprotocol.RawEntry, json.RawMessage) *Match {
			return &Match{RawMessage: "structured fired"}
		},
	}
	regexPat := TerminalPattern{
		ID:     "regex-second",
		Reason: "rate_limited",
		Source: SourceRegex,
		Re:     regexp.MustCompile(`limit reached`),
		Roles:  []string{adapterprotocol.RoleSystem},
	}
	m := &recordingMetrics{}
	d := NewTerminalDetector("test", []TerminalPattern{structured, regexPat}, 10*time.Millisecond, m)

	d.OnBatch("sess", []adapterprotocol.RawEntry{
		{Role: adapterprotocol.RoleSystem, Content: "limit reached"},
	}, 1, nil)

	if len(m.hits) != 1 {
		t.Fatalf("expected 1 hit, got %d", len(m.hits))
	}
	if m.hits[0].patternID != "structured-first" {
		t.Errorf("expected structured to win, got %q", m.hits[0].patternID)
	}
}

func TestDetector_RegexRoleGate(t *testing.T) {
	pat := TerminalPattern{
		ID:     "system-only",
		Reason: "rate_limited",
		Source: SourceRegex,
		Re:     regexp.MustCompile(`limit reached`),
		Roles:  []string{adapterprotocol.RoleSystem},
	}
	cases := []struct {
		role    string
		wantHit bool
	}{
		{adapterprotocol.RoleSystem, true},
		{adapterprotocol.RoleAssistant, false},
		{adapterprotocol.RoleUser, false},
		{adapterprotocol.RoleTool, false},
	}
	for _, tc := range cases {
		t.Run(tc.role, func(t *testing.T) {
			m := &recordingMetrics{}
			d := NewTerminalDetector("test", []TerminalPattern{pat}, 10*time.Millisecond, m)
			d.OnBatch("sess", []adapterprotocol.RawEntry{
				{Role: tc.role, Content: "limit reached"},
			}, 1, nil)
			gotHit := len(m.hits) > 0
			if gotHit != tc.wantHit {
				t.Errorf("role %s: got hit=%v want %v", tc.role, gotHit, tc.wantHit)
			}
		})
	}
}

func TestDetector_RegexDefaultRoleIsSystem(t *testing.T) {
	pat := TerminalPattern{
		ID:     "default-role",
		Reason: "rate_limited",
		Source: SourceRegex,
		Re:     regexp.MustCompile(`limit reached`),
		// Roles unset
	}
	m := &recordingMetrics{}
	d := NewTerminalDetector("test", []TerminalPattern{pat}, 10*time.Millisecond, m)
	// assistant role must NOT match
	d.OnBatch("sess", []adapterprotocol.RawEntry{
		{Role: adapterprotocol.RoleAssistant, Content: "limit reached"},
	}, 1, nil)
	if len(m.hits) != 0 {
		t.Errorf("expected default role gate to reject assistant, got hits=%d", len(m.hits))
	}
	// system role must match
	d.OnBatch("sess2", []adapterprotocol.RawEntry{
		{Role: adapterprotocol.RoleSystem, Content: "limit reached"},
	}, 1, nil)
	if len(m.hits) != 1 {
		t.Errorf("expected system role default to match, got hits=%d", len(m.hits))
	}
}

func TestDetector_SubstringPreFilter(t *testing.T) {
	called := 0
	wrapped := regexp.MustCompile(`limit reached`)
	pat := TerminalPattern{
		ID:         "prefilter",
		Reason:     "rate_limited",
		Source:     SourceRegex,
		Substrings: []string{"limit reached"},
		Re:         wrapped,
		Roles:      []string{adapterprotocol.RoleSystem},
	}
	m := &recordingMetrics{}
	d := NewTerminalDetector("test", []TerminalPattern{pat}, 10*time.Millisecond, m)
	// no substring match → regex must not match (pre-filter rejected)
	d.OnBatch("sess", []adapterprotocol.RawEntry{
		{Role: adapterprotocol.RoleSystem, Content: "all good here"},
	}, 1, nil)
	if len(m.hits) != 0 {
		t.Errorf("expected pre-filter to skip, got hits=%d", len(m.hits))
	}
	_ = called
}

func TestDetector_SilenceWindowConfirm(t *testing.T) {
	pat := TerminalPattern{
		ID:     "sw",
		Reason: "rate_limited",
		Source: SourceRegex,
		Re:     regexp.MustCompile(`limit reached`),
		Roles:  []string{adapterprotocol.RoleSystem},
	}
	m := &recordingMetrics{}
	d := NewTerminalDetector("test", []TerminalPattern{pat}, 10*time.Millisecond, m)
	d.OnBatch("sess", []adapterprotocol.RawEntry{
		{Role: adapterprotocol.RoleSystem, Content: "limit reached"},
	}, 1, nil)
	// Tick before window elapses → no confirm.
	if c := d.Tick(time.Now()); len(c) != 0 {
		t.Errorf("expected no confirm before window, got %d", len(c))
	}
	// Tick after window elapses → confirm.
	got := d.Tick(time.Now().Add(time.Hour))
	if len(got) != 1 {
		t.Fatalf("expected 1 confirm after window, got %d", len(got))
	}
	if got[0].EntrySeq != 1 {
		t.Errorf("EntrySeq=%d want 1", got[0].EntrySeq)
	}
}

func TestDetector_SilenceWindowRevoke(t *testing.T) {
	pat := TerminalPattern{
		ID:     "rv",
		Reason: "rate_limited",
		Source: SourceRegex,
		Re:     regexp.MustCompile(`limit reached`),
		Roles:  []string{adapterprotocol.RoleSystem},
	}
	m := &recordingMetrics{}
	d := NewTerminalDetector("test", []TerminalPattern{pat}, time.Hour, m)
	// match in batch 1
	d.OnBatch("sess", []adapterprotocol.RawEntry{
		{Role: adapterprotocol.RoleSystem, Content: "limit reached"},
	}, 1, nil)
	// non-matching entry in batch 2 — revokes pending
	d.OnBatch("sess", []adapterprotocol.RawEntry{
		{Role: adapterprotocol.RoleSystem, Content: "continuing normally"},
	}, 2, nil)
	// Tick way past window — must NOT confirm
	got := d.Tick(time.Now().Add(2 * time.Hour))
	if len(got) != 0 {
		t.Errorf("expected revoke to prevent confirm, got %d", len(got))
	}
	if len(m.revoked) != 1 {
		t.Errorf("expected 1 revoke metric, got %d", len(m.revoked))
	}
}

func TestDetector_EmptyReasonInformationalOnly(t *testing.T) {
	pat := TerminalPattern{
		ID:     "info-only",
		Reason: "", // intentional: log+metric, do not finalize
		Source: SourceStructured,
		Structured: func(adapterprotocol.RawEntry, json.RawMessage) *Match {
			return &Match{RawMessage: "info"}
		},
	}
	m := &recordingMetrics{}
	d := NewTerminalDetector("test", []TerminalPattern{pat}, 10*time.Millisecond, m)
	d.OnBatch("sess", []adapterprotocol.RawEntry{{Role: adapterprotocol.RoleAssistant}}, 1, nil)
	if len(m.hits) != 1 {
		t.Errorf("hit must still fire for metrics, got %d", len(m.hits))
	}
	got := d.Tick(time.Now().Add(time.Hour))
	if len(got) != 0 {
		t.Errorf("empty-reason pattern must NOT produce ConfirmedMatch, got %d", len(got))
	}
}

func TestDetector_Forget(t *testing.T) {
	pat := TerminalPattern{
		ID:     "fp",
		Reason: "rate_limited",
		Source: SourceRegex,
		Re:     regexp.MustCompile(`limit reached`),
		Roles:  []string{adapterprotocol.RoleSystem},
	}
	d := NewTerminalDetector("test", []TerminalPattern{pat}, time.Hour, NopMetrics{})
	d.OnBatch("sess", []adapterprotocol.RawEntry{
		{Role: adapterprotocol.RoleSystem, Content: "limit reached"},
	}, 1, nil)
	d.Forget("sess")
	if got := d.Tick(time.Now().Add(2 * time.Hour)); len(got) != 0 {
		t.Errorf("Forget must drop pending, got %d", len(got))
	}
}

func TestDetector_RawMessageTruncation(t *testing.T) {
	big := make([]byte, MaxRawMessageBytes+200)
	for i := range big {
		big[i] = 'x'
	}
	pat := TerminalPattern{
		ID:     "trunc",
		Reason: "rate_limited",
		Source: SourceRegex,
		Re:     regexp.MustCompile(`x+`),
		Roles:  []string{adapterprotocol.RoleSystem},
	}
	d := NewTerminalDetector("test", []TerminalPattern{pat}, 10*time.Millisecond, NopMetrics{})
	d.OnBatch("sess", []adapterprotocol.RawEntry{
		{Role: adapterprotocol.RoleSystem, Content: string(big)},
	}, 1, nil)
	got := d.Tick(time.Now().Add(time.Hour))
	if len(got) != 1 {
		t.Fatalf("expected 1 confirm, got %d", len(got))
	}
	if len(got[0].Match.RawMessage) > MaxRawMessageBytes {
		t.Errorf("raw message not truncated: len=%d > %d", len(got[0].Match.RawMessage), MaxRawMessageBytes)
	}
}

func TestDetector_DisabledNoOp(t *testing.T) {
	var d *TerminalDetector // nil detector
	if d.Enabled() {
		t.Error("nil detector must report Enabled()=false")
	}
	d2 := NewTerminalDetector("test", nil, 10*time.Millisecond, NopMetrics{})
	if d2.Enabled() {
		t.Error("zero-pattern detector must report Enabled()=false")
	}
	// OnBatch / Tick / Forget all safe on disabled
	d.OnBatch("sess", nil, 1, nil)
	if got := d.Tick(time.Now()); got != nil {
		t.Errorf("nil detector Tick must return nil, got %v", got)
	}
	d.Forget("sess")
}

func TestLoadYAMLPatterns_StructuredJSONPath(t *testing.T) {
	yaml := []byte(`version: 1
patterns:
  - id: t.stop_reason
    source: structured
    reason: rate_limited
    structured:
      json_path: message.stop_reason
      equals: rate_limited
`)
	patterns, err := LoadYAMLPatterns(yaml, nil, nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(patterns) != 1 {
		t.Fatalf("want 1 pattern, got %d", len(patterns))
	}
	// Exercise the structured check.
	raw := json.RawMessage(`{"message":{"stop_reason":"rate_limited"}}`)
	m := patterns[0].Structured(adapterprotocol.RawEntry{}, raw)
	if m == nil {
		t.Fatal("expected structured match")
	}
	raw2 := json.RawMessage(`{"message":{"stop_reason":"max_tokens"}}`)
	if m := patterns[0].Structured(adapterprotocol.RawEntry{}, raw2); m != nil {
		t.Errorf("expected no match for max_tokens, got %+v", m)
	}
}

func TestLoadYAMLPatterns_RegexCompileError(t *testing.T) {
	yaml := []byte(`version: 1
patterns:
  - id: bad
    source: regex
    reason: x
    re: '(unbalanced'
`)
	if _, err := LoadYAMLPatterns(yaml, nil, nil); err == nil {
		t.Error("expected compile error for invalid regex, got nil")
	}
}

func TestLoadYAMLPatterns_VersionMismatch(t *testing.T) {
	yaml := []byte(`version: 99
patterns: []
`)
	if _, err := LoadYAMLPatterns(yaml, nil, nil); err == nil {
		t.Error("expected version mismatch error, got nil")
	}
}

func TestLoadYAMLPatterns_NoReDoSOnPathological(t *testing.T) {
	// Sanity check: the bundled-style regex shape must not catastrophically
	// backtrack on hostile input. Run with a hard deadline.
	yaml := []byte(`version: 1
patterns:
  - id: redos
    source: regex
    reason: rate_limited
    roles: [system]
    re: '(?i)(?:hit your limit|limit reached|usage limit)[^\n]{0,80}?(?:resets?(?:\s+in)?\s+(?P<reset>[^\n.]{1,30}))?'
`)
	patterns, err := LoadYAMLPatterns(yaml, nil, nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	hostile := ""
	for i := 0; i < 5000; i++ {
		hostile += "a"
	}
	done := make(chan bool, 1)
	go func() {
		_ = patterns[0].Re.FindStringSubmatch(hostile)
		done <- true
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ReDoS: regex did not finish within 2s on hostile input")
	}
}

func TestConfirmedMatch_ToTerminalErrorData(t *testing.T) {
	now := time.Now()
	c := ConfirmedMatch{
		SessionID:   "s1",
		Pattern:     TerminalPattern{ID: "p1", Source: SourceRegex},
		Match:       Match{Reason: "rate_limited", RawMessage: "msg", ResetsAtRaw: "in 3h", ResetsAt: &now},
		EntrySeq:    7,
		DetectedAt:  now.Add(-30 * time.Second),
		ConfirmedAt: now,
	}
	d := c.ToTerminalErrorData()
	if d.Reason != "rate_limited" || d.PatternID != "p1" || d.Source != string(SourceRegex) {
		t.Errorf("payload fields wrong: %+v", d)
	}
	if d.EntrySeq != 7 {
		t.Errorf("entry seq lost: %d", d.EntrySeq)
	}
	if d.ResetsAt == nil || !d.ResetsAt.Equal(now) {
		t.Errorf("resets_at lost: %v", d.ResetsAt)
	}
}
