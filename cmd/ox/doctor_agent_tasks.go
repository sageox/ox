package main

import (
	"fmt"
	"strings"

	"github.com/sageox/ox/internal/agenttask"
)

// maxTaskAttempts is the number of times a task may be (re)claimed before
// doctor treats it as poison and cancels it on --fix. A task that is claimed,
// never completed, reclaimed, claimed again, ... forever would otherwise churn
// agents indefinitely.
const maxTaskAttempts = 5

// checkAgentTasksStuck inspects the project-local agent task queue. Reading the
// store reconciles it (reclaims expired/dead-claimer leases, prunes
// expired/old-terminal rows) as a side effect, so this check doubles as the
// self-heal trigger. It then surfaces poison tasks — those reclaimed past
// maxTaskAttempts — and cancels them under --fix.
func checkAgentTasksStuck(fix bool) checkResult {
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return SkippedCheck("Agent tasks", "not in git repo", "")
	}

	// only inspect if the queue file exists — avoid creating the directory as
	// a side effect of a read-only health check.
	if !agenttask.QueueExists(gitRoot) {
		return SkippedCheck("Agent tasks", "no task queue", "")
	}

	store, err := agenttask.NewStore(gitRoot)
	if err != nil {
		return SkippedCheck("Agent tasks", "could not open task store", "")
	}
	defer store.Close()

	// List reconciles leases and prunes as a side effect.
	tasks, err := store.List(true)
	if err != nil {
		return SkippedCheck("Agent tasks", "could not read task queue", "")
	}

	var ready, inProgress int
	var poison []*agenttask.Task
	for _, t := range tasks {
		switch t.Status {
		case agenttask.StatusReady:
			ready++
		case agenttask.StatusInProgress:
			inProgress++
		}
		// Poison = repeatedly (re)claimed but never finished. Exclude live
		// in_progress tasks: List() already reconciled away expired/dead-claimer
		// leases, so a still-in_progress task has a live claimer on a valid lease
		// and is legitimately working — canceling it would discard real work and
		// override the agent's `tasks extend`.
		if t.Status != agenttask.StatusInProgress && !t.IsTerminal() && t.Attempts >= maxTaskAttempts {
			poison = append(poison, t)
		}
	}

	if len(poison) == 0 {
		if ready == 0 && inProgress == 0 {
			return SkippedCheck("Agent tasks", "queue empty", "")
		}
		return PassedCheck("Agent tasks",
			fmt.Sprintf("%d ready, %d in progress", ready, inProgress))
	}

	// describe the poison tasks
	var ids []string
	for _, t := range poison {
		ids = append(ids, fmt.Sprintf("%s (%d attempts)", shortID(t.ID), t.Attempts))
	}

	if fix {
		canceled := 0
		failed := 0
		for _, t := range poison {
			if err := store.Cancel(t.ID, fmt.Sprintf("auto-canceled by doctor after %d attempts", t.Attempts)); err == nil {
				canceled++
			} else {
				failed++
			}
		}
		if failed > 0 {
			// Don't claim success while poison remains — the next run retries.
			return WarningCheck("Agent tasks",
				fmt.Sprintf("canceled %d poison task(s); %d could not be canceled", canceled, failed),
				"Re-run `ox doctor --fix`; if it persists, inspect the queue with `ox agent <id> tasks list`")
		}
		return PassedCheck("Agent tasks",
			fmt.Sprintf("canceled %d poison task(s) past %d attempts", canceled, maxTaskAttempts))
	}

	return WarningCheck("Agent tasks",
		fmt.Sprintf("%d task(s) repeatedly failing: %s", len(poison), strings.Join(ids, ", ")),
		fmt.Sprintf("Run `ox doctor --fix` to cancel tasks past %d attempts", maxTaskAttempts))
}
