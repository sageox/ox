package tokenstrip

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"
)

// runCompress is a small helper that feeds one-or-more jsonl lines through
// Compress and returns the rewritten lines plus stats.
func runCompress(t *testing.T, lines []string, opts Options) ([]string, Stats) {
	t.Helper()
	var in bytes.Buffer
	for _, l := range lines {
		in.WriteString(l)
		in.WriteByte('\n')
	}
	var out bytes.Buffer
	stats, err := CompressWith(&in, &out, opts)
	if err != nil {
		t.Fatalf("CompressWith: %v", err)
	}
	got := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	return got, stats
}

// mustJSON is a small helper for assembling jsonl fixtures.
func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

// TestUserTurnsUntouched verifies the hard rule: user turns are byte-identical
// on output, even when they contain zero-width chars and stop words.
func TestUserTurnsUntouched(t *testing.T) {
	user := map[string]any{
		"type":    "user",
		"content": "what is the\u200b best way to use a configuration file",
	}
	line := mustJSON(t, user)
	got, stats := runCompress(t, []string{line}, Options{})

	if got[0] != line {
		t.Fatalf("user turn was modified.\n  in:  %s\n  out: %s", line, got[0])
	}
	if stats.ZeroWidthStripped != 0 || stats.StopWordsRemoved != 0 {
		t.Fatalf("user-turn stats should be zero, got %+v", stats)
	}
}

// TestAssistantProseOutsideThinkingUntouched verifies stop words in regular
// assistant prose (no <thinking>) are NOT removed.
func TestAssistantProseOutsideThinkingUntouched(t *testing.T) {
	// The sentence below uses only stop words + short content words; if the
	// removal were applied globally it would be devastated.
	prose := "The answer is that you should run the tool on the file."
	asst := map[string]any{"type": "assistant", "content": prose}
	line := mustJSON(t, asst)

	got, stats := runCompress(t, []string{line}, Options{})

	var e struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal([]byte(got[0]), &e); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if e.Content != prose {
		t.Fatalf("assistant prose outside <thinking> was altered.\n  in:  %q\n  out: %q", prose, e.Content)
	}
	if stats.StopWordsRemoved != 0 {
		t.Fatalf("stop word removal fired outside <thinking>: %+v", stats)
	}
}

// TestThinkingBlockStopWordsRemoved verifies stop words inside <thinking>
// blocks ARE removed (English list).
func TestThinkingBlockStopWordsRemoved(t *testing.T) {
	content := "Answer: 42.\n<thinking>I should think about the problem and the solution before I respond</thinking>\nHere you go."
	asst := map[string]any{"type": "assistant", "content": content}
	line := mustJSON(t, asst)

	got, stats := runCompress(t, []string{line}, Options{})

	if stats.StopWordsRemoved != 1 {
		t.Fatalf("StopWordsRemoved=%d, want 1", stats.StopWordsRemoved)
	}
	var e struct {
		Content string `json:"content"`
	}
	_ = json.Unmarshal([]byte(got[0]), &e)

	// Prose outside <thinking> must survive intact.
	if !strings.Contains(e.Content, "Answer: 42.") {
		t.Fatalf("prose before <thinking> lost: %q", e.Content)
	}
	if !strings.Contains(e.Content, "Here you go.") {
		t.Fatalf("prose after <thinking> lost: %q", e.Content)
	}
	// The inside of <thinking> should have shrunk — at minimum "the", "I",
	// "about", "and", "before" are English stop words.
	// We don't assert the exact post-strip text (bbalet lowercases and
	// drops punctuation) but we do assert it is shorter than the original.
	origLen := len("I should think about the problem and the solution before I respond")
	innerStart := strings.Index(e.Content, "<thinking>") + len("<thinking>")
	innerEnd := strings.Index(e.Content, "</thinking>")
	newInner := e.Content[innerStart:innerEnd]
	if len(newInner) >= origLen {
		t.Fatalf("inside <thinking> did not shrink: %q", newInner)
	}
}

// TestNFCNormalization verifies NFD (decomposed) input becomes NFC.
func TestNFCNormalization(t *testing.T) {
	// "café" with combining acute accent (NFD form).
	nfd := "cafe\u0301"
	nfc := "café"

	content := "Answer " + nfd + "."
	asst := map[string]any{"type": "assistant", "content": content}
	line := mustJSON(t, asst)

	got, stats := runCompress(t, []string{line}, Options{})
	if stats.NFCNormalized != 1 {
		t.Fatalf("NFCNormalized=%d, want 1", stats.NFCNormalized)
	}
	var e struct {
		Content string `json:"content"`
	}
	_ = json.Unmarshal([]byte(got[0]), &e)
	if !strings.Contains(e.Content, nfc) {
		t.Fatalf("NFC form not present: %q", e.Content)
	}
	if strings.Contains(e.Content, nfd) {
		t.Fatalf("NFD form leaked through: %q", e.Content)
	}
}

