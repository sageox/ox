package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/internal/paths"
)

// HeldMarkerFile marks a session folder the coworker chose to keep on this
// machine. A held session is never summarized, uploaded, or deleted by any
// automatic path — the daemon's finalize scan, the /clear and SessionEnd hooks,
// ox doctor, session recover, push-summary, or prune. Only an explicit
// `ox session upload <name>` publishes it, and that removes the marker after
// the commit lands (GH #1093, #1019).
//
// The decision lives on the session itself because the daemon is a separate
// process: it cannot see the OX_SESSION_PUBLISHING override the stop command
// ran with. Same shape as the .downloaded marker in internal/lfs.
//
// The marker lives only in a session's cache folder, never in the Ledger's
// git-tracked sessions/ tree: the daemon commits whole session folders, so a
// marker there would hold the session on every teammate's machine.
const HeldMarkerFile = ".held"

// ErrHoldInSharedTree is returned when a hold would be written into the
// Ledger's git-tracked sessions/ tree.
var ErrHoldInSharedTree = errors.New("hold marker refused inside the Ledger's shared sessions/ tree")

// HoldReason records why a session is held.
type HoldReason string

// HoldManualPublishing: the coworker stopped the session with
// session_publishing: manual.
const HoldManualPublishing HoldReason = "manual_publishing"

// HoldInfo is the content of a .held marker.
type HoldInfo struct {
	HeldAt time.Time  `json:"held_at"`
	Reason HoldReason `json:"reason"`
	// Source names the path that placed the hold (session_stop, hook,
	// daemon_recovery, doctor, recover), for diagnosis.
	Source string `json:"source,omitempty"`
}

// WriteHoldMarker holds the session in dir. An existing hold is kept as is, so
// the first held_at survives later writers. dir must already exist and must not
// be inside the Ledger's shared sessions/ tree.
func WriteHoldMarker(dir string, reason HoldReason, source string) error {
	if inSharedSessionsTree(dir) {
		return fmt.Errorf("hold session %s: %w", dir, ErrHoldInSharedTree)
	}
	// Skip only a marker confirmed present. IsHeld fails closed, so it would
	// report a marker it could not check as already written.
	if _, err := os.Lstat(filepath.Join(dir, HeldMarkerFile)); err == nil {
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, syscall.ENOTDIR) {
		return fmt.Errorf("hold session %s: check marker: %w", dir, err)
	}
	if info, err := os.Stat(dir); err != nil {
		return fmt.Errorf("hold session %s: %w", dir, err)
	} else if !info.IsDir() {
		return fmt.Errorf("hold session %s: not a directory", dir)
	}
	hold := HoldInfo{HeldAt: time.Now().UTC(), Reason: reason, Source: source}
	if err := fileutil.AtomicWriteJSON(filepath.Join(dir, HeldMarkerFile), hold, 0o644); err != nil {
		return fmt.Errorf("write hold marker: %w", err)
	}
	return nil
}

// IsHeld reports whether the session in dir is held. It fails closed: a marker
// that exists but cannot be read, or one that cannot be checked for any reason
// other than "it does not exist", counts as held.
func IsHeld(dir string) bool {
	_, err := os.Lstat(filepath.Join(dir, HeldMarkerFile))
	if err == nil {
		return true
	}
	// ENOTDIR: a path component is a file, so no marker can exist below it.
	return !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, syscall.ENOTDIR)
}

// ReadHoldMarker returns the hold recorded in dir.
func ReadHoldMarker(dir string) (HoldInfo, error) {
	var hold HoldInfo
	data, err := os.ReadFile(filepath.Join(dir, HeldMarkerFile))
	if err != nil {
		return hold, err
	}
	if err := json.Unmarshal(data, &hold); err != nil {
		return hold, fmt.Errorf("parse hold marker: %w", err)
	}
	return hold, nil
}

// ClearHoldMarker releases the hold on dir. Call it only after the session
// has been published, never before.
func ClearHoldMarker(dir string) error {
	if err := os.Remove(filepath.Join(dir, HeldMarkerFile)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("release hold: %w", err)
	}
	return nil
}

// RecordedManualPublishing reports whether a recording was started under
// session_publishing: manual. Crash recovery uses it to hold the session
// instead of publishing it.
func (s *RecordingState) RecordedManualPublishing() bool {
	return s != nil && s.PublishingMode == config.SessionPublishingManual
}

// HoldRecordedManual holds a recovered recording that was started under
// session_publishing: manual. Call it before the recording's .recording.json
// is removed: after that, the hold is the only record of the coworker's
// choice, and the recovering process (doctor, recover) may run without the
// CLI's environment.
func HoldRecordedManual(state *RecordingState, source string) error {
	if !state.RecordedManualPublishing() || state.SessionPath == "" {
		return nil
	}
	return WriteHoldMarker(state.SessionPath, HoldManualPublishing, source)
}

// HeldAnywhere reports whether a session named name is held in any of the
// given sessions directories. A session can have copies in more than one cache
// location, and a hold on any copy holds them all — otherwise an unheld copy
// elsewhere would still be published.
func HeldAnywhere(name string, sessionsDirs ...string) bool {
	if name == "" {
		return false
	}
	for _, base := range sessionsDirs {
		if base != "" && IsHeld(filepath.Join(base, name)) {
			return true
		}
	}
	return false
}

// inSharedSessionsTree reports whether dir is <ledger>/sessions/<name>: its
// parent is named "sessions" and sits at the root of a git work tree. The
// Ledger cache (<ledger>/.sageox/cache/sessions/<name>) and the XDG cache are
// not git roots, so they pass.
func inSharedSessionsTree(dir string) bool {
	parent := filepath.Dir(filepath.Clean(dir))
	if filepath.Base(parent) != "sessions" {
		return false
	}
	_, err := os.Lstat(filepath.Join(filepath.Dir(parent), ".git"))
	return err == nil
}

// HeldSessionDirs lists every local cache location a session for the Ledger
// at ledgerPath can live in: the Ledger cache and the XDG caches. A hold on a
// copy in any of them holds the session by name. The Ledger's git-tracked
// sessions/ tree is deliberately absent: a hold is never written there.
func HeldSessionDirs(ledgerPath string) []string {
	dirs := []string{filepath.Join(ledgerPath, ".sageox", "cache", "sessions")}
	repoID := filepath.Base(ledgerPath)
	if ledgerPath == "" || repoID == "" || repoID == "." || repoID == string(filepath.Separator) {
		return dirs
	}
	dirs = append(dirs, filepath.Join(paths.SessionCacheDir(repoID), "sessions"))
	for _, d := range paths.AlternateSessionCacheDirs(repoID) {
		dirs = append(dirs, filepath.Join(d, "sessions"))
	}
	return dirs
}

// IsHeldInLedger reports whether the session named name, belonging to the
// Ledger at ledgerPath, is held in any of its cache locations.
func IsHeldInLedger(ledgerPath, name string) bool {
	return HeldAnywhere(name, HeldSessionDirs(ledgerPath)...)
}
