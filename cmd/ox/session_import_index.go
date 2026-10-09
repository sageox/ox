package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/paths"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/adapters"
	"github.com/sageox/ox/internal/session/nativeimport"
)

// importQuietPeriod is how long a native file must go untouched before it is
// imported: a session written to in the last half hour may still be running.
const importQuietPeriod = 30 * time.Minute

// lateRecordingStart: ox starts recording within seconds of a session's start,
// so a recording that first saw a native session later than this began on a
// resume or a manual start, and holds only what followed.
const lateRecordingStart = 10 * time.Minute

// importStaleTurn is how long a Codex turn may stay open with no write before
// it counts as interrupted: a Codex killed mid-turn never records the turn's
// end, and its session must not stay "in progress" forever.
const importStaleTurn = 12 * time.Hour

// nativeKey identifies a native session independent of any Ledger name.
type nativeKey struct {
	agent string // canonical adapter name: claude-code or codex
	id    string
}

func keyFor(agent nativeimport.Agent, nativeID string) nativeKey {
	return nativeKey{agent: canonicalImportAgent(string(agent)), id: nativeID}
}

// canonicalImportAgent folds the agent spellings recordings have used
// ("claude", "claude-code", "Claude Code") to one form.
func canonicalImportAgent(agent string) string { return adapters.CanonicalAdapterName(agent) }

// adapterNameFor is the adapter an imported session is read and recorded with.
func adapterNameFor(agent nativeimport.Agent) string { return canonicalImportAgent(string(agent)) }

type liveRecording struct {
	name  string
	start time.Time
}

// importIndex is what the Ledger and this machine already hold. It answers
// "is this native session already in the Ledger, or covered by an ox
// recording?" without reading any transcript.
type importIndex struct {
	names       map[string]bool            // sessions/<name> directories in HEAD
	imported    map[nativeKey]string       // native session -> its import's name
	importedTo  map[nativeKey]time.Time    // native session -> last activity the import holds
	recorded    map[nativeKey]string       // native session -> the ox recording that covers it
	recordedAt  map[nativeKey]time.Time    // native session -> when a recording first saw it
	sessionIDs  map[string]string          // ses_ ID, stored or derived -> name
	byAgentID   map[string][]liveRecording // ox agent instance -> recordings named for it
	active      map[nativeKey]string       // native session -> a recording still in progress
	activeFiles map[string]string          // native file -> a recording still in progress
}

func newImportIndex() *importIndex {
	return &importIndex{
		names:       map[string]bool{},
		imported:    map[nativeKey]string{},
		importedTo:  map[nativeKey]time.Time{},
		recorded:    map[nativeKey]string{},
		recordedAt:  map[nativeKey]time.Time{},
		sessionIDs:  map[string]string{},
		byAgentID:   map[string][]liveRecording{},
		active:      map[nativeKey]string{},
		activeFiles: map[string]string{},
	}
}

// buildImportIndex reads the Ledger's HEAD tree and metas through git objects,
// never the worktree, so a sparse or dehydrated checkout reads the same, then
// adds this machine's recordings and captures not yet uploaded.
func buildImportIndex(ctx context.Context, projectRoot, ledgerPath, repoID string) (*importIndex, error) {
	idx := newImportIndex()
	names, err := ledgerSessionNames(ctx, ledgerPath)
	if err != nil {
		return nil, err
	}
	for _, name := range names {
		idx.names[name] = true
	}
	metas, err := readLedgerMetas(ctx, ledgerPath, names)
	if err != nil {
		return nil, err
	}
	unreadable := 0
	for _, name := range names {
		meta, ok := metas[name]
		if !ok {
			continue
		}
		if meta == nil {
			unreadable++
			continue
		}
		idx.addMeta(name, meta, repoID)
	}
	if unreadable > 0 {
		// Their sessions cannot be recognized; ox doctor repairs such metas.
		slog.Warn("session import: unreadable Ledger metas", "count", unreadable)
	}
	idx.addLocalCaptures(projectRoot, ledgerPath, repoID)
	return idx, nil
}

