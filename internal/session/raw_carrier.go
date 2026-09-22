package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/trace/model"
)

// CarrierStamp is what a finalize door appends to raw.jsonl just before the
// recording-state file goes away. Fields are optional: a door that only
// knows one of them carries that one, and a later stamp wins per field when
// the file is read.
type CarrierStamp struct {
	TraceCapture   *model.Capture
	NativeSessions []lfs.NativeSession
	StoppedAt      time.Time
}

// StampRawCarrier appends one footer record carrying the stamp to rawPath.
// It is the crash-safe hand-off for recording fields the daemon
// cannot otherwise learn: .recording.json is deleted at SessionEnd, /clear
// and orphan-sweep time, but raw.jsonl survives into the ledger and every
// finalize door reads it (ReadSessionFromPath folds the footer's fields into
// StoreMeta, so consumers see one metadata view).
//
// It APPENDS and never rewrites. A live raw.jsonl can have open appenders —
// the daemon's tail watcher keeps one O_APPEND descriptor per session for as
// long as it runs, and parallel PostToolUse hooks open their own — and a
// rewrite-by-rename leaves them writing into the unlinked inode, silently
// losing every entry they accept after the stamp. An O_APPEND write of one
// short line coexists with them (TestStampRawCarrier_KeepsEntriesFromAnOpenAppender).
//
// The record is a footer ({"type":"footer",...}): the one line kind every
// raw.jsonl reader already treats as framing rather than content, so a
// consumer that predates the carrier skips it instead of rendering it as a
// turn. A file may end up with more than one footer; readers merge them and
// take the last value of each field. An empty native-session list is never
// written — a present empty list reads as a deliberate override, and a door
// that merely has no ids must not erase ones an earlier line carried.
//
// Every byte goes through RawWriter like any other raw.jsonl write; the
// only content is identifiers, timestamps, and byte boundaries, never conversation text.
//
// A missing file or an LFS pointer is reported as an error and nothing is
// written; callers treat the stamp as best-effort and log.
func StampRawCarrier(rawPath string, stamp CarrierStamp) error {
	if rawPath == "" {
		return errors.New("stamp raw carrier: empty path")
	}
	if lfs.IsPointerFile(rawPath) {
		return fmt.Errorf("stamp raw carrier: %s is an LFS pointer", rawPath)
	}
	info, err := os.Stat(rawPath)
	if err != nil {
		return fmt.Errorf("stamp raw carrier: %w", err)
	}

	record := map[string]any{"type": "footer"}
	if !stamp.StoppedAt.IsZero() {
		at := stamp.StoppedAt.UTC().Format(time.RFC3339Nano)
		record["closed_at"] = at
		record["stopped_at"] = at
	}
	if len(stamp.NativeSessions) > 0 {
		data, err := json.Marshal(stamp.NativeSessions)
		if err != nil {
			return fmt.Errorf("stamp raw carrier: encode native sessions: %w", err)
		}
		var generic any
		if err := json.Unmarshal(data, &generic); err != nil {
			return fmt.Errorf("stamp raw carrier: decode native sessions: %w", err)
		}
		record["native_sessions"] = generic
	}
	if stamp.TraceCapture != nil {
		data, err := json.Marshal(stamp.TraceCapture)
		if err != nil {
			return fmt.Errorf("stamp trace capture: %w", err)
		}
		var generic any
		if err := json.Unmarshal(data, &generic); err != nil {
			return err
		}
		record["trace_capture"] = generic
	}
	if len(record) == 1 {
		return nil // nothing to carry
	}

	// A torn last line (a writer that died mid-entry) must not swallow the
	// footer: make sure the file ends on a line boundary first. Same
	// O_APPEND discipline as the record itself.
	if err := ensureTrailingNewline(rawPath, info.Size()); err != nil {
		return fmt.Errorf("stamp raw carrier: %w", err)
	}

	w, err := NewRawWriter(rawPath, "")
	if err != nil {
		return fmt.Errorf("stamp raw carrier: open: %w", err)
	}
	if err := w.WriteRaw(record); err != nil {
		_ = w.Close()
		return fmt.Errorf("stamp raw carrier: append: %w", err)
	}
	if err := w.CloseAndSync(); err != nil {
		return fmt.Errorf("stamp raw carrier: sync: %w", err)
	}
	return nil
}

// ensureTrailingNewline appends a newline to path when its last byte is not
// one. size is the file's current length; an empty file needs nothing.
func ensureTrailingNewline(path string, size int64) error {
	if size == 0 {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	last := make([]byte, 1)
	_, err = f.ReadAt(last, size-1)
	_ = f.Close()
	if err != nil {
		return err
	}
	if last[0] == '\n' {
		return nil
	}
	w, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	if _, err := w.Write([]byte{'\n'}); err != nil {
		_ = w.Close()
		return err
	}
	return w.Close()
}

// ResolveStoppedAt picks the recording stop time for meta.json from the
// sources a finalize door may have, most authoritative first:
//
//  1. requested — the moment the stop was asked for (explicit stop,
//     SessionEnd, /clear), as carried in RecordingState.StoppedAt or the
//     door's own clock;
//  2. the stopped_at already carried in raw.jsonl — on its header when the
//     file was written whole at stop, or on the footer a hook door appended
//     before the daemon finalized;
//  3. the timestamp of the last entry in raw.jsonl — for a recording whose
//     owner died, the last thing it recorded is a far better estimate than
//     the sweep that noticed hours later;
//  4. the raw.jsonl file's modification time — the last write the recording
//     made, when its entries carry no timestamps;
//  5. fallback — the finalize time, so the field is never left unset.
//
// Every door goes through this so the precedence has exactly one home. Steps
// 2–4 are all properties of the recording itself, so a retried upload
// resolves the same instant as the attempt before it and meta.json stays
// byte-identical across retries; only step 5 varies per call, and it is
// reached only when there is no raw.jsonl at all.
func ResolveStoppedAt(requested *time.Time, rawPath string, fallback time.Time) time.Time {
	if requested != nil && !requested.IsZero() {
		return requested.UTC()
	}
	if rawPath != "" && !lfs.IsPointerFile(rawPath) {
		if stored, err := ReadSessionFromPath(rawPath); err == nil && stored != nil {
			if stored.Meta != nil && stored.Meta.StoppedAt != nil && !stored.Meta.StoppedAt.IsZero() {
				return stored.Meta.StoppedAt.UTC()
			}
			if last := lastEntryTimestamp(stored.Entries); !last.IsZero() {
				return last.UTC()
			}
		}
		if fi, err := os.Stat(rawPath); err == nil && !fi.ModTime().IsZero() {
			return fi.ModTime().UTC()
		}
	}
	return fallback.UTC()
}

// lastEntryTimestamp returns the latest parseable timestamp among entries
// (both the native "timestamp" key and the import dialect's "ts"), or zero.
func lastEntryTimestamp(entries []map[string]any) time.Time {
	var last time.Time
	for _, entry := range entries {
		for _, key := range []string{"timestamp", "ts"} {
			raw, ok := entry[key].(string)
			if !ok || raw == "" {
				continue
			}
			if t, valid := ParseTimestamp(raw); valid && t.After(last) {
				last = t
			}
		}
	}
	return last
}
