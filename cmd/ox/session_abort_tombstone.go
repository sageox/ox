package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/paths"
	"github.com/sageox/ox/internal/session"
)

// abortedSessions remembers, on this machine only, the native Claude Code and
// Codex sessions whose ox recording the coworker aborted. Abort deletes
// everything keyed by the recording, but the native transcript stays where
// the agent wrote it, so without this record `ox session import` would offer
// the discarded session as ready once it went quiet, and prime would start
// recording it again when the conversation resumed.
//
// It lives in the Ledger's gitignored cache beside the import's verdicts:
// shared by every worktree of the repo, carried across GC reclones, never
// pushed. An abort made before the Ledger is cloned goes to the repo's session
// cache instead, and readers merge both. Nothing clears an entry; only an
// explicit `ox session import --include-aborted` uploads one.
type abortedSessions struct {
	Sessions map[string]abortedSession `json:"sessions"` // by native session ID
	// Recordings holds the aborted recordings' ses_ IDs. Prime writes each
	// recording's link into the native transcript, so this still recognizes a
	// session whose native ID the recording never stored.
	Recordings map[string]abortedSession `json:"recordings,omitempty"`
}

type abortedSession struct {
	Agent     string    `json:"agent,omitempty"`
	Recording string    `json:"recording,omitempty"` // the ox session that was aborted
	AbortedAt time.Time `json:"aborted_at"`
}

// abortedNative is a native session an aborted recording covered.
type abortedNative struct {
	agent string
	id    string
}

func abortedSessionsPath(ledgerPath string) string {
	return filepath.Join(ledgerPath, ".sageox", "cache", "session-import", "aborted.json")
}

// pendingAbortRecordPath holds aborts made while the repo had no Ledger
// clone. It sits beside the recordings themselves, outside sessions/.
func pendingAbortRecordPath(repoID string) string {
	return filepath.Join(paths.SessionCacheDir(repoID), "aborted.json")
}

// abortRecordPaths lists every place an abort record can be, for readers.
func abortRecordPaths(ledgerPath, repoID string) []string {
	var out []string
	if ledgerPath != "" {
		out = append(out, abortedSessionsPath(ledgerPath))
	}
	if repoID != "" {
		out = append(out, pendingAbortRecordPath(repoID))
	}
	return out
}

// errAbortRecordUnreadable marks a record that exists but cannot be read.
var errAbortRecordUnreadable = errors.New("the record of sessions you aborted cannot be read")

// loadAbortRecords merges every record of the repo. A missing file is an
// empty record; an unreadable one is an error, never an empty record, because
// reading it as empty would offer every aborted session again.
func loadAbortRecords(ledgerPath, repoID string) (*abortedSessions, error) {
	merged := newAbortedSessions()
	for _, path := range abortRecordPaths(ledgerPath, repoID) {
		record, err := loadAbortRecordFile(path)
		if err != nil {
			return nil, err
		}
		for id, a := range record.Sessions {
			if _, ok := merged.Sessions[id]; !ok {
				merged.Sessions[id] = a
			}
		}
		for id, a := range record.Recordings {
			if _, ok := merged.Recordings[id]; !ok {
				merged.Recordings[id] = a
			}
		}
	}
	return merged, nil
}

func newAbortedSessions() *abortedSessions {
	return &abortedSessions{Sessions: map[string]abortedSession{}, Recordings: map[string]abortedSession{}}
}

func loadAbortRecordFile(path string) (*abortedSessions, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return newAbortedSessions(), nil
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", errAbortRecordUnreadable, path, err)
	}
	// Decode through a pointer: a record of `null` would otherwise leave the
	// empty record in place and read as "nothing aborted".
	var record *abortedSessions
	if err := json.Unmarshal(data, &record); err != nil {
		return nil, fmt.Errorf("%w: %s: %w", errAbortRecordUnreadable, path, err)
	}
	if record == nil {
		return nil, fmt.Errorf("%w: %s: the record is null", errAbortRecordUnreadable, path)
	}
	if record.Sessions == nil {
		record.Sessions = map[string]abortedSession{}
	}
	if record.Recordings == nil {
		record.Recordings = map[string]abortedSession{}
	}
	return record, nil
}

// abortRecordKeys are the entries one abort added, so a failed abort can take
// back exactly those and leave earlier aborts alone.
type abortRecordKeys struct {
	path       string
	natives    []string
	recordings []string
}

// updateAbortRecord rewrites one record under a file lock, so two AI
// coworkers aborting at once cannot drop each other's entry. A record it
// cannot read is left alone rather than replaced.
func updateAbortRecord(path string, change func(*abortedSessions)) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return fileutil.WithFileLock(context.Background(), path, func() error {
		record, err := loadAbortRecordFile(path)
		if err != nil {
			return err
		}
		change(record)
		return fileutil.AtomicWriteJSON(path, record, 0o600)
	})
}