func (idx *importIndex) addMeta(name string, meta *lfs.SessionMeta, repoID string) {
	if meta.RepoID == "" {
		meta.RepoID = repoID
	}
	idx.sessionIDs[meta.EffectiveSessionID()] = name
	agent := canonicalImportAgent(meta.AgentType)
	for _, ns := range meta.NativeSessions {
		if ns.ID == "" {
			continue
		}
		key := nativeKey{agent: agent, id: ns.ID}
		// A draft is a live recording's placeholder (ADR-029): the native
		// sessions it names are covered by that recording, never an import.
		if ns.Source == nativeimport.NativeSourceImport && !meta.IsDraft() {
			idx.imported[key] = name
			idx.importedTo[key] = ns.LastSeen
		} else {
			idx.addRecorded(key, name, ns.FirstSeen)
		}
	}
	idx.addAgentName(name, meta.AgentID, meta.CreatedAt)
}

// addAgentName indexes a live recording by its ox agent instance, the last
// segment of every live name, for markers that carry no link. start is the
// recording's own start time; the name's timestamp is only a fallback, since
// ox named recordings in local time before 2026-03-31.
func (idx *importIndex) addAgentName(name, agentID string, start time.Time) {
	if agentID == "" {
		if i := strings.LastIndex(name, "-"); i >= 0 {
			agentID = name[i+1:]
		}
	}
	if !strings.HasPrefix(agentID, "Ox") || len(agentID) != 6 || len(name) < 16 {
		return
	}
	if start.IsZero() {
		parsed, err := time.Parse("2006-01-02T15-04", name[:16])
		if err != nil {
			return
		}
		start = parsed
	}
	idx.byAgentID[agentID] = append(idx.byAgentID[agentID], liveRecording{name: name, start: start.UTC()})
}

// addRecorded notes a recording that covers a native session, keeping the
// earliest time any recording first saw it.
func (idx *importIndex) addRecorded(key nativeKey, name string, firstSeen time.Time) {
	idx.recorded[key] = name
	if firstSeen.IsZero() {
		return
	}
	if at, ok := idx.recordedAt[key]; !ok || firstSeen.Before(at) {
		idx.recordedAt[key] = firstSeen
	}
}

// continuedSinceImport reports a native session that kept going after it was
// imported: the Ledger holds only the part before the import.
func (idx *importIndex) continuedSinceImport(s nativeimport.Session) bool {
	held, ok := idx.importedTo[keyFor(s.Agent, s.NativeID)]
	return ok && !held.IsZero() && s.LastActivity.After(held.Add(time.Minute))
}

// coverageNote says what the Ledger lacks of a session it already holds in
// part. Such a session is never uploaded again; the note keeps the preview
// honest about the missing part.
func (idx *importIndex) coverageNote(s nativeimport.Session, state importState) string {
	key := keyFor(s.Agent, s.NativeID)
	switch state {
	case stateAlreadyImported:
		if !idx.continuedSinceImport(s) {
			return ""
		}
		// A recording naming the session shows when ox saw it again, not
		// that it holds everything since the import: the session may have
		// continued unrecorded first.
		if rec := idx.recorded[key]; rec != "" {
			if at, ok := idx.recordedAt[key]; ok {
				return "continued after it was imported; ox recorded it from " + at.Local().Format("2006-01-02 15:04 MST") + " as " + rec
			}
			return "continued after it was imported; ox also recorded it as " + rec
		}
		return "continued after it was imported; the later part is not in the Ledger"
	case stateRecordedLive:
		if at, ok := idx.recordedAt[key]; ok && at.After(s.StartedAt.Add(lateRecordingStart)) {
			return "ox recorded it only from " + at.Local().Format("2006-01-02 15:04 MST") +
				"; the part before that is not in the Ledger"
		}
	}
	return ""
}

