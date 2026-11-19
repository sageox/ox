package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/internal/paths"
)

// Recording markers are found by walking session folders, and some of those
// folders are writable by anyone who can push to the repository or its Ledger:
// the Ledger checkout's cache and the legacy sessions/ folder. Everything in a
// marker, its session_path and agent_id included, is whatever its writer put
// there. The daemon's hook catch-up reads the transcript a marker names and
// appends it to raw.jsonl, so it acts only on recordings ox started on this
// machine.
//
// StartRecording records each recording it starts in a registry outside the
// Ledger and the repository, in the ox data directory beside livesession's
// live-conversations.json and written the same way (file lock, atomic write).
// A marker counts as started here only when it sits in the folder recorded
// for it, names that folder, and carries the recorded session and agent IDs
// (LoadRecordingMarkers). Recordings started by an older ox are not in it.
//
// The entry also binds the recording's transcript (startedRecording.Source).
// A marker's session_file is as forgeable as the rest of it, and a Claude Code
// transcript under the home directory passes every source check, so the
// catch-up reads only the transcript a local hook found for the recording.

const startedRecordingsFileName = "started-recordings.json"

// startedRecordingsLockWait bounds waiting on the registry lock. Every holder
// does one small read-modify-write, as with livesession's registry.
const startedRecordingsLockWait = 5 * time.Second

// startedRecording is one registry entry.
type startedRecording struct {
	// Dir is the session folder, resolved (canonicalPath).
	Dir       string    `json:"dir"`
	SessionID string    `json:"session_id"`
	AgentID   string    `json:"agent_id"`
	StartedAt time.Time `json:"started_at"`
	// Source is the transcript a local hook found for the recording, resolved
	// (canonicalPath); empty until its first hook discovers one. Only a hook
	// sets it (bindStartedRecordingSource), never the daemon.
	Source string `json:"source,omitempty"`
}

type startedKey struct{ dir, sessionID, agentID string }

func startedRecordingsPath() string {
	return filepath.Join(paths.DataDir(), startedRecordingsFileName)
}

// canonicalPath is the one spelling a session folder or transcript has in
// the registry, however a caller spelled it (macOS /var is /private/var). A
// path already gone falls back to Clean.
func canonicalPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

// loadStartedRecordings reads the registry without its lock: every write
// replaces the file atomically, so a read sees one whole version.
func loadStartedRecordings() (map[startedKey]startedRecording, error) {
	list, err := readStartedRecordings()
	if err != nil {
		return nil, err
	}
	known := make(map[startedKey]startedRecording, len(list))
	for _, e := range list {
		known[startedKey{e.Dir, e.SessionID, e.AgentID}] = e
	}
	return known, nil
}

func readStartedRecordings() ([]startedRecording, error) {
	raw, err := os.ReadFile(startedRecordingsPath())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read started recordings: %w", err)
	}
	var list []startedRecording
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("parse started recordings: %w", err)
	}
	return list, nil
}

// updateStartedRecordings runs fn on the registry under its lock and writes
// the result back when fn reports a change.
func updateStartedRecordings(fn func([]startedRecording) ([]startedRecording, bool)) error {
	path := startedRecordingsPath()
	return fileutil.WithFileLockTimeout(context.Background(), path, startedRecordingsLockWait, func() error {
		list, err := readStartedRecordings()
		if err != nil {
			return err
		}
		list, changed := fn(list)
		if !changed {
			return nil
		}
		if err := fileutil.EnsurePrivateDir(filepath.Dir(path)); err != nil {
			return fmt.Errorf("create data dir: %w", err)
		}
		if err := fileutil.AtomicWriteJSON(path, list, 0o600); err != nil {
			return fmt.Errorf("write started recordings: %w", err)
		}
		return nil
	})
}

