package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/pkg/sessionprovenance"
)

// preserveLocalDeletionIntent applies only to explicit user deletion, never
// successful-upload cache pruning. Offline intent stays beside retained content.
func preserveLocalDeletionIntent(dir, ledgerPath string) error {
	source, err := session.ReadCaptureSource(dir)
	if err != nil {
		return err
	}
	nativeID := ""
	if source != nil {
		nativeID = source.NativeSessionID
	}
	if nativeID == "" {
		meta, err := lfs.ReadSessionMeta(dir)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if meta != nil && meta.Source != nil {
			nativeID = meta.Source.NativeSessionID
		}
	}
	if nativeID == "" {
		data, err := os.ReadFile(filepath.Join(dir, ".recording.json"))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err == nil {
			var state session.RecordingState
			if err := json.Unmarshal(data, &state); err != nil {
				return err
			}
			if state.AdapterName == "codex" {
				// Older opaque adapter handles do not establish native provenance.
				if _, err := sessionprovenance.Path(state.AgentSessionID); err == nil {
					nativeID = state.AgentSessionID
				}
			}
		}
	}
	if nativeID == "" {
		return nil
	}
	rel, err := sessionprovenance.Path(nativeID)
	if err != nil {
		return err
	}
	intent := struct {
		NativeID  string    `json:"native_session_id"`
		Reason    string    `json:"reason"`
		CreatedAt time.Time `json:"created_at"`
	}{nativeID, "deleted", time.Now().UTC()}
	if err := fileutil.AtomicWriteJSON(filepath.Join(dir, ".deletion-pending.json"), intent, 0600); err != nil {
		return err
	}
	if ledgerPath == "" {
		return fmt.Errorf("deletion pending: Ledger unavailable; local exclusion retained")
	}
	err = gitutil.WithRepoLock(context.Background(), ledgerPath, func() error {
		if err := gitutil.CheckSourcePublication(context.Background(), ledgerPath); err != nil {
			return err
		}
		if err := session.ExcludeNativeSession(ledgerPath, nativeID, "deleted", 0, -1); err != nil {
			return err
		}
		if _, err := gitutil.RunGit(context.Background(), ledgerPath, "add", "--sparse", "--", rel); err != nil {
			return err
		}
		_, err := gitutil.CommitLedgerSnapshot(context.Background(), ledgerPath, "session: preserve local deletion exclusion", rel)
		return err
	})
	if err != nil {
		return fmt.Errorf("deletion pending; local exclusion retained: %w", err)
	}
	return pushLedger(context.Background(), ledgerPath)
}

// pendingLocalDeletion is strictly observational and rejects malformed intent
// rather than allowing a corrupt privacy marker to resurrect native history.
func pendingLocalDeletion(ledger, repoID, nativeID string) (bool, error) {
	roots := []string{ledger, session.GetContextPath(repoID)}
	for _, root := range roots {
		if root == "" {
			continue
		}
		store, err := session.OpenStoreReadOnly(root)
		if err != nil {
			return false, err
		}
		for _, base := range []string{store.BasePath(), store.CacheSessionPath("")} {
			entries, err := os.ReadDir(base)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return false, err
			}
			for _, entry := range entries {
				if !entry.IsDir() {
					continue
				}
				blocked, err := localPrivacyIntent(filepath.Join(base, entry.Name()), nativeID)
				if err != nil || blocked {
					return blocked, err
				}
			}
		}
	}
	return false, nil
}

// Local recording state can predate source receipts. A known pause is a privacy
// boundary, never an uncertain candidate that individual confirmation overrides.
func localPrivacyIntent(dir, nativeID string) (bool, error) {
	for _, marker := range []string{".deletion-pending.json", ".capture-exclusion-pending.json"} {
		data, err := os.ReadFile(filepath.Join(dir, marker))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return false, err
		}
		var intent struct {
			NativeID string `json:"native_session_id"`
			Reason   string `json:"reason"`
		}
		if err = json.Unmarshal(data, &intent); err != nil {
			return false, fmt.Errorf("invalid local privacy intent: %w", err)
		}
		if _, err = sessionprovenance.Path(intent.NativeID); err != nil {
			return false, err
		}
		switch intent.Reason {
		case "deleted", "paused", "resumed", "aborted":
		default:
			return false, fmt.Errorf("invalid local privacy intent")
		}
		if intent.NativeID == nativeID {
			return true, nil
		}
	}
	data, err := os.ReadFile(filepath.Join(dir, ".recording.json"))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var state session.RecordingState
	if err = json.Unmarshal(data, &state); err != nil {
		return false, fmt.Errorf("invalid local recording state: %w", err)
	}
	if state.AdapterName != "codex" || state.AgentSessionID != nativeID {
		return false, nil
	}
	if state.SuspendedAt != nil || state.PauseCount > 0 {
		return true, nil
	}
	for _, event := range state.Lifecycle {
		if event.Action == session.LifecycleActionPause {
			return true, nil
		}
	}
	return false, nil
}