// addLocalCaptures covers recordings this machine has not uploaded yet: active
// recordings (in progress) and captures still waiting in the session caches.
func (idx *importIndex) addLocalCaptures(projectRoot, ledgerPath, repoID string) {
	if states, err := session.LoadAllRecordingStates(projectRoot); err == nil {
		for _, st := range states {
			name := filepath.Base(st.SessionPath)
			agent := canonicalImportAgent(st.AdapterName)
			seen := []lfs.NativeSession{{ID: st.AgentSessionID, FirstSeen: st.StartedAt}}
			seen = append(seen, st.NativeSessions...)
			for _, ns := range seen {
				if ns.ID == "" {
					continue
				}
				key := nativeKey{agent: agent, id: ns.ID}
				if st.StoppedAt == nil {
					idx.active[key] = name
				} else {
					idx.addRecorded(key, name, ns.FirstSeen)
				}
			}
			if st.SessionFile != "" && st.StoppedAt == nil {
				idx.activeFiles[filepath.Clean(st.SessionFile)] = name
			}
			if st.SessionID != "" {
				idx.sessionIDs[st.SessionID] = name
			}
			idx.addAgentName(name, st.AgentID, st.StartedAt)
		}
	}
	dirs := []string{filepath.Join(ledgerPath, ".sageox", "cache", "sessions")}
	if repoID != "" {
		dirs = append(dirs, filepath.Join(paths.SessionCacheDir(repoID), "sessions"))
		for _, d := range paths.AlternateSessionCacheDirs(repoID) {
			dirs = append(dirs, filepath.Join(d, "sessions"))
		}
	}
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				idx.addCapture(e.Name(), filepath.Join(dir, e.Name(), "raw.jsonl"))
			}
		}
	}
}

// addCapture reads a pending capture's header and footers, the crash-safe
// carriers of its ses_ ID and native sessions, without reading its entries.
func (idx *importIndex) addCapture(name, rawPath string) {
	if lfs.IsPointerFile(rawPath) {
		return
	}
	first, footers, err := headerAndFooters(rawPath)
	if err != nil {
		return
	}
	agent := ""
	var started time.Time
	var natives []lfs.NativeSession
	var header struct {
		Metadata *session.StoreMeta `json:"metadata"`
	}
	if json.Unmarshal(first, &header) == nil && header.Metadata != nil {
		agent = canonicalImportAgent(header.Metadata.AgentType)
		started = header.Metadata.CreatedAt
		natives = append(natives, header.Metadata.NativeSessions...)
		if header.Metadata.SessionID != "" {
			idx.sessionIDs[header.Metadata.SessionID] = name
		}
	}
	idx.addAgentName(name, "", started)
	// Footers carry native sessions recorded after the header was written.
	// They merge field by field, so one without native_sessions hides none;
	// they name no agent, so the header's applies.
	for _, line := range footers {
		var footer struct {
			NativeSessions []lfs.NativeSession `json:"native_sessions"`
		}
		if json.Unmarshal(line, &footer) == nil {
			natives = append(natives, footer.NativeSessions...)
		}
	}
	for _, ns := range natives {
		if ns.ID != "" {
			idx.addRecorded(nativeKey{agent: agent, id: ns.ID}, name, ns.FirstSeen)
		}
	}
}

// headerAndFooters returns a capture's first line and the footer records that
// end it, reading only the head and the tail.
func headerAndFooters(path string) ([]byte, [][]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("not a regular file")
	}
	first, err := bufio.NewReaderSize(io.LimitReader(f, 4<<20), 64*1024).ReadBytes('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, nil, err
	}
	const tailSize = 256 * 1024
	offset := max(info.Size()-tailSize, 0)
	tail := make([]byte, info.Size()-offset)
	if _, err := f.ReadAt(tail, offset); err != nil && !errors.Is(err, io.EOF) {
		return nil, nil, err
	}
	lines := bytes.Split(bytes.TrimRight(tail, "\n"), []byte("\n"))
	var footers [][]byte
	for i := len(lines) - 1; i >= 0; i-- {
		line := bytes.TrimSpace(lines[i])
		var record struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(line, &record) != nil || record.Type != "footer" {
			break
		}
		footers = append(footers, line)
	}
	return bytes.TrimSpace(first), footers, nil
}

