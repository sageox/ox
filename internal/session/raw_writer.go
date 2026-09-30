package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/internal/fileutil"
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
	return newRawFileWriter(path, projectRoot, os.O_WRONLY|os.O_CREATE|os.O_APPEND)
}

// NewRawWriterTruncate is the same as NewRawWriter but truncates the
// destination first. Used by rewrite paths (session redact, regenerate)
// that produce a fresh raw.jsonl rather than appending. The redaction
// stack is identical — every byte still passes through.
func NewRawWriterTruncate(path, projectRoot string) (*RawWriter, error) {
	return newRawFileWriter(path, projectRoot, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
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
	return w.withAppendLock(func() error { return w.writeEntry(entry) })
}

func (w *RawWriter) writeEntry(entry *SessionEntry) error {
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
	if w.cmdRedactor.overflow {
		return fmt.Errorf("too many unmatched credential-output calls")
	}

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
	return w.withAppendLock(func() error { return w.writeRaw(data) })
}

func (w *RawWriter) writeRaw(data map[string]any) error {
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
	if w == nil || w.closed || w.file == nil {
		return nil
	}
	return w.file.Sync()
}

// Close flushes and closes the underlying file. Idempotent.
func (w *RawWriter) Close() error {
	if w == nil || w.closed || w.file == nil {
		return nil
	}
	w.closed = true
	return w.file.Close()
}

// CloseAndSync is a convenience for callers that want fsync + close in
// one shot at the end of a write session.
func (w *RawWriter) CloseAndSync() error {
	if w == nil || w.closed || w.file == nil {
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

// NewRawStreamWriter applies exactly the raw-file redaction stack to a stream.
// Import previews use io.Discard so inspecting history never creates artifacts.
// Policy errors are fatal for new imports; silently ignoring a malformed rule
// would publish content the repository owner intended to exclude.
func NewRawStreamWriter(dst io.Writer, projectRoot string) (*RawWriter, error) {
	redactor, problems := NewRedactorWithCustomRules(projectRoot)
	if len(problems) != 0 {
		return nil, fmt.Errorf("invalid redaction policy (%d errors)", len(problems))
	}
	w := newRawWriterFromFile(nil, "")
	w.encoder = json.NewEncoder(dst)
	w.redactor = redactor
	return w, nil
}

// NewRawSnapshotWriter validates repository policy before truncating any output.
// Import application must use the same strict policy as its read-only preview.
func NewRawSnapshotWriter(path, projectRoot string) (*RawWriter, error) {
	return NewRawWriterTruncate(path, projectRoot)
}
func newRawFileWriter(path, projectRoot string, flags int) (*RawWriter, error) {
	w, err := NewRawStreamWriter(io.Discard, projectRoot)
	if err != nil {
		return nil, err
	}
	// Validate policy before creating or truncating any persistent content.
	f, err := os.OpenFile(path, flags, 0600)
	if err != nil {
		return nil, err
	}
	w.file = f
	w.encoder = json.NewEncoder(f)
	return w, nil
}

// rawAppendJournalSuffix names the batch journal kept beside raw.jsonl while a
// capture batch is in flight. It is machine-local recovery state, never
// session content, so it must not travel with the session into the Ledger.
const rawAppendJournalSuffix = ".append.json"

// IsRawAppendJournal reports whether a file name is a capture batch journal.
func IsRawAppendJournal(name string) bool {
	return strings.HasSuffix(name, rawAppendJournalSuffix)
}

type rawAppendCheckpoint struct {
	RawSize   int64  `json:"raw_size"`
	FinalSize *int64 `json:"final_size,omitempty"`
	OldOffset int64  `json:"old_offset"`
	NewOffset int64  `json:"new_offset"`
}

// BeginAppend journals the rollback point before any batch bytes are written.
// The caller must hold the raw capture lock through cursor persistence.
func (w *RawWriter) BeginAppend(oldOffset, newOffset int64) error {
	info, err := w.file.Stat()
	if err != nil {
		return err
	}
	data, err := json.Marshal(rawAppendCheckpoint{RawSize: info.Size(), OldOffset: oldOffset, NewOffset: newOffset})
	if err != nil {
		return err
	}
	return fileutil.AtomicWriteBytes(w.file.Name()+rawAppendJournalSuffix, data, 0600)
}

// SealAppend persists the exact fsynced output length before the cursor can
// advance. Recovery must reject a replaced/truncated file behind a committed cursor.
func (w *RawWriter) SealAppend() error {
	if err := w.Sync(); err != nil {
		return err
	}
	path := w.file.Name() + rawAppendJournalSuffix
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var checkpoint rawAppendCheckpoint
	if err := json.Unmarshal(data, &checkpoint); err != nil {
		return err
	}
	info, err := w.file.Stat()
	if err != nil {
		return err
	}
	size := info.Size()
	if size < checkpoint.RawSize {
		return fmt.Errorf("captured file was truncated")
	}
	checkpoint.FinalSize = &size
	data, err = json.Marshal(checkpoint)
	if err != nil {
		return err
	}
	return fileutil.AtomicWriteBytes(path, data, 0600)
}

func (w *RawWriter) FinishAppend() error { return os.Remove(w.file.Name() + rawAppendJournalSuffix) }

// RecoverRawAppend discards only an unacknowledged batch. A cursor is committed
// after raw fsync; if its atomic replacement survived, its batch must survive too.
// Call under the capture lock before reading, stopping, or reopening the writer.
//
// Carrier footers (StampRawCarrier) are the one thing that may legitimately
// land on raw.jsonl between a crash and this recovery: a SessionEnd hook or
// `session native` stamps them without consulting the journal. They carry only
// identifiers and timestamps, never batch content, so a rollback keeps them and
// a committed check accepts them as the only bytes allowed past the sealed size.
func RecoverRawAppend(path string, persistedOffset int64) error {
	// Under the append lock as well: a stamp holds only that lock, and a
	// rollback must not truncate underneath one that is mid-write.
	return withRawAppendLock(path, func() error { return recoverRawAppend(path, persistedOffset) })
}

// footerTailAfter returns the complete footer lines found past offset. Anything
// else there -- a complete non-footer line, or a torn partial line -- is
// reported in other; the caller decides whether that content is the batch
// being rolled back (dropped) or a conflict with a committed batch (refused).
func footerTailAfter(path string, offset int64) (footers []byte, other bool, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	if _, err = f.Seek(offset, io.SeekStart); err != nil {
		return nil, false, err
	}
	tail, err := io.ReadAll(f)
	if err != nil {
		return nil, false, err
	}
	for len(tail) > 0 {
		nl := strings.IndexByte(string(tail), '\n')
		if nl < 0 {
			return footers, true, nil // torn partial line
		}
		line, rest := tail[:nl+1], tail[nl+1:]
		tail = rest
		if strings.TrimSpace(string(line)) == "" {
			continue
		}
		var record struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(line, &record) != nil || record.Type != "footer" {
			other = true
			continue
		}
		footers = append(footers, line...)
	}
	return footers, other, nil
}

func recoverRawAppend(path string, persistedOffset int64) error {
	checkpointPath := path + rawAppendJournalSuffix
	data, err := os.ReadFile(checkpointPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var checkpoint rawAppendCheckpoint
	if err = json.Unmarshal(data, &checkpoint); err != nil {
		return err
	}
	// Mirror what the writer can produce. SealAppend never records a final size
	// below the size it started from, and cursors are byte offsets: a journal
	// claiming otherwise is corrupt, and "committed" read off it would bless a
	// raw file that has lost content since the batch began.
	if checkpoint.RawSize < 0 || checkpoint.OldOffset < 0 || checkpoint.NewOffset <= checkpoint.OldOffset ||
		(checkpoint.FinalSize != nil && *checkpoint.FinalSize < checkpoint.RawSize) {
		return fmt.Errorf("invalid capture checkpoint")
	}
	switch persistedOffset {
	case checkpoint.OldOffset:
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		if info.Size() < checkpoint.RawSize {
			return fmt.Errorf("captured file was truncated")
		}
		// Everything past the journal's size is the batch that never committed,
		// except footers stamped after the crash: those go back on the end.
		footers, _, err := footerTailAfter(path, checkpoint.RawSize)
		if err != nil {
			return err
		}
		f, err := os.OpenFile(path, os.O_WRONLY, 0)
		if err != nil {
			return err
		}
		if err = f.Truncate(checkpoint.RawSize); err == nil && len(footers) > 0 {
			_, err = f.WriteAt(footers, checkpoint.RawSize)
		}
		if err == nil {
			err = f.Sync()
		}
		closeErr := f.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	case checkpoint.NewOffset:
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		if checkpoint.FinalSize == nil || info.Size() < *checkpoint.FinalSize {
			return fmt.Errorf("committed capture size conflicts with pending batch")
		}
		// A file longer than it was sealed at is only explained by footers
		// stamped since; any other content means it is not the file that was sealed.
		if info.Size() > *checkpoint.FinalSize {
			_, other, err := footerTailAfter(path, *checkpoint.FinalSize)
			if err != nil {
				return err
			}
			if other {
				return fmt.Errorf("committed capture size conflicts with pending batch")
			}
		}
	default:
		return fmt.Errorf("capture cursor conflicts with pending batch")
	}
	return os.Remove(checkpointPath)
}

// AppendRecordingBatch requires the raw capture lock; the nested state lock
// keeps a pause/resume from crossing the batch's sequence/cursor boundary.
//
// The whole journal sequence -- begin, entries, seal, cursor commit, finish --
// runs under one hold of the append lock, so a carrier stamp cannot land
// between the sizes the journal records and the size recovery later checks.
// Entries go through writeEntry: the lock is not re-entrant, and it is
// already held. Lock order everywhere is raw capture -> append -> state.
func (w *RawWriter) AppendRecordingBatch(statePath string, entries []Entry, newOffset int64) error {
	return w.withAppendLock(func() error { return w.appendRecordingBatch(statePath, entries, newOffset) })
}

func (w *RawWriter) appendRecordingBatch(statePath string, entries []Entry, newOffset int64) error {
	err := MutateRecordingStateFile(statePath, func(state *RecordingState) error {
		if newOffset <= state.SourceOffset {
			return fmt.Errorf("capture cursor did not advance")
		}
		if err := w.RestoreCaptureRedaction(state, w.file.Name()); err != nil {
			return err
		}
		if err := w.BeginAppend(state.SourceOffset, newOffset); err != nil {
			return err
		}
		for i := range entries {
			if err := w.writeEntry(&entries[i]); err != nil {
				return err
			}
		}
		if err := w.SealAppend(); err != nil {
			return err
		}
		state.CommandRedactionVersion = 1
		state.PendingCommandRedactions = w.cmdRedactor.pending
		state.SourceOffset = newOffset
		state.EntryCount += len(entries)
		return nil
	})
	if err != nil {
		return err
	}
	return w.FinishAppend()
}

// withRawAppendLock serializes individual appends with checkpoint rollback.
// This is separate from the watcher's lifetime raw-file lock: hooks must be
// able to append a footer while the watcher is alive. Lock files use the
// existing fileutil temporary lock directory, never the session directory.
func withRawAppendLock(path string, fn func() error) error {
	// Resolve aliases before fileutil makes the key absolute and hashes it.
	// Keep the original path on failure so the writer reports its I/O error.
	if canonical, err := filepath.EvalSymlinks(path); err == nil {
		path = canonical
	}
	return fileutil.WithFileLock(context.Background(), path+".append", fn)
}

func (w *RawWriter) withAppendLock(fn func() error) error {
	if w == nil {
		return fmt.Errorf("raw writer: nil")
	}
	// A stream writer (NewRawStreamWriter) has no shared file to serialize on.
	if w.file == nil {
		return fn()
	}
	return withRawAppendLock(w.file.Name(), fn)
}