// recordAbortedSessions adds an aborted recording to the record at path and
// returns the entries it added.
func recordAbortedSessions(path, recording, sessionID string, natives []abortedNative, at time.Time) (abortRecordKeys, error) {
	added := abortRecordKeys{path: path}
	if len(natives) == 0 && sessionID == "" {
		return added, nil
	}
	err := updateAbortRecord(path, func(record *abortedSessions) {
		for _, n := range natives {
			if _, ok := record.Sessions[n.id]; !ok {
				record.Sessions[n.id] = abortedSession{Agent: n.agent, Recording: recording, AbortedAt: at.UTC()}
				added.natives = append(added.natives, n.id)
			}
		}
		if _, ok := record.Recordings[sessionID]; sessionID != "" && !ok {
			record.Recordings[sessionID] = abortedSession{Recording: recording, AbortedAt: at.UTC()}
			added.recordings = append(added.recordings, sessionID)
		}
	})
	return added, err
}

// rememberAbort records, before abort deletes anything, the recording's ses_
// ID and the native sessions it covered: in the Ledger's cache, or the repo's
// session cache when the Ledger is missing or its record cannot be written.
// Writing first means a crash mid-abort still leaves the session discarded;
// the returned undo takes the entries back when the abort fails while the
// recording is still armed. Like the draft removal it never stops the abort:
// a failure to record comes back as the warning the coworker needs.
func rememberAbort(projectRoot, recording, sessionID string, natives []abortedNative) (string, func()) {
	noUndo := func() {}
	if len(natives) == 0 && sessionID == "" {
		return "", noUndo
	}
	var targets []string
	if ledgerPath, err := resolveLedgerPath(); err == nil {
		targets = append(targets, abortedSessionsPath(ledgerPath))
	}
	targets = append(targets, pendingAbortRecordPath(getRepoIDOrDefault(projectRoot)))
	var errs []error
	for _, path := range targets {
		added, err := recordAbortedSessions(path, recording, sessionID, natives, time.Now())
		if err == nil {
			return "", func() { undoAbortRecord(added) }
		}
		errs = append(errs, err)
	}
	err := errors.Join(errs...)
	slog.Warn("could not record aborted native sessions", "session", recording, "error", err)
	return fmt.Sprintf("could not record the abort for 'ox session import', which may offer this session again: %v", err), noUndo
}

// undoAbortRecord removes what one abort added.
func undoAbortRecord(added abortRecordKeys) {
	if len(added.natives) == 0 && len(added.recordings) == 0 {
		return
	}
	if err := updateAbortRecord(added.path, func(record *abortedSessions) {
		for _, id := range added.natives {
			delete(record.Sessions, id)
		}
		for _, id := range added.recordings {
			delete(record.Recordings, id)
		}
	}); err != nil {
		slog.Warn("could not take back the abort record of a failed abort", "path", added.path, "error", err)
	}
}

// abortedNativeSession reports whether the coworker aborted a recording of
// this native session, reading every record of the project's repo.
func abortedNativeSession(projectRoot, nativeID string) (abortedSession, bool, error) {
	if nativeID == "" {
		return abortedSession{}, false, nil
	}
	record, err := loadAbortRecords(getLedgerPath(), getRepoIDOrDefault(projectRoot))
	if err != nil {
		return abortedSession{}, false, err
	}
	a, ok := record.Sessions[nativeID]
	return a, ok, nil
}

// nativeSessionsOfRecording lists the native sessions a local recording
// covered: those its recording state names (state, or the .recording.json in
// sessionPath when state is nil) and those its raw.jsonl header and footers
// carry, which outlive a lost recording state.
func nativeSessionsOfRecording(sessionPath string, state *session.RecordingState) []abortedNative {
	var out []abortedNative
	add := func(agent, id string) {
		if id != "" {
			out = append(out, abortedNative{agent: canonicalImportAgent(agent), id: id})
		}
	}
	if state == nil {
		if data, err := os.ReadFile(filepath.Join(sessionPath, ".recording.json")); err == nil {
			var st session.RecordingState
			if json.Unmarshal(data, &st) == nil {
				state = &st
			}
		}
	}
	if state != nil {
		add(state.AdapterName, state.AgentSessionID)
		for _, ns := range state.NativeSessions {
			add(state.AdapterName, ns.ID)
		}
	}
	if capture, ok := readCaptureIdentity(filepath.Join(sessionPath, ledgerFileRaw)); ok {
		for _, ns := range capture.natives {
			add(capture.agent, ns.ID)
		}
	}
	return out
}

// localIdentityOfSession reads what local copies of a session say about it,
// for when its Ledger meta.json cannot be read: the ses_ ID from a capture
// header and the native sessions each copy names. Empty dirs are skipped.
func localIdentityOfSession(dirs ...string) (string, []abortedNative) {
	sessionID := ""
	var natives []abortedNative
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		natives = append(natives, nativeSessionsOfRecording(dir, nil)...)
		if capture, ok := readCaptureIdentity(filepath.Join(dir, ledgerFileRaw)); ok && sessionID == "" {
			sessionID = capture.sessionID
		}
	}
	return sessionID, natives
}

// nativeSessionsOfMeta lists the native sessions a Ledger session covered.
func nativeSessionsOfMeta(meta *lfs.SessionMeta) []abortedNative {
	if meta == nil {
		return nil
	}
	var out []abortedNative
	for _, ns := range meta.NativeSessions {
		if ns.ID != "" {
			out = append(out, abortedNative{agent: canonicalImportAgent(meta.AgentType), id: ns.ID})
		}
	}
	return out
}

func joinWarnings(warnings ...string) string {
	var kept []string
	for _, w := range warnings {
		if w != "" {
			kept = append(kept, w)
		}
	}
	return strings.Join(kept, "; ")
}