// ledgerSessionNames lists the session directories in the Ledger's HEAD.
func ledgerSessionNames(ctx context.Context, ledgerPath string) ([]string, error) {
	if !ledgerHasHead(ctx, ledgerPath) {
		return nil, nil
	}
	out, err := exec.CommandContext(ctx, "git", "-C", ledgerPath, "ls-tree", "-z", "-d", "--name-only", "HEAD", "sessions/").Output()
	if err != nil {
		return nil, fmt.Errorf("list Ledger sessions: %w", err)
	}
	var names []string
	for _, p := range strings.Split(string(out), "\x00") {
		if name := strings.TrimPrefix(p, "sessions/"); name != "" && name != p {
			names = append(names, name)
		}
	}
	return names, nil
}

func ledgerHasHead(ctx context.Context, ledgerPath string) bool {
	return exec.CommandContext(ctx, "git", "-C", ledgerPath, "rev-parse", "--verify", "--quiet", "HEAD").Run() == nil
}

// readLedgerMetas reads HEAD:sessions/<name>/meta.json for every name in one
// git cat-file process. A missing meta is absent from the result; an
// unreadable one maps to nil.
func readLedgerMetas(ctx context.Context, ledgerPath string, names []string) (map[string]*lfs.SessionMeta, error) {
	out := map[string]*lfs.SessionMeta{}
	if len(names) == 0 {
		return out, nil
	}
	var input bytes.Buffer
	for _, name := range names {
		fmt.Fprintf(&input, "HEAD:sessions/%s/meta.json\n", name)
	}
	cmd := exec.CommandContext(ctx, "git", "-C", ledgerPath, "cat-file", "--batch")
	cmd.Stdin = &input
	stdout, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("read Ledger metas: %w", err)
	}
	rd := bufio.NewReader(bytes.NewReader(stdout))
	for _, name := range names {
		header, err := rd.ReadString('\n')
		if err != nil {
			return nil, fmt.Errorf("read Ledger metas: %w", err)
		}
		fields := strings.Fields(header)
		if len(fields) == 2 && fields[1] == "missing" {
			continue
		}
		if len(fields) != 3 {
			return nil, fmt.Errorf("read Ledger metas: unexpected %q", strings.TrimSpace(header))
		}
		var size int
		if _, err := fmt.Sscan(fields[2], &size); err != nil {
			return nil, fmt.Errorf("read Ledger metas: %w", err)
		}
		blob := make([]byte, size+1) // content plus the trailing newline
		if _, err := io.ReadFull(rd, blob); err != nil {
			return nil, fmt.Errorf("read Ledger metas: %w", err)
		}
		var meta lfs.SessionMeta
		if json.Unmarshal(blob[:size], &meta) != nil {
			out[name] = nil
			continue
		}
		out[name] = &meta
	}
	return out, nil
}

// importState is a candidate's classification, in the order the checks run.
type importState string

const (
	stateReady           importState = "ready"
	stateIneligible      importState = "ineligible"
	stateInProgress      importState = "in_progress"
	stateAlreadyImported importState = "already_imported"
	stateRecordedLive    importState = "recorded_live"
	stateNeedsSummarizer importState = "needs_summarizer"
	stateNotShared       importState = "not_shared"
)

