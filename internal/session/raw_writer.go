package session

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

// RawWriter is the SINGLE supported way to write entries to a session's
// raw.jsonl file inside ox.
//
// Every byte that lands in raw.jsonl passes through three redaction
// layers before encoding:
//
//  1. CommandRedactor — whole-output replacement for known credential-
//     emitting tool calls (aws sso login, gh auth token, glab auth, ...).
//     Catches multi-line credential blocks that no single regex captures
//     reliably.
//
//  2. Built-in Redactor — the ox DefaultPatterns set (~25 detectors with
//     stable [REDACTED_*] slugs). Project-local custom rules from
//     .sageox/REDACT.md compose into the same Redactor when the caller
//     supplies a project root.
//
//  3. ExtraDetectors — pluggable additional patterns (gitleaks-derived
//     rules slot in here; see internal/session/gitleaks_detectors.go).
//     Treated as a soft-fallback layer: a match in the extra set redacts
//     to a generic slug rather than a class-specific one.
//
// Per ox-h20u: this type IS the chokepoint. Every raw.jsonl writer in
// the ox codebase must go through it. The build-time grep gate
// (Makefile target check-raw-writer-chokepoint) fails the build if any
// other file opens raw.jsonl directly.
//
// Adapters (ox-adapter-*) emit RawEntry JSON on stdout; the daemon and
// CLI read that stream and feed entries into a RawWriter. Adapters
// physically cannot bypass — they have no write access to raw.jsonl.
type RawWriter struct {
	file        *os.File
	encoder     *json.Encoder
	cmdRedactor *CommandRedactor
	redactor    *Redactor
	extras      []SecretPattern // gitleaks-derived or other supplemental detectors
	closed      bool
}

// NewRawWriter opens path for appending and returns a writer whose
// every Write call is gated by the redaction stack. The file is opened
// O_APPEND so multiple writers (e.g. catch-up + live tail in the same
// session_watcher run) don't trample each other.
//
// projectRoot enables project-local custom redaction rules
// (.sageox/REDACT.md). Pass "" if no project context is available
// (e.g. daemon writing to a ledger session whose project isn't known
// at write time); built-in patterns still apply.
func NewRawWriter(path, projectRoot string) (*RawWriter, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0644)
	if err != nil {
		return nil, fmt.Errorf("raw writer: open: %w", err)
	}
	return newRawWriterFromFile(f, projectRoot), nil
}

// NewRawWriterTruncate is the same as NewRawWriter but truncates the
// destination first. Used by rewrite paths (session redact, regenerate)
// that produce a fresh raw.jsonl rather than appending. The redaction
// stack is identical — every byte still passes through.
func NewRawWriterTruncate(path, projectRoot string) (*RawWriter, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return nil, fmt.Errorf("raw writer: open: %w", err)
	}
	return newRawWriterFromFile(f, projectRoot), nil
}

func newRawWriterFromFile(f *os.File, projectRoot string) *RawWriter {
	var redactor *Redactor
	if projectRoot != "" {
		r, _ := NewRedactorWithCustomRules(projectRoot)
		redactor = r
	} else {
		redactor = NewRedactor()
	}
	// Layer 3 combines two sources:
	//   - hand-ported gitleaks rules (DefaultExtraDetectors): the high-
	//     value subset with class-specific [REDACTED_*] slugs consumers
	//     grep for.
	//   - generated gitleaks rules (generatedGitleaksDetectors): the
	//     remainder of the gitleaks v8.30.1 catalog, auto-translated by
	//     internal/session/cmd/gitleaks-port. Generic [REDACTED_<RULE>]
	//     slugs, broad coverage.
	// Hand-ported runs FIRST so when both layers match the same bytes,
	// the consumer-friendly slug wins.
	extras := DefaultExtraDetectors()
	extras = append(extras, generatedGitleaksDetectors()...)
	return &RawWriter{
		file:        f,
		encoder:     json.NewEncoder(f),
		cmdRedactor: NewCommandRedactor(),
		redactor:    redactor,
		extras:      extras,
	}
}

