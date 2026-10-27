package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/consultscan"
	"github.com/sageox/ox/internal/session/contexttrace"
	"github.com/sageox/ox/internal/session/contextuse"
)

// Live use evidence for `ox brief`. Finalize (tagSessionConsults) is the
// writer of record for `used` events; until then a recording's trace holds
// only self-reports, so a brief taken mid-session would say nothing surfaced
// was used even when the AI coworker re-opened it or named it in its reply.
// This file runs the same deterministic scan (consultscan.ScanUses, same
// roots, same surfaced set) over the live raw.jsonl and adds what it finds to
// the brief IN MEMORY. It never writes the trace; finalize re-scans from zero
// and writes the identical events.

// briefUsesCacheSubdir holds the incremental scan cache, one file per AI
// coworker, under the ledger's local-only .sageox/cache/ (ledger-cache rule).
const briefUsesCacheSubdir = "brief-uses"

// loadRawEntriesFrom is consultscan.LoadRawEntriesFrom; a var so tests can
// observe the offsets the brief reads from.
var loadRawEntriesFrom = consultscan.LoadRawEntriesFrom

// briefUsesCache is the incremental scan state for one recording: the raw.jsonl
// byte offset and entry index scanned through, how many surfaced items the
// scan saw, and the used events found so far.
type briefUsesCache struct {
	Session       string `json:"session"`
	Offset        int64  `json:"offset"`
	NextIndex     int    `json:"next_index"`
	SurfacedCount int    `json:"surfaced_count"`
	// TailSHA fingerprints the bytes just before Offset, so a raw.jsonl
	// rewritten to the same or a greater length still rescans.
	TailSHA string               `json:"tail_sha,omitempty"`
	Events  []contexttrace.Event `json:"events"`
}

// briefUsesTailBytes is how much of raw.jsonl before the cached offset the
// fingerprint covers: the last scanned line or so.
const briefUsesTailBytes = 512

