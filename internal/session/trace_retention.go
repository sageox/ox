package session

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/paths"
)

// UnfinalizedNativeSessionIDs protects traces still referenced by local
// recordings, across projects and endpoints. Any unreadable state aborts pruning:
// uncertainty must retain data, not make it look unreferenced.
func UnfinalizedNativeSessionIDs() (map[string]bool, error) {
	protected := make(map[string]bool)
	manifests := make(map[string][]string)
	endpoints, err := readTraceDirs(paths.DataDir())
	if err != nil {
		return nil, err
	}
	for _, ep := range endpoints {
		repos, err := readTraceDirs(paths.LedgersDataDir("", ep))
		if err != nil {
			return nil, err
		}
		for _, repo := range repos {
			manifestDir := filepath.Join(paths.LedgersDataDir(repo, ep), "sessions")
			manifests[repo] = append(manifests[repo], manifestDir)
			cacheDir := filepath.Join(paths.LedgerSessionCacheBase(repo, ep), "sessions")
			if err := protectTraceReferences(cacheDir, []string{manifestDir}, protected); err != nil {
				return nil, err
			}
		}
	}
	bases := append([]string{paths.SessionCacheDir("")}, paths.AlternateSessionCacheDirs("")...)
	for _, base := range bases {
		repos, err := readTraceDirs(base)
		if err != nil {
			return nil, err
		}
		for _, repo := range repos {
			if err := protectTraceReferences(filepath.Join(base, repo, "sessions"), manifests[repo], protected); err != nil {
				return nil, err
			}
		}
	}
	return protected, nil
}

func readTraceDirs(path string) ([]string, error) {
	entries, err := os.ReadDir(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("inspect recording references: %w", err)
	}
	var names []string
	for _, entry := range entries {
		// Do not walk symlinks into unrelated trees. Refuse pruning when one
		// could conceal recording state rather than silently overlooking it.
		if entry.Type()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("recording reference directory is a symlink: %s", filepath.Join(path, entry.Name()))
		}
		if entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	return names, nil
}

func protectTraceReferences(sessionsDir string, manifestDirs []string, protected map[string]bool) error {
	names, err := readTraceDirs(sessionsDir)
	if err != nil {
		return err
	}
	for _, name := range names {
		dir := filepath.Join(sessionsDir, name)
		state, err := ReadRecordingStateFile(dir)
		if err == nil && state != nil {
			protectNativeIDs(protected, state.NativeSessions)
			if state.AgentSessionID != "" {
				protected[strings.ToLower(state.AgentSessionID)] = true
			}
			if state.AgentSessionID == "" && len(state.NativeSessions) == 0 {
				if err := protectRawNativeIDs(filepath.Join(dir, "raw.jsonl"), protected); err != nil {
					return err
				}
			}
			continue // stopped/paused markers still await finalization
		}
		if err != nil {
			return err
		}
		finalized := false
		for _, base := range append([]string{sessionsDir}, manifestDirs...) {
			meta, err := lfs.ReadSessionMeta(filepath.Join(base, name))
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err != nil {
				return err
			}
			if _, hasRaw := meta.Files["raw.jsonl"]; !meta.IsDraft() && hasRaw {
				finalized = true
				break
			}
			protectNativeIDs(protected, meta.NativeSessions)
		}
		if finalized {
			continue
		}
		// SessionEnd can remove the marker before the daemon finalizes. The
		// append-only raw carrier preserves every native ID for that window.
		if err := protectRawNativeIDs(filepath.Join(dir, "raw.jsonl"), protected); err != nil {
			return err
		}
	}
	return nil
}

func protectNativeIDs(protected map[string]bool, sessions []lfs.NativeSession) {
	for _, native := range sessions {
		protected[strings.ToLower(native.ID)] = true
	}
}

func protectRawNativeIDs(path string, protected map[string]bool) error {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		var entry struct {
			NativeSessions []lfs.NativeSession `json:"native_sessions"`
			Metadata       *StoreMeta          `json:"metadata"`
			Meta           *StoreMeta          `json:"_meta"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			return fmt.Errorf("inspect unfinalized trace references: %w", err)
		}
		protectNativeIDs(protected, entry.NativeSessions)
		if entry.Metadata != nil {
			protectNativeIDs(protected, entry.Metadata.NativeSessions)
		}
		if entry.Meta != nil {
			protectNativeIDs(protected, entry.Meta.NativeSessions)
		}
	}
	return scanner.Err()
}
