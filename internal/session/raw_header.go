package session

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/sageox/ox/internal/lfs"
)

// HeaderStamp is what a finalize door writes into the raw.jsonl header just
// before the recording state file goes away. Both fields are optional; a
// zero StoppedAt leaves any existing stopped_at untouched and a nil
// NativeSessions leaves any existing native_sessions untouched, so a door
// that only knows one of them cannot erase the other.
type HeaderStamp struct {
	NativeSessions []lfs.NativeSession
	StoppedAt      time.Time
}

// StampRawHeader rewrites the first line of rawPath so its metadata carries
// the stamp. It is the crash-safe hand-off for the two recording fields the
// daemon cannot otherwise learn: .recording.json is deleted at SessionEnd,
// /clear and orphan-sweep time, but the header survives into the ledger and
// every finalize door reads it.
//
// Both header dialects are handled — {"type":"header","metadata":{...}} and
// the import-style {"_meta":{...}}. Every other key in the header, and every
// byte after the first newline, is copied verbatim; the file is replaced
// atomically (tmp + rename) so a crash mid-rewrite leaves the original.
//
// Redaction: the only bytes this writes that RawWriter did not already
// redact are the stamp's ids and timestamps, which ox itself minted or the
// agent's hook reported — never conversation content. The remainder of the
// file is copied unchanged from a file RawWriter produced.
//
// A missing file or a first line that is not a header is reported as an
// error and nothing is written; callers treat this as best-effort and log.
func StampRawHeader(rawPath string, stamp HeaderStamp) error {
	if rawPath == "" {
		return errors.New("stamp raw header: empty path")
	}
	if lfs.IsPointerFile(rawPath) {
		return fmt.Errorf("stamp raw header: %s is an LFS pointer", rawPath)
	}
	src, err := os.Open(rawPath)
	if err != nil {
		return fmt.Errorf("stamp raw header: open: %w", err)
	}
	defer src.Close()

	reader := bufio.NewReaderSize(src, 1024*1024)
	first, err := reader.ReadBytes('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("stamp raw header: read first line: %w", err)
	}
	if len(first) == 0 {
		return fmt.Errorf("stamp raw header: %s is empty", rawPath)
	}
	hadNewline := first[len(first)-1] == '\n'

	var header map[string]any
	if err := json.Unmarshal(first, &header); err != nil {
		return fmt.Errorf("stamp raw header: first line is not JSON: %w", err)
	}
	meta, key := headerMetadata(header)
	if meta == nil {
		return fmt.Errorf("stamp raw header: first line of %s is not a session header", rawPath)
	}
	applyHeaderStamp(meta, stamp)
	header[key] = meta

	encoded, err := json.Marshal(header)
	if err != nil {
		return fmt.Errorf("stamp raw header: encode: %w", err)
	}

	tmpPath := rawPath + ".stamp.tmp"
	// 0600 to match RawWriter: the file holds conversation content.
	dst, err := os.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("stamp raw header: create tmp: %w", err)
	}
	defer os.Remove(tmpPath)

	if _, err := dst.Write(append(encoded, '\n')); err != nil {
		_ = dst.Close()
		return fmt.Errorf("stamp raw header: write header: %w", err)
	}
	if hadNewline {
		if _, err := io.Copy(dst, reader); err != nil {
			_ = dst.Close()
			return fmt.Errorf("stamp raw header: copy body: %w", err)
		}
	}
	if err := dst.Sync(); err != nil {
		_ = dst.Close()
		return fmt.Errorf("stamp raw header: sync: %w", err)
	}
	if err := dst.Close(); err != nil {
		return fmt.Errorf("stamp raw header: close: %w", err)
	}
	if err := os.Rename(tmpPath, rawPath); err != nil {
		return fmt.Errorf("stamp raw header: replace: %w", err)
	}
	return nil
}

// headerMetadata returns the metadata object of a parsed header line and the
// key it lives under ("metadata" for the native dialect, "_meta" for the
// import dialect), or nil when the line is not a header.
func headerMetadata(header map[string]any) (map[string]any, string) {
	if header["type"] == "header" {
		if meta, ok := header["metadata"].(map[string]any); ok {
			return meta, "metadata"
		}
		return nil, ""
	}
	if meta, ok := header["_meta"].(map[string]any); ok {
		return meta, "_meta"
	}
	return nil, ""
}

// applyHeaderStamp writes the stamp's populated fields into meta as plain
// JSON values (round-tripped through the lfs.NativeSession tags so the
// header and meta.json agree byte-for-byte on the shape).
func applyHeaderStamp(meta map[string]any, stamp HeaderStamp) {
	if stamp.NativeSessions != nil {
		data, err := json.Marshal(stamp.NativeSessions)
		if err == nil {
			var generic any
			if json.Unmarshal(data, &generic) == nil {
				meta["native_sessions"] = generic
			}
		}
	}
	if !stamp.StoppedAt.IsZero() {
		meta["stopped_at"] = stamp.StoppedAt.UTC().Format(time.RFC3339Nano)
	}
}

// ResolveStoppedAt picks the recording stop time for meta.json from the
// sources a finalize door may have, most authoritative first:
//
//  1. requested — the moment the stop was asked for (explicit stop,
//     SessionEnd, /clear), as carried in RecordingState.StoppedAt or the
//     door's own clock;
//  2. the stopped_at already stamped in the raw.jsonl header by an earlier
//     door (a hook that ran before the daemon finalized);
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