// TestZeroWidthStripped verifies zero-width codepoints are removed.
func TestZeroWidthStripped(t *testing.T) {
	content := "Hello\u200bworld\u200c.\ufeffThe\u00a0end."
	asst := map[string]any{"type": "assistant", "content": content}
	line := mustJSON(t, asst)

	got, stats := runCompress(t, []string{line}, Options{})
	if stats.ZeroWidthStripped != 1 {
		t.Fatalf("ZeroWidthStripped=%d, want 1", stats.ZeroWidthStripped)
	}
	var e struct {
		Content string `json:"content"`
	}
	_ = json.Unmarshal([]byte(got[0]), &e)
	for _, bad := range []string{"\u200b", "\u200c", "\ufeff", "\u00a0"} {
		if strings.Contains(e.Content, bad) {
			t.Fatalf("zero-width %q not stripped from %q", bad, e.Content)
		}
	}
	// NBSP should have become a regular space.
	if !strings.Contains(e.Content, "The end.") {
		t.Fatalf("NBSP not collapsed to regular space: %q", e.Content)
	}
}

// TestWhitespaceCanonicalized verifies multi-space / multi-newline collapse
// and trailing-space trim.
func TestWhitespaceCanonicalized(t *testing.T) {
	content := "hello    world   \n\n\n\nnext para  \nend"
	asst := map[string]any{"type": "assistant", "content": content}
	line := mustJSON(t, asst)

	got, stats := runCompress(t, []string{line}, Options{})
	if stats.WhitespaceCanonicalized != 1 {
		t.Fatalf("WhitespaceCanonicalized=%d, want 1", stats.WhitespaceCanonicalized)
	}
	var e struct {
		Content string `json:"content"`
	}
	_ = json.Unmarshal([]byte(got[0]), &e)
	if strings.Contains(e.Content, "    ") {
		t.Fatalf("multi-space not collapsed: %q", e.Content)
	}
	if strings.Contains(e.Content, "\n\n\n") {
		t.Fatalf("3+ newlines not capped: %q", e.Content)
	}
	if strings.Contains(e.Content, "   \n") || strings.Contains(e.Content, "  \n") {
		t.Fatalf("trailing spaces not trimmed: %q", e.Content)
	}
}

// TestToolEntriesUntouched verifies tool_name, tool_input, tool_mark.brief
// are not mutated.
func TestToolEntriesUntouched(t *testing.T) {
	// Pathological: tool entry with stop words + zero-width in fields. All
	// must survive verbatim.
	tool := map[string]any{
		"type":        "tool",
		"tool_name":   "Bash",
		"tool_input":  "{\"command\":\"the cat ran\u200b over the mat\"}",
		"tool_output": "the output is\u200b here",
	}
	line := mustJSON(t, tool)
	got, _ := runCompress(t, []string{line}, Options{})
	if got[0] != line {
		t.Fatalf("tool entry modified.\n  in:  %s\n  out: %s", line, got[0])
	}

	mark := map[string]any{
		"type":      "tool_mark",
		"tool_name": "Read",
		"brief":     "the file that I want to read",
	}
	line2 := mustJSON(t, mark)
	got2, _ := runCompress(t, []string{line2}, Options{})
	if got2[0] != line2 {
		t.Fatalf("tool_mark modified.\n  in:  %s\n  out: %s", line2, got2[0])
	}
}

// TestHeaderUntouched verifies header entries pass through verbatim.
func TestHeaderUntouched(t *testing.T) {
	hdr := map[string]any{
		"type":       "header",
		"session_id": "abc\u200bdef",
	}
	line := mustJSON(t, hdr)
	got, _ := runCompress(t, []string{line}, Options{})
	if got[0] != line {
		t.Fatalf("header modified.\n  in:  %s\n  out: %s", line, got[0])
	}
}

