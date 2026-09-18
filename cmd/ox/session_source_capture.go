package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/pkg/sessionprovenance"
)

// excludeNativeCapture publishes known native privacy boundaries before a
// retrospective import can select them. Unknown legacy boundaries fail closed.
func excludeNativeCapture(state *session.RecordingState, reason string) error {
	if state == nil || state.AdapterName != "codex" || state.AgentSessionID == "" {
		return nil
	}
	if reason == "resumed" {
		hasPause := false
		for _, event := range state.Lifecycle {
			if event.Action == session.LifecycleActionPause {
				hasPause = true
				break
			}
		}
		if !hasPause {
			return nil
		}
	}
	rel, err := sessionprovenance.Path(state.AgentSessionID)
	if err != nil {
		return err
	}
	// Persist privacy intent before the first network operation. Offline pause
	// and abort must block retrospective import even without a remote receipt.
	intentPath := filepath.Join(state.SessionPath, ".capture-exclusion-pending.json")
	if err := fileutil.AtomicWriteJSON(intentPath, map[string]any{"native_session_id": state.AgentSessionID, "reason": reason, "created_at": time.Now().UTC()}, 0600); err != nil {
		return err
	}
	ledgerPath := deriveLedgerPath(state.SessionPath)
	if ledgerPath == "" {
		return fmt.Errorf("cannot locate Ledger for native session exclusion")
	}
	err = gitutil.WithRepoLock(context.Background(), ledgerPath, func() error {
		if err := gitutil.CheckSourcePublication(context.Background(), ledgerPath); err != nil {
			return err
		}
		if reason == "paused" || reason == "resumed" {
			record, err := session.ReadSourceRecord(ledgerPath, state.AgentSessionID)
			if err != nil {
				return err
			}
			if record == nil {
				record = &sessionprovenance.Record{Version: 1, Agent: "codex", NativeSessionID: state.AgentSessionID}
			}
			var pending *session.LifecycleEvent
			for i := range state.Lifecycle {
				event := state.Lifecycle[i]
				if event.Action != session.LifecycleActionPause && event.Action != session.LifecycleActionResume {
					continue
				}
				if !event.SourceOffsetKnown {
					record.Exclusions = append(record.Exclusions, sessionprovenance.Exclusion{Start: 0, End: -1, Reason: "paused", CreatedAt: time.Now().UTC()})
					pending = nil
					break
				}
				if event.Action == session.LifecycleActionPause {
					pending = &event
					continue
				}
				if pending != nil {
					start, end := pending.Offset, event.Offset
					replaced := false
					for j := range record.Exclusions {
						ex := &record.Exclusions[j]
						if ex.Reason == "paused" && ex.Start == start && ex.CreatedAt.Equal(pending.At) {
							ex.End = end
							replaced = true
						}
					}
					if !replaced && end > start {
						record.Exclusions = append(record.Exclusions, sessionprovenance.Exclusion{Start: start, End: end, Reason: "paused", CreatedAt: pending.At})
					}
					pending = nil
				}
			}
			if pending != nil {
				found := false
				for _, ex := range record.Exclusions {
					if ex.Reason == "paused" && ex.Start == pending.Offset && ex.CreatedAt.Equal(pending.At) {
						found = true
					}
				}
				if !found {
					record.Exclusions = append(record.Exclusions, sessionprovenance.Exclusion{Start: pending.Offset, End: -1, Reason: "paused", CreatedAt: pending.At})
				}
			}
			kept := record.Exclusions[:0]
			for _, ex := range record.Exclusions {
				if ex.End == -1 || ex.End > ex.Start {
					kept = append(kept, ex)
				}
			}
			record.Exclusions = kept
			record.UpdatedAt = time.Now().UTC()
			if err = session.WriteSourceRecord(ledgerPath, record); err != nil {
				return err
			}
		} else if err := session.ExcludeNativeSession(ledgerPath, state.AgentSessionID, reason, state.StartOffset, -1); err != nil {
			return err
		}
		// A failed commit retains the atomic local tombstone. Abort must not delete
		// its recording until this succeeds; another machine needs the same intent.
		add := exec.Command("git", "-C", ledgerPath, "add", "--sparse", "--", filepath.FromSlash(rel))
		if out, err := add.CombinedOutput(); err != nil {
			return fmt.Errorf("stage source exclusion: %w: %s", err, out)
		}
		if _, err := gitutil.CommitLedgerSnapshot(context.Background(), ledgerPath, "session: preserve native capture exclusion", filepath.FromSlash(rel)); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err := pushLedger(context.Background(), ledgerPath); err != nil {
		return err
	}
	return os.Remove(intentPath)
}
