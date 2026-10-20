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
	"github.com/sageox/ox/internal/session"
)

// abortedSessions remembers, on this machine only, the native Claude Code and
// Codex sessions whose ox recording the coworker aborted. Abort deletes
// everything keyed by the recording, but the native transcript stays where
// the agent wrote it, so without this record `ox session import` would offer
// the discarded session as ready once it went quiet.
//
// It lives in the Ledger's gitignored cache beside the import's verdicts:
// shared by every worktree of the repo, carried across GC reclones, never
// pushed. Nothing clears an entry: importing the same native session later
// would publish the part the coworker discarded.
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

// loadAbortedSessions reads the record. A missing file is an empty record; an
// unreadable one is an error, never an empty record, because reading it as
// empty would offer every aborted session again.
func loadAbortedSessions(ledgerPath string) (*abortedSessions, error) {
	path := abortedSessionsPath(ledgerPath)
	record := &abortedSessions{}
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("read the sessions you aborted: %w", err)
	}
	if err == nil {
		if err := json.Unmarshal(data, record); err != nil {
			return nil, fmt.Errorf("read the sessions you aborted at %s: %w", path, err)
		}
	}
	if record.Sessions == nil {
		record.Sessions = map[string]abortedSession{}
	}
	if record.Recordings == nil {
		record.Recordings = map[string]abortedSession{}
	}
	return record, nil
}

// recordAbortedSessions adds an aborted recording to the record under a file
// lock, so two AI coworkers aborting at once cannot drop each other's entry.
// A record it cannot read is left alone rather than replaced.
func recordAbortedSessions(ledgerPath, recording, sessionID string, natives []abortedNative, at time.Time) error {
	if len(natives) == 0 && sessionID == "" {
		return nil
	}
	path := abortedSessionsPath(ledgerPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return fileutil.WithFileLock(context.Background(), path, func() error {
		record, err := loadAbortedSessions(ledgerPath)
		if err != nil {
			return err
		}
		for _, n := range natives {
			record.Sessions[n.id] = abortedSession{Agent: n.agent, Recording: recording, AbortedAt: at.UTC()}
		}
		if sessionID != "" {
			record.Recordings[sessionID] = abortedSession{Recording: recording, AbortedAt: at.UTC()}
		}
		return fileutil.AtomicWriteJSON(path, record, 0o600)
	})
}

// rememberAbort records, before abort deletes anything, the recording's ses_
// ID and the native sessions it covered. Like the draft removal it never
// stops the abort: a failure comes back as the warning the coworker needs.
func rememberAbort(recording, sessionID string, natives []abortedNative) string {
	if len(natives) == 0 && sessionID == "" {
		return ""
	}
	ledgerPath, err := resolveLedgerPath()
	if err == nil {
		err = recordAbortedSessions(ledgerPath, recording, sessionID, natives, time.Now())
	}
	if err != nil {
		slog.Warn("could not record aborted native sessions", "session", recording, "error", err)
		return fmt.Sprintf("could not record the abort for 'ox session import', which may offer this session again: %v", err)
	}
	return ""
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
