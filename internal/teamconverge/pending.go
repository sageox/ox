package teamconverge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/sageox/ox/internal/fileutil"
)

const (
	pendingSchemaVersion            = 1
	pendingRelativePath             = ".sageox/cache/team-convergence.json"
	MaxAutomaticConvergenceAttempts = 3
)

// AutomaticRetryAllowed reports whether the daemon may spend another retry on
// a durable convergence failure. The initial failed convergence is attempt one;
// at most two unchanged sync passes follow it. A new Team Context commit resets
// the counter through SavePending, while explicit `ox sync` remains available
// after the automatic budget is exhausted.
func AutomaticRetryAllowed(record *PendingRecord, teamPath string) bool {
	return record != nil && record.Status == PendingRetry &&
		record.Attempts < MaxAutomaticConvergenceAttempts &&
		record.TeamPath != "" && teamPath != "" &&
		filepath.Clean(record.TeamPath) == filepath.Clean(teamPath)
}

type PendingStatus string

const (
	PendingRetry  PendingStatus = "pending"
	PendingFailed PendingStatus = "failed"
)

// PendingRecord is durable, machine-local scheduler state. It is derived from
// the canonical Team Context and safe to discard, but surviving daemon restarts
// closes the event-loss gap after a pull has already consumed the changed commit.
type PendingRecord struct {
	SchemaVersion int           `json:"schema_version"`
	Status        PendingStatus `json:"status"`
	TeamPath      string        `json:"team_path"`
	TeamCommit    string        `json:"team_commit,omitempty"`
	Attempts      int           `json:"attempts"`
	LastAttempt   time.Time     `json:"last_attempt"`
	Reason        string        `json:"reason,omitempty"`
	Outcomes      []Outcome     `json:"outcomes,omitempty"`
}

func PendingPath(projectRoot string) string {
	return filepath.Join(projectRoot, filepath.FromSlash(pendingRelativePath))
}

func LoadPending(projectRoot string) (*PendingRecord, error) {
	data, err := os.ReadFile(PendingPath(projectRoot))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read Team Context convergence state: %w", err)
	}
	var record PendingRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return nil, fmt.Errorf("parse Team Context convergence state: %w", err)
	}
	if record.SchemaVersion != pendingSchemaVersion {
		return nil, fmt.Errorf("unsupported Team Context convergence state schema %d", record.SchemaVersion)
	}
	return &record, nil
}

func SavePending(projectRoot string, status PendingStatus, report Report, reason string) (*PendingRecord, error) {
	path := PendingPath(projectRoot)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create Team Context convergence cache: %w", err)
	}
	var record *PendingRecord
	err := fileutil.WithFileLock(context.Background(), path, func() error {
		previous, loadErr := LoadPending(projectRoot)
		if loadErr != nil {
			// A corrupt record must not silently reset the retry budget without a
			// trace: fall back to "no prior record" (attempts restart at 1), but
			// say so, since that's otherwise indistinguishable from a fresh repo.
			slog.Warn("team convergence pending record unreadable; retry budget reset", "project_root", projectRoot, "error", loadErr)
		}

		// The attempt counter is keyed on TeamPath alone, not (TeamPath, TeamCommit).
		// TeamCommit is not always known: a convergence that fails before discovery
		// completes has no commit to report, and keying on the pair let that unknown
		// state masquerade as a commit change and silently reset the budget whenever
		// automatic retries alternated between an error and a reported-pending
		// result. A commit is treated as "changed" only when both the previous and
		// the new value are known and differ — an unknown/empty commit neither
		// resets the counter nor overwrites the last known commit.
		commit := report.Snapshot.Commit
		attempts := 1
		if previous != nil && previous.TeamPath == report.Snapshot.Path {
			attempts = previous.Attempts + 1
			switch {
			case commit == "":
				commit = previous.TeamCommit
			case previous.TeamCommit != "" && commit != previous.TeamCommit:
				attempts = 1 // a real, new commit is materially new work
			}
		}
		record = &PendingRecord{
			SchemaVersion: pendingSchemaVersion,
			Status:        status, TeamPath: report.Snapshot.Path, TeamCommit: commit,
			Attempts: attempts, LastAttempt: time.Now().UTC(), Reason: reason,
			Outcomes: append([]Outcome(nil), report.Outcomes...),
		}
		return fileutil.AtomicWriteJSON(path, record, 0o600)
	})
	if err != nil {
		return nil, fmt.Errorf("write Team Context convergence state: %w", err)
	}
	return record, nil
}

func ClearPending(projectRoot string) error {
	path := PendingPath(projectRoot)
	return fileutil.WithFileLock(context.Background(), path, func() error {
		err := os.Remove(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	})
}

// PendingStatusFor distinguishes transient work from content or capability
// failures that need human action. Only PendingRetry is eligible for the
// scheduler's bounded anti-entropy retry; settled failures wait for an explicit
// sync or a new Team Context artifact change.
func PendingStatusFor(report Report) PendingStatus {
	for _, outcome := range report.Outcomes {
		if outcome.State == StateError || outcome.State == StateConflict ||
			(outcome.State == StateUnsupported && outcome.Required) || outcome.State == StatePendingApproval {
			return PendingFailed
		}
	}
	return PendingRetry
}

func FailureReason(report Report) string {
	for _, outcome := range report.Outcomes {
		switch outcome.State {
		case StatePending, StateError, StateConflict, StatePendingApproval:
			if outcome.Detail != "" {
				return outcome.Detail
			}
			return string(outcome.State) + ": " + string(outcome.Kind) + "/" + outcome.Name
		case StateUnsupported:
			if outcome.Required {
				if outcome.Detail != "" {
					return outcome.Detail
				}
				return "unsupported: " + string(outcome.Kind) + "/" + outcome.Name
			}
		}
	}
	return "Team Context convergence is incomplete"
}
