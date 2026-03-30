package main

import (
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/sageox/ox/internal/daemon"
)

// checkDaemonDeduplication detects multiple running daemons for the same repo_id
// and stops all but the newest. Multiple daemons waste resources and arise when
// clones/worktrees each spawn their own daemon due to workspace ID drift (e.g.,
// daemon started from a subdirectory CWD, causing path-based fallback hashing).
func checkDaemonDeduplication(fix bool) checkResult {
	running, err := daemon.ListRunningDaemons()
	if err != nil {
		return SkippedCheck("Daemon deduplication", "registry unavailable", err.Error())
	}
	if len(running) < 2 {
		return PassedCheck("Daemon deduplication", "no duplicates")
	}

	// group by repo_id (skip entries with empty repo_id — they can't be compared)
	byRepo := map[string][]daemon.DaemonInfo{}
	for _, d := range running {
		if d.RepoID != "" {
			byRepo[d.RepoID] = append(byRepo[d.RepoID], d)
		}
	}

	var dupes []daemon.DaemonInfo
	for _, group := range byRepo {
		if len(group) < 2 {
			continue
		}
		// keep newest (by StartedAt), flag the rest as duplicates
		sort.Slice(group, func(i, j int) bool {
			return group[i].StartedAt.After(group[j].StartedAt)
		})
		dupes = append(dupes, group[1:]...)
	}

	if len(dupes) == 0 {
		return PassedCheck("Daemon deduplication", "no duplicates")
	}

	if !fix {
		detail := fmt.Sprintf("%d duplicate daemon(s) for the same repo", len(dupes))
		return FailedCheck("Daemon deduplication", detail,
			"Run 'ox doctor --fix' to stop duplicates",
		).WithFixInfo(CheckSlugDaemonDedup, FixLevelConfirm)
	}

	// stop each duplicate via IPC with longer timeout, wait for exit, then unregister
	reg, _ := daemon.LoadRegistry()
	var stopped, failed int
	for _, d := range dupes {
		client := daemon.NewClientWithSocketAndTimeout(d.SocketPath, 2*time.Second)
		if err := client.Stop(); err != nil {
			slog.Debug("failed to stop duplicate daemon", "workspace_id", d.WorkspaceID, "pid", d.PID, "error", err)
			failed++
			continue
		}
		// wait for the process to actually exit before unregistering
		if !daemon.WaitForProcessExit(d.PID, 5*time.Second) {
			slog.Debug("duplicate daemon did not exit in time", "workspace_id", d.WorkspaceID, "pid", d.PID)
			failed++
			continue
		}
		stopped++
		if reg != nil {
			_ = reg.Unregister(d.WorkspaceID)
		}
	}

	if failed > 0 {
		return WarningCheck("Daemon deduplication",
			fmt.Sprintf("stopped %d, failed %d duplicate(s)", stopped, failed),
			"Run 'ox doctor' again or manually kill stale processes",
		)
	}
	return PassedCheck("Daemon deduplication",
		fmt.Sprintf("stopped %d duplicate daemon(s)", stopped))
}