// classify applies checks 4 to 6 to an in-scope session with a conversation.
// It returns the state and, for recorded_live and already_imported, the Ledger
// session that covers it.
func (idx *importIndex) classify(s nativeimport.Session, now time.Time) (importState, string) {
	key := keyFor(s.Agent, s.NativeID)
	switch {
	case s.InFlight && now.Sub(s.ModTime) < importStaleTurn:
		return stateInProgress, "a Codex turn is still in flight"
	case now.Sub(s.ModTime) < importQuietPeriod:
		return stateInProgress, "changed in the last 30 minutes"
	case idx.active[key] != "":
		return stateInProgress, "ox is recording it: " + idx.active[key]
	case idx.activeFiles[filepath.Clean(s.Path)] != "":
		return stateInProgress, "ox is recording it: " + idx.activeFiles[filepath.Clean(s.Path)]
	}
	name := nativeimport.Name(s.Agent, s.NativeID, s.StartedAt)
	if idx.names[name] {
		return stateAlreadyImported, name
	}
	if existing := idx.imported[key]; existing != "" {
		return stateAlreadyImported, existing
	}
	if existing := idx.recorded[key]; existing != "" {
		return stateRecordedLive, existing
	}
	if existing := idx.markerMatch(s); existing != "" {
		return stateRecordedLive, existing
	}
	return stateReady, ""
}

// markerMatch recognizes a recording made by an ox that did not store native
// IDs, through the marker ox wrote into the transcript: its ses_ link, its
// name link, or its agent instance with a recording that starts inside the
// session.
func (idx *importIndex) markerMatch(s nativeimport.Session) string {
	for _, m := range s.Markers {
		if id := m.SessionID(); id != "" && idx.sessionIDs[id] != "" {
			return idx.sessionIDs[id]
		}
		if name := m.SessionName(); name != "" && idx.names[name] {
			return name
		}
	}
	for _, m := range s.Markers {
		for _, rec := range idx.byAgentID[m.AgentID] {
			if !rec.start.Before(s.StartedAt.Add(-time.Minute)) && !rec.start.After(s.LastActivity) {
				return rec.name
			}
		}
	}
	return ""
}

// importVerdicts remembers, on this machine only, the sessions the summarizer
// judged not worth sharing or local-only, so a rerun neither summarizes them
// again nor lists them as ready. A session that changed since is judged again.
type importVerdicts struct {
	mu       sync.Mutex
	path     string
	Sessions map[string]importVerdict `json:"sessions"`
}

type importVerdict struct {
	Size    int64     `json:"size"` // the native file's size when judged
	Verdict string    `json:"verdict"`
	Reason  string    `json:"reason,omitempty"`
	At      time.Time `json:"at"`
}

// loadImportVerdicts reads the verdicts kept in the Ledger's gitignored cache.
// A missing or unreadable file reads as empty, which costs one more summary.
func loadImportVerdicts(ledgerPath string) *importVerdicts {
	v := &importVerdicts{path: filepath.Join(ledgerPath, ".sageox", "cache", "session-import", "verdicts.json")}
	if data, err := os.ReadFile(v.path); err == nil {
		_ = json.Unmarshal(data, v)
	}
	if v.Sessions == nil {
		v.Sessions = map[string]importVerdict{}
	}
	return v
}

func verdictKey(s nativeimport.Session) string {
	key := keyFor(s.Agent, s.NativeID)
	return key.agent + "/" + key.id
}

// lookup reads a verdict under the shared worker lock and rejects stale native files.
func (v *importVerdicts) lookup(s nativeimport.Session) (importVerdict, bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	got, ok := v.Sessions[verdictKey(s)]
	return got, ok && got.Size == s.Size
}

// record serializes the in-memory update and atomic cache write so concurrent
// preparation cannot overwrite another session's verdict.
func (v *importVerdicts) record(s nativeimport.Session, verdict importVerdict) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.Sessions[verdictKey(s)] = verdict
	if err := os.MkdirAll(filepath.Dir(v.path), 0o700); err != nil {
		return err
	}
	return fileutil.AtomicWriteJSON(v.path, v, 0o600)
}