// registerStartedRecording records state, whose session folder must exist, as
// started here. It also drops entries whose folder is gone: ghost cleanup and
// orphan sweeps remove folders without clearing their recordings.
func registerStartedRecording(state *RecordingState) error {
	entry := startedRecording{
		Dir: canonicalPath(state.SessionPath), SessionID: state.SessionID, AgentID: state.AgentID, StartedAt: state.StartedAt,
	}
	return updateStartedRecordings(func(list []startedRecording) ([]startedRecording, bool) {
		list = slices.DeleteFunc(list, func(e startedRecording) bool {
			if e.Dir == entry.Dir && e.SessionID == entry.SessionID {
				return true
			}
			_, err := os.Lstat(e.Dir)
			return errors.Is(err, fs.ErrNotExist)
		})
		return append(list, entry), true
	})
}

// bindStartedRecordingSource records source as the transcript of the
// recording sessionID in sessionPath. Only a hook calls it, for a transcript
// it discovered (CaptureHookEntries, HookCaptureOptions.BindSource). A first
// discovery binds once: the marker's session_file is not cleared by ox, so a
// second one means someone emptied it. replace is a hook's rediscovery of a
// transcript that went away or shrank, which legitimately moves the binding.
// A recording not in the registry is left out: it is not caught up anyway.
func bindStartedRecordingSource(sessionPath, sessionID, source string, replace bool) error {
	if sessionPath == "" || sessionID == "" || source == "" {
		return nil
	}
	dir, resolved := canonicalPath(sessionPath), canonicalPath(source)
	return updateStartedRecordings(func(list []startedRecording) ([]startedRecording, bool) {
		for i := range list {
			e := &list[i]
			if e.Dir != dir || e.SessionID != sessionID {
				continue
			}
			if e.Source == resolved || (e.Source != "" && !replace) {
				return list, false
			}
			e.Source = resolved
			return list, true
		}
		return list, false
	})
}

// forgetStartedRecording removes the recording sessionID in sessionPath from
// the registry, once it is finalized or cleared.
func forgetStartedRecording(sessionPath, sessionID string) error {
	if sessionPath == "" || sessionID == "" {
		return nil
	}
	dir := canonicalPath(sessionPath)
	return updateStartedRecordings(func(list []startedRecording) ([]startedRecording, bool) {
		n := len(list)
		list = slices.DeleteFunc(list, func(e startedRecording) bool { return e.Dir == dir && e.SessionID == sessionID })
		return list, len(list) != n
	})
}

// startedHere returns the registry entry of st, read from the marker in
// folder dir, when it is a recording local ox started in that folder.
func startedHere(known map[startedKey]startedRecording, dir string, st *RecordingState) (startedRecording, bool) {
	if st.SessionID == "" || st.SessionPath == "" {
		return startedRecording{}, false
	}
	resolved := canonicalPath(dir)
	// a forged marker can name a real recording's folder and IDs; only the
	// folder it is actually in says where it came from
	if canonicalPath(st.SessionPath) != resolved {
		return startedRecording{}, false
	}
	e, ok := known[startedKey{resolved, st.SessionID, st.AgentID}]
	return e, ok
}

// RecordingMarker is one recording marker under a project's session folders.
type RecordingMarker struct {
	// Dir is the folder the marker was found in. A marker's SessionPath is only
	// what its content claims.
	Dir   string
	State *RecordingState
	// Started is true when ox on this machine started this recording in Dir.
	Started bool
	// Source is the transcript a local hook found for a Started recording,
	// resolved; empty when none has been found yet. The marker's own
	// SessionFile is only what its content claims.
	Source string
}

// LoadRecordingMarkers returns the markers LoadAllRecordingStates would, each
// with the folder it was found in and whether ox on this machine started it
// there. An unreadable registry is an error: nothing can be shown to be
// started here.
func LoadRecordingMarkers(projectRoot string) ([]RecordingMarker, error) {
	known, err := loadStartedRecordings()
	if err != nil {
		return nil, err
	}
	var markers []RecordingMarker
	if err := walkRecordingMarkers(projectRoot, false, func(dir string, st *RecordingState) bool {
		e, started := startedHere(known, dir, st)
		markers = append(markers, RecordingMarker{Dir: dir, State: st, Started: started, Source: e.Source})
		return true
	}); err != nil {
		return nil, err
	}
	return markers, nil
}