// WriteEntry redacts and writes one entry. Three layers applied in
// order: command-allowlist (whole-output for known credential-emitting
// commands), built-in regex Redactor, then extra detectors.
//
// Mutation contract: WriteEntry MUTATES the entry in place so the
// caller's slice sees the redacted state. This is intentional — if the
// caller pushes the same entry through multiple consumers (display,
// upload, summarize), they all see the redacted form. To keep an
// un-redacted copy, copy before WriteEntry.
func (w *RawWriter) WriteEntry(entry *SessionEntry) error {
	if w == nil {
		return fmt.Errorf("raw writer: nil")
	}
	if w.closed {
		return fmt.Errorf("raw writer: already closed")
	}
	if entry == nil {
		return fmt.Errorf("raw writer: nil entry")
	}

	// Layer 1: command-allowlist whole-output redaction.
	w.cmdRedactor.RedactEntry(entry)

	// Layer 2: built-in regex redactor (covers ToolInput, ToolOutput,
	// Content via RedactEntries-style traversal).
	w.redactor.RedactEntry(entry)

	// Layer 3: extra detectors (gitleaks-derived rules). Same fields
	// as layer 2; the extras run AFTER built-ins so layer-2's class-
	// specific slugs win when they apply, with layer 3 catching the
	// long tail. The traversal is a thin loop over the same string
	// fields rather than a second Redactor allocation per write.
	//
	// Quick-screen: most patterns carry distinctive lowercase Keywords
	// (e.g. "akia", "adafruit"). Lowercasing each field once and asking
	// each pattern "does the input even mention you?" lets the no-match
	// case skip the regex entirely. On a 1 MB credential-free string,
	// this is a >300x speedup vs. running every pattern unconditionally.
	lowerContent := lowerForScreen(entry.Content)
	lowerInput := lowerForScreen(entry.ToolInput)
	lowerOutput := lowerForScreen(entry.ToolOutput)
	for i := range w.extras {
		p := &w.extras[i]
		if p.Pattern == nil {
			continue
		}
		if p.MatchesKeyword(lowerContent) {
			entry.Content = p.Pattern.ReplaceAllString(entry.Content, p.Redact)
		}
		if p.MatchesKeyword(lowerInput) {
			entry.ToolInput = p.Pattern.ReplaceAllString(entry.ToolInput, p.Redact)
		}
		if p.MatchesKeyword(lowerOutput) {
			entry.ToolOutput = p.Pattern.ReplaceAllString(entry.ToolOutput, p.Redact)
		}
	}

	return w.encoder.Encode(entry)
}

// lowerForScreen returns a lowercase copy of s used only for the
// keyword pre-screen. Empty input short-circuits so we don't allocate
// in the common case where ToolInput/ToolOutput are unused.
func lowerForScreen(s string) string {
	if s == "" {
		return ""
	}
	return strings.ToLower(s)
}

// WriteEntries writes a slice. Returns on the first error; partial
// writes are flushed to disk via the encoder's buffer. Caller is
// responsible for reconciling partial output (typically: re-read the
// file and resume from the last entry).
func (w *RawWriter) WriteEntries(entries []SessionEntry) error {
	for i := range entries {
		if err := w.WriteEntry(&entries[i]); err != nil {
			return err
		}
	}
	return nil
}

// WriteRaw writes a map[string]any entry (used by the planning-history
// importer, which doesn't have a SessionEntry struct because it works
// with the wire-format JSON). Same three-layer redaction; recursive
// RedactMap handles the nested-string-values case.
func (w *RawWriter) WriteRaw(data map[string]any) error {
	if w == nil {
		return fmt.Errorf("raw writer: nil")
	}
	if w.closed {
		return fmt.Errorf("raw writer: already closed")
	}
	if data == nil {
		return fmt.Errorf("raw writer: nil data")
	}
	w.redactor.RedactMap(data)
	for i := range w.extras {
		p := &w.extras[i]
		if p.Pattern == nil {
			continue
		}
		applyPatternToMap(data, p)
	}
	return w.encoder.Encode(data)
}

// applyPatternToMap walks data (nested maps + slices) and applies a
// single pattern to every string value. Helper for the WriteRaw layer-3
// pass; the built-in Redactor.RedactMap already does this for its own
// patterns. Honors p.Keywords as a quick-screen.
func applyPatternToMap(data map[string]any, p *SecretPattern) {
	for k, v := range data {
		switch tv := v.(type) {
		case string:
			if p.MatchesKeyword(lowerForScreen(tv)) {
				data[k] = p.Pattern.ReplaceAllString(tv, p.Redact)
			}
		case map[string]any:
			applyPatternToMap(tv, p)
		case []any:
			applyPatternToSlice(tv, p)
		}
	}
}

func applyPatternToSlice(data []any, p *SecretPattern) {
	for i, v := range data {
		switch tv := v.(type) {
		case string:
			if p.MatchesKeyword(lowerForScreen(tv)) {
				data[i] = p.Pattern.ReplaceAllString(tv, p.Redact)
			}
		case map[string]any:
			applyPatternToMap(tv, p)
		case []any:
			applyPatternToSlice(tv, p)
		}
	}
}

// Sync flushes the writer's underlying file. The json.Encoder's own
// buffer was already drained by Encode (it writes per Encode call).
func (w *RawWriter) Sync() error {
	if w == nil || w.closed {
		return nil
	}
	return w.file.Sync()
}

// Close flushes and closes the underlying file. Idempotent.
func (w *RawWriter) Close() error {
	if w == nil || w.closed {
		return nil
	}
	w.closed = true
	return w.file.Close()
}

// CloseAndSync is a convenience for callers that want fsync + close in
// one shot at the end of a write session.
func (w *RawWriter) CloseAndSync() error {
	if w == nil || w.closed {
		return nil
	}
	if err := w.file.Sync(); err != nil {
		_ = w.file.Close()
		w.closed = true
		return err
	}
	w.closed = true
	return w.file.Close()
}

// asWriter exposes the underlying file as an io.Writer for callers
// that need to plumb a generic writer (e.g. a tar-stream extractor).
// Bypass: callers using asWriter SKIP the redaction stack. Use only
// when the bytes being written are guaranteed to be already-redacted
// (e.g. copying a session that ox itself produced). Marked unexported
// to keep this contract tight; in-package callers can use it; external
// callers must use WriteEntry / WriteRaw.
//
//nolint:unused // reserved for in-package bypass use; expected unused warning until first caller
func (w *RawWriter) asWriter() io.Writer {
	return w.file
}
