package tokenstrip

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
)

// Stats reports what Compress did. Zero values mean no matches.
type Stats struct {
	EntriesIn  int
	EntriesOut int
	BytesIn    int64
	BytesOut   int64

	NFCNormalized           int // entries where NFC normalization changed content
	ZeroWidthStripped       int // entries where zero-width / unusual whitespace was removed
	WhitespaceCanonicalized int // entries where whitespace collapse changed content
	StopWordsRemoved        int // <thinking> blocks where stop words were removed
	SynonymsSubstituted     int // <thinking> blocks where synonym substitution fired

	// Token estimates use a ~4 chars/token heuristic (Anthropic's rule of
	// thumb). They exist so callers can log a rough token-reduction number
	// without pulling in a BPE-heavy tokenizer. When a real tokenizer is
	// wired in later, swap the estimator in transforms.go and these fields
	// will reflect actual counts.
	TokensInEstimate  int64
	TokensOutEstimate int64
}

// Reduction returns bytes saved and percentage. Safe when BytesIn is zero.
func (s Stats) Reduction() (saved int64, pct float64) {
	saved = s.BytesIn - s.BytesOut
	if s.BytesIn == 0 {
		return saved, 0
	}
	return saved, float64(saved) / float64(s.BytesIn) * 100
}

// TokenReduction returns estimated tokens saved and percentage.
func (s Stats) TokenReduction() (saved int64, pct float64) {
	saved = s.TokensInEstimate - s.TokensOutEstimate
	if s.TokensInEstimate == 0 {
		return saved, 0
	}
	return saved, float64(saved) / float64(s.TokensInEstimate) * 100
}

// Add accumulates other into s. Useful for aggregating across many sessions.
func (s *Stats) Add(other Stats) {
	s.EntriesIn += other.EntriesIn
	s.EntriesOut += other.EntriesOut
	s.BytesIn += other.BytesIn
	s.BytesOut += other.BytesOut
	s.NFCNormalized += other.NFCNormalized
	s.ZeroWidthStripped += other.ZeroWidthStripped
	s.WhitespaceCanonicalized += other.WhitespaceCanonicalized
	s.StopWordsRemoved += other.StopWordsRemoved
	s.SynonymsSubstituted += other.SynonymsSubstituted
	s.TokensInEstimate += other.TokensInEstimate
	s.TokensOutEstimate += other.TokensOutEstimate
}

// LogValue implements slog.LogValuer. Enables single-line key=value telemetry:
//
//	slog.Info("tokenstrip", "stats", stats)
func (s Stats) LogValue() slog.Value {
	_, bytePct := s.Reduction()
	_, tokPct := s.TokenReduction()
	return slog.GroupValue(
		slog.Int("entries_in", s.EntriesIn),
		slog.Int("entries_out", s.EntriesOut),
		slog.Int64("bytes_in", s.BytesIn),
		slog.Int64("bytes_out", s.BytesOut),
		slog.Float64("byte_reduction_pct", bytePct),
		slog.Int64("tokens_in_est", s.TokensInEstimate),
		slog.Int64("tokens_out_est", s.TokensOutEstimate),
		slog.Float64("token_reduction_pct", tokPct),
		slog.Int("nfc_normalized", s.NFCNormalized),
		slog.Int("zero_width_stripped", s.ZeroWidthStripped),
		slog.Int("whitespace_canonicalized", s.WhitespaceCanonicalized),
		slog.Int("stop_words_removed", s.StopWordsRemoved),
		slog.Int("synonyms_substituted", s.SynonymsSubstituted),
	)
}