// rawTailSHA is the sha256 of the up-to-briefUsesTailBytes bytes of rawPath
// that end at offset ("" at offset 0).
func rawTailSHA(rawPath string, offset int64) (string, error) {
	if offset <= 0 {
		return "", nil
	}
	f, err := os.Open(rawPath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	start := max(offset-briefUsesTailBytes, 0)
	buf := make([]byte, offset-start)
	if _, err := f.ReadAt(buf, start); err != nil {
		return "", err
	}
	sum := sha256.Sum256(buf)
	return hex.EncodeToString(sum[:]), nil
}

// withLiveUses returns events plus the use evidence the live raw.jsonl holds
// for items they surfaced, minus any the trace already records. Fail-open: any
// problem returns events unchanged, so the brief is what it was before.
func withLiveUses(projectRoot string, state *session.RecordingState, events []contexttrace.Event) (out []contexttrace.Event) {
	out = events
	defer func() {
		if r := recover(); r != nil {
			slog.Debug("brief live uses: recovered", "panic", r)
			out = events
		}
	}()
	if state == nil || state.SessionPath == "" || !config.TraceConsultationsEnabled() {
		return events
	}
	surfaced := contextuse.Surfaced(events)
	if len(surfaced) == 0 {
		return events
	}
	roots := consultRoots(projectRoot)
	uses, fresh, err := scanLiveUses(roots, state, surfaced)
	if err != nil {
		slog.Debug("brief live uses: scan failed", "session", filepath.Base(state.SessionPath), "err", err)
		return events
	}

	inTrace := map[string]bool{}
	for _, ev := range events {
		inTrace[liveUseKey(ev)] = true
	}
	added := make([]contexttrace.Event, 0, len(uses))
	for _, ev := range uses {
		if !inTrace[liveUseKey(ev)] {
			added = append(added, ev)
		}
	}
	for _, ev := range fresh {
		if s, ok := surfacedFor(surfaced, ev); ok {
			recordStatuslineUse(projectRoot, state, ev.RefType, ev.Ref, s.Aliases...)
		}
	}
	if len(added) == 0 {
		return events
	}
	out = make([]contexttrace.Event, 0, len(events)+len(added))
	out = append(out, events...)
	return append(out, added...)
}

// scanLiveUses returns every use the recording's raw.jsonl shows for the
// surfaced items, and which of them this call found for the first time. It
// scans only the entries past the cached offset: a surfaced item's Seq is the
// entry count when it was surfaced, so entries already scanned cannot hold
// evidence for an item surfaced after them. When an item appears that the
// cached scan never saw but whose Seq is inside the scanned range, or the
// cache is unusable, it scans from zero.
func scanLiveUses(roots consultscan.Roots, state *session.RecordingState, surfaced []contextuse.SurfacedRef) (all, fresh []contexttrace.Event, err error) {
	rawPath := filepath.Join(state.SessionPath, "raw.jsonl")
	sessionKey := firstNonEmpty(state.SessionID, filepath.Base(state.SessionPath))
	cachePath := briefUsesCachePath(roots.Ledger, state.AgentID)

	c := loadBriefUsesCache(cachePath, sessionKey, rawPath, surfaced)
	kept := make([]contexttrace.Event, 0, len(c.Events))
	for _, ev := range c.Events {
		if _, ok := surfacedFor(surfaced, ev); ok {
			kept = append(kept, ev)
		}
	}

	entries, nextOffset, nextIndex, err := loadRawEntriesFrom(rawPath, c.Offset, c.NextIndex)
	if err != nil {
		return nil, nil, err
	}
	if len(entries) > 0 {
		// ScanUses numbers entries by slice index, the seq frame surfaced
		// items are in; blank slots for the scanned prefix keep that frame
		// and match nothing.
		window := make([]consultscan.Entry, c.NextIndex, c.NextIndex+len(entries))
		window = append(window, entries...)
		seen := map[string]bool{}
		for _, ev := range kept {
			seen[liveUseItemKey(ev)] = true
		}
		for _, ev := range consultscan.ScanUses(window, surfaced, roots) {
			if k := liveUseItemKey(ev); !seen[k] {
				seen[k] = true // keep the earliest, as one full scan would
				fresh = append(fresh, ev)
			}
		}
	}
	all = append(kept, fresh...)

	next := briefUsesCache{
		Session: sessionKey, Offset: nextOffset, NextIndex: nextIndex,
		SurfacedCount: len(surfaced), TailSHA: c.TailSHA, Events: all,
	}
	if next.Offset != c.Offset {
		if next.TailSHA, err = rawTailSHA(rawPath, next.Offset); err != nil {
			return nil, nil, err
		}
	}
	if cachePath != "" && (next.Offset != c.Offset || next.SurfacedCount != c.SurfacedCount || len(next.Events) != len(c.Events) || c.Session != sessionKey) {
		if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err != nil {
			slog.Debug("brief live uses: cache dir", "path", filepath.Dir(cachePath), "err", err)
		} else if err := fileutil.AtomicWriteJSON(cachePath, next, 0o644); err != nil {
			slog.Debug("brief live uses: cache write", "path", cachePath, "err", err)
		}
	}
	return all, fresh, nil
}

// loadBriefUsesCache returns the cache to resume from, or an empty one (scan
// from zero) when there is none, it is unreadable or corrupt, it belongs to
// another recording, raw.jsonl is shorter than its offset or its bytes before
// the offset changed (rewritten), the
// surfaced set shrank, or a newly surfaced item sits inside the range the
// cache already scanned.
func loadBriefUsesCache(cachePath, sessionKey, rawPath string, surfaced []contextuse.SurfacedRef) briefUsesCache {
	fresh := briefUsesCache{Session: sessionKey}
	if cachePath == "" {
		return fresh
	}
	data, err := os.ReadFile(cachePath)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Debug("brief live uses: cache read", "path", cachePath, "err", err)
		}
		return fresh
	}
	var c briefUsesCache
	if err := json.Unmarshal(data, &c); err != nil {
		slog.Debug("brief live uses: corrupt cache, rescanning", "path", cachePath, "err", err)
		return fresh
	}
	if c.Session != sessionKey || c.Offset < 0 || c.NextIndex < 0 || c.SurfacedCount > len(surfaced) {
		return fresh
	}
	if info, err := os.Stat(rawPath); err != nil || info.Size() < c.Offset {
		return fresh
	}
	if sum, err := rawTailSHA(rawPath, c.Offset); err != nil || sum != c.TailSHA {
		return fresh
	}
	for _, s := range surfaced[c.SurfacedCount:] {
		if s.Seq < c.NextIndex {
			return fresh
		}
	}
	return c
}

// briefUsesCachePath is <ledger>/.sageox/cache/brief-uses/<agentID>.json, or
// "" (no cache: every brief scans from zero) when the ledger is not on disk
// or the agent id is not a plain name. The cache never creates a ledger
// directory that is not already there.
func briefUsesCachePath(ledgerPath, agentID string) string {
	if ledgerPath == "" || agentID == "" || agentID != filepath.Base(agentID) || strings.HasPrefix(agentID, ".") {
		return ""
	}
	if info, err := os.Stat(ledgerPath); err != nil || !info.IsDir() {
		return ""
	}
	return filepath.Join(ledgerPath, ".sageox", "cache", briefUsesCacheSubdir, agentID+".json")
}

// surfacedFor returns the surfaced item a use event is evidence for: the
// item it names, surfaced at or before the use.
func surfacedFor(surfaced []contextuse.SurfacedRef, ev contexttrace.Event) (contextuse.SurfacedRef, bool) {
	for _, s := range surfaced {
		if s.Matches(ev.Ref) && ev.Seq >= s.Seq {
			return s, true
		}
	}
	return contextuse.SurfacedRef{}, false
}

// liveUseKey identifies one use event: type, item, evidence and turn — the
// key that finalize's idempotent append uses, over the canonical ref.
func liveUseKey(ev contexttrace.Event) string {
	return liveUseItemKey(ev) + "\x00" + strconv.Itoa(ev.Seq)
}

// liveUseItemKey identifies one (item, evidence) pair regardless of turn;
// ScanUses reports each pair once, at its earliest turn.
func liveUseItemKey(ev contexttrace.Event) string {
	return string(ev.Type) + "\x00" + strings.ToLower(contextuse.CanonicalRef(ev.RefType, ev.Ref)) + "\x00" + ev.Evidence
}