// TestOversizedEntry verifies a single >5MB entry does not break the stream.
// bufio.Scanner would fail here; bufio.Reader.ReadBytes must not.
func TestOversizedEntry(t *testing.T) {
	big := strings.Repeat("x", 5*1024*1024)
	asst := map[string]any{"type": "assistant", "content": big}
	line := mustJSON(t, asst)

	got, stats := runCompress(t, []string{line}, Options{})
	if stats.EntriesIn != 1 || stats.EntriesOut != 1 {
		t.Fatalf("entries in/out: %d/%d, want 1/1", stats.EntriesIn, stats.EntriesOut)
	}
	// Oversized entry had no transforms applicable; should round-trip.
	if len(got[0]) < 5*1024*1024 {
		t.Fatalf("oversized entry lost content: got %d bytes", len(got[0]))
	}
}

// TestStatsReductionMath checks both byte- and token-reduction arithmetic.
func TestStatsReductionMath(t *testing.T) {
	s := Stats{BytesIn: 1000, BytesOut: 750, TokensInEstimate: 250, TokensOutEstimate: 200}
	saved, pct := s.Reduction()
	if saved != 250 || pct != 25.0 {
		t.Fatalf("Reduction(): saved=%d pct=%f, want 250 / 25.0", saved, pct)
	}
	tSaved, tPct := s.TokenReduction()
	if tSaved != 50 || tPct != 20.0 {
		t.Fatalf("TokenReduction(): saved=%d pct=%f, want 50 / 20.0", tSaved, tPct)
	}

	// Zero-input edge case: no divide-by-zero.
	empty := Stats{}
	if _, pct := empty.Reduction(); pct != 0 {
		t.Fatalf("empty.Reduction pct=%f, want 0", pct)
	}
	if _, pct := empty.TokenReduction(); pct != 0 {
		t.Fatalf("empty.TokenReduction pct=%f, want 0", pct)
	}
}

// TestSynonymOffByDefault verifies the synonym table does not fire with a
// zero Options value.
func TestSynonymOffByDefault(t *testing.T) {
	content := "<thinking>review the configuration and the documentation</thinking>"
	asst := map[string]any{"type": "assistant", "content": content}
	line := mustJSON(t, asst)

	_, stats := runCompress(t, []string{line}, Options{})
	if stats.SynonymsSubstituted != 0 {
		t.Fatalf("SynonymsSubstituted=%d with default opts, want 0", stats.SynonymsSubstituted)
	}
}

// TestSynonymSubstitutionEnabled verifies the default table fires when
// EnableSynonymSub=true. Runs with stop-word removal effectively disabled
// by choosing a language that has no stop-word list so we can inspect the
// post-substitution text directly.
func TestSynonymSubstitutionEnabled(t *testing.T) {
	// Use an unrecognized langCode → stopwords.CleanString is a near-identity
	// (it only collapses duplicate spaces), so our synonym substitution
	// effect is visible end-to-end.
	content := "<thinking>Configuration and documentation for applications</thinking>"
	asst := map[string]any{"type": "assistant", "content": content}
	line := mustJSON(t, asst)

	got, stats := runCompress(t, []string{line}, Options{
		EnableSynonymSub: true,
		StopWordLanguage: "zz", // unused by bbalet → near-identity
	})
	if stats.SynonymsSubstituted == 0 {
		t.Fatalf("SynonymsSubstituted=0 with EnableSynonymSub, want >=1")
	}
	var e struct {
		Content string `json:"content"`
	}
	_ = json.Unmarshal([]byte(got[0]), &e)

	// Inside <thinking>, the substitutions should have fired.
	inner := e.Content
	for _, want := range []string{"config", "docs", "apps"} {
		if !strings.Contains(inner, want) {
			t.Fatalf("synonym %q not applied: %q", want, inner)
		}
	}
	for _, bad := range []string{"configuration", "documentation", "applications"} {
		if strings.Contains(inner, bad) {
			t.Fatalf("original phrase %q still present after substitution: %q", bad, inner)
		}
	}
}

// TestSynonymOnlyInsideThinking verifies synonym substitution does NOT fire
// on assistant prose outside <thinking>, even when enabled.
func TestSynonymOnlyInsideThinking(t *testing.T) {
	content := "The configuration is at /etc/configuration.yaml — see documentation."
	asst := map[string]any{"type": "assistant", "content": content}
	line := mustJSON(t, asst)

	got, stats := runCompress(t, []string{line}, Options{EnableSynonymSub: true, StopWordLanguage: "zz"})
	if stats.SynonymsSubstituted != 0 {
		t.Fatalf("SynonymsSubstituted=%d outside <thinking>, want 0", stats.SynonymsSubstituted)
	}
	var e struct {
		Content string `json:"content"`
	}
	_ = json.Unmarshal([]byte(got[0]), &e)
	if !strings.Contains(e.Content, "configuration") {
		t.Fatalf("prose outside <thinking> was substituted: %q", e.Content)
	}
}