// Options configures a Compress run. Zero value is a reasonable default
// (English stop words, synonym substitution OFF).
type Options struct {
	// EnableSynonymSub turns on phrase→synonym substitution inside assistant
	// <thinking> blocks. Off by default even when tokenstrip itself is on,
	// because the table is opinionated and can produce awkward reasoning
	// text; callers should opt in explicitly.
	EnableSynonymSub bool

	// SynonymTable overrides the default substitution table. Keys are
	// matched case-insensitively as whole words. A nil map falls back to
	// DefaultSynonymTable().
	SynonymTable map[string]string

	// StopWordLanguage is an ISO-639-1 language code (e.g. "en", "fr").
	// Empty string defaults to "en".
	StopWordLanguage string
}

// DefaultSynonymTable returns the baseline high-token phrase → shorter form
// mapping. Kept short and conservative; callers wanting more aggressive
// shortening should pass their own table.
func DefaultSynonymTable() map[string]string {
	return map[string]string{
		"configuration": "config",
		"documentation": "docs",
		"applications":  "apps",
		"specification": "spec",
	}
}

func (o *Options) withDefaults() {
	if o.StopWordLanguage == "" {
		o.StopWordLanguage = "en"
	}
	if o.SynonymTable == nil {
		o.SynonymTable = DefaultSynonymTable()
	}
}

// Compress reads raw.jsonl entries from r, applies token-aware transforms,
// and writes the transformed stream to w. Equivalent to CompressWith with a
// zero Options value.
func Compress(r io.Reader, w io.Writer) (Stats, error) {
	return CompressWith(r, w, Options{})
}

// CompressWith is Compress with tunable options.
//
// Guarantees:
//   - Single pass over r, bounded memory.
//   - Entry order preserved; nothing is dropped.
//   - User turns and header entries are byte-identical on output.
//   - Unknown top-level JSON fields survive round-trip.
func CompressWith(r io.Reader, w io.Writer, opts Options) (Stats, error) {
	opts.withDefaults()

	var stats Stats
	st := newState(opts)

	// bufio.Reader.ReadBytes('\n') instead of bufio.Scanner — scanner caps
	// token size (default 64KB) and would fail the whole stream on a single
	// oversized entry. Large entries are common in raw.jsonl (base64
	// images, big tool outputs) so the reader must tolerate them.
	br := bufio.NewReader(r)
	bw := bufio.NewWriter(w)

	seq := 0
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			seq++
			hadNL := line[len(line)-1] == '\n'
			entryBytes := line
			if hadNL {
				entryBytes = line[:len(line)-1]
			}
			stats.EntriesIn++
			stats.BytesIn += int64(len(line))
			stats.TokensInEstimate += estimateTokens(entryBytes)

			out, terr := st.transform(entryBytes, &stats)
			if terr != nil {
				return stats, fmt.Errorf("entry %d: %w", seq, terr)
			}
			if _, werr := bw.Write(out); werr != nil {
				return stats, werr
			}
			if _, werr := bw.Write([]byte{'\n'}); werr != nil {
				return stats, werr
			}
			stats.EntriesOut++
			stats.BytesOut += int64(len(out)) + 1
			stats.TokensOutEstimate += estimateTokens(out)
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			return stats, err
		}
	}

	// Surface flush failures explicitly — a deferred Flush would swallow
	// them and let callers believe the output was fully written.
	if err := bw.Flush(); err != nil {
		return stats, fmt.Errorf("flush output: %w", err)
	}
	return stats, nil
}

// entry loosely mirrors the jsonl shape. Unknown fields round-trip via a
// parallel map[string]json.RawMessage in transforms.go.
type entry struct {
	Type       string          `json:"type,omitempty"`
	Content    string          `json:"content,omitempty"`
	Timestamp  string          `json:"timestamp,omitempty"`
	ToolName   string          `json:"tool_name,omitempty"`
	ToolInput  string          `json:"tool_input,omitempty"`
	ToolOutput string          `json:"tool_output,omitempty"`
	Brief      string          `json:"brief,omitempty"`
	IsError    bool            `json:"is_error,omitempty"`
	Metadata   json.RawMessage `json:"metadata,omitempty"`
}