// TestUnknownFieldsPreserved verifies unknown top-level JSON keys survive
// a mutation round-trip via the map[string]json.RawMessage pattern.
func TestUnknownFieldsPreserved(t *testing.T) {
	// "seq" and "future_schema_field" are not part of our entry struct but
	// must appear in output byte-for-byte-equivalent (value-preserving).
	raw := `{"type":"assistant","content":"hello    world","seq":42,"future_schema_field":{"a":1,"b":[2,3]}}`

	got, _ := runCompress(t, []string{raw}, Options{})
	var out map[string]any
	if err := json.Unmarshal([]byte(got[0]), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out["seq"] != float64(42) {
		t.Fatalf("seq field lost or mutated: %v", out["seq"])
	}
	fsf, ok := out["future_schema_field"].(map[string]any)
	if !ok {
		t.Fatalf("future_schema_field lost or wrong shape: %v", out["future_schema_field"])
	}
	if fsf["a"] != float64(1) {
		t.Fatalf("nested unknown field mutated: %v", fsf)
	}
}

// TestLogValueRendersKeyValue verifies Stats emits slog.GroupValue with the
// expected keys so downstream telemetry can grep single-line logs.
func TestLogValueRendersKeyValue(t *testing.T) {
	s := Stats{
		EntriesIn: 3, EntriesOut: 3, BytesIn: 1000, BytesOut: 800,
		NFCNormalized: 1, ZeroWidthStripped: 2, WhitespaceCanonicalized: 1,
		StopWordsRemoved: 1, SynonymsSubstituted: 0,
		TokensInEstimate: 250, TokensOutEstimate: 200,
	}
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	logger.Info("tokenstrip", "stats", s)

	out := buf.String()
	for _, want := range []string{
		"entries_in=3", "entries_out=3",
		"bytes_in=1000", "bytes_out=800",
		"nfc_normalized=1", "zero_width_stripped=2",
		"whitespace_canonicalized=1", "stop_words_removed=1",
		"synonyms_substituted=0", "tokens_in_est=250", "tokens_out_est=200",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("slog output missing %q, got: %s", want, out)
		}
	}
}

// TestCompressEntryPoint exercises the no-options entrypoint end-to-end.
func TestCompressEntryPoint(t *testing.T) {
	lines := []string{
		`{"type":"user","content":"hello"}`,
		`{"type":"assistant","content":"world    end"}`,
	}
	var in bytes.Buffer
	for _, l := range lines {
		in.WriteString(l)
		in.WriteByte('\n')
	}
	var out bytes.Buffer
	stats, err := Compress(&in, &out)
	if err != nil {
		t.Fatalf("Compress: %v", err)
	}
	if stats.EntriesIn != 2 || stats.EntriesOut != 2 {
		t.Fatalf("entries: in=%d out=%d, want 2/2", stats.EntriesIn, stats.EntriesOut)
	}
	if stats.WhitespaceCanonicalized != 1 {
		t.Fatalf("WhitespaceCanonicalized=%d, want 1", stats.WhitespaceCanonicalized)
	}
}

// TestEntryOrderPreserved verifies the output entries appear in input order.
func TestEntryOrderPreserved(t *testing.T) {
	lines := []string{
		`{"type":"header","session_id":"s1"}`,
		`{"type":"user","content":"q1"}`,
		`{"type":"assistant","content":"a1"}`,
		`{"type":"user","content":"q2"}`,
		`{"type":"assistant","content":"a2"}`,
	}
	got, _ := runCompress(t, lines, Options{})
	if len(got) != len(lines) {
		t.Fatalf("got %d lines, want %d", len(got), len(lines))
	}
	for i, want := range lines {
		if got[i] != want {
			t.Fatalf("line %d: got %q want %q", i, got[i], want)
		}
	}
}

// TestReaderErrorPropagates surfaces a non-EOF reader failure.
func TestReaderErrorPropagates(t *testing.T) {
	r := io.MultiReader(
		strings.NewReader(`{"type":"user","content":"ok"}`+"\n"),
		&errReader{},
	)
	_, err := Compress(r, io.Discard)
	if err == nil {
		t.Fatal("expected read error, got nil")
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
