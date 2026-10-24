package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/sageox/ox/internal/daemon"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/gitutil"
)

// skills_publish_push.go — the push leg of `ox skills publish` (ox#1477).
//
// The publish commit is only useful once it is on the Team Context's remote: a
// coworker's sync pulls from there. So publish pushes right after it commits,
// and when it cannot, says so plainly and leaves the retry to the daemon's
// Team Context sync, which pushes the same commits under the same rule
// (daemon.ShouldPushTeamPublishes).

// The push is bounded by attempts and a per-git-operation timeout, NOT by a
// context deadline: PushWithRetry may be mid-rebase when a deadline fires, and a
// killed rebase leaves the checkout wedged (see pushLedger). Two attempts of 20s
// operations cap a pathological network near a minute; a healthy push of one
// small commit takes a second or two, and anything slower is the daemon's job.
const (
	teamPublishPushAttempts  = 2
	teamPublishPushOpTimeout = 20 * time.Second
)

// teamPublishPush is what happened to the publish commit after it was recorded.
type teamPublishPush struct {
	pushed bool
	// reason says why the commit is still local; empty when pushed.
	reason string
	// retried is true when ox's own sync will push it later: the commit is
	// ox's to push and only the attempt failed. False means a human must act.
	retried bool
}

func (o *skillsChangeOutput) applyPush(p teamPublishPush) {
	o.Pushed, o.NotPushedReason, o.pushRetried = p.pushed, p.reason, p.retried
}

// teamPublishCommitMessage names the skills in the commit subject the push
// legs recognize.
func teamPublishCommitMessage(names []string) string {
	return daemon.TeamPublishCommitPrefix + strings.Join(names, ", ")
}

// pushTeamPublish pushes the Team Context after a publish commit. The caller
// must NOT hold the repository lock: PushWithRetry takes it itself and the lock
// is not re-entrant.
func pushTeamPublish(repoRoot, teamPath string) teamPublishPush {
	logCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	subjects, err := gitutil.RunGit(logCtx, teamPath, daemon.TeamPublishLogArgs...)
	if err != nil {
		return teamPublishPush{reason: "the Team Context branch tracks no remote branch ox can push to"}
	}
	blocker, ok := daemon.ShouldPushTeamPublishes(subjects)
	switch {
	case !ok && blocker != "":
		return teamPublishPush{reason: fmt.Sprintf(
			"another unpushed commit in it (%q) is ahead of the publish, and ox pushes only its own publish commits", blocker)}
	case !ok:
		// Nothing ahead of the remote: a concurrent sync already carried it.
		return teamPublishPush{pushed: true}
	}

	opts := teamContextPushOpts(endpoint.GetForProject(repoRoot))
	opts.MaxRetries = teamPublishPushAttempts
	opts.OpTimeout = teamPublishPushOpTimeout
	opts.ValidatePush = daemon.ValidateTeamPublishPush
	if err := gitutil.PushWithRetry(context.Background(), teamPath, opts); err != nil {
		if errors.Is(err, daemon.ErrForeignCommitAhead) {
			// Re-checked under the push lock: a commit landed after the check
			// above. The daemon refuses it too, so no retry is promised.
			return teamPublishPush{reason: fmt.Sprintf(
				"another unpushed commit landed ahead of the publish (%s), and ox pushes only its own publish commits", err)}
		}
		slog.Warn("team skill publish push failed", "team_context", teamPath, "error", err)
		reason := strings.Join(strings.Fields(err.Error()), " ")
		// Only the daemon retries a failed publish push; with it disabled,
		// promising "ox retries" would leave the skill local forever.
		if !teamPublishDaemonRetries() {
			return teamPublishPush{reason: reason + "; the ox daemon is disabled here, so nothing retries the push"}
		}
		return teamPublishPush{reason: reason, retried: true}
	}
	slog.Info("team skill publish pushed", "team_context", teamPath)
	return teamPublishPush{pushed: true}
}

// teamPublishDaemonRetries reports whether a daemon will retry a failed
// publish push. A var so tests do not depend on the host's runtime
// capabilities, which can disable the daemon on their own.
var teamPublishDaemonRetries = func() bool { return !daemon.IsDaemonDisabled() }

// teamPublishGuidance tells the publisher where the skills actually are. It
// never says distribution happened unless the push did.
func teamPublishGuidance(names []string, out skillsChangeOutput) string {
	subject := strings.Join(names, ", ")
	const after = "Their `repos:` metadata controls where they arrive; without `repos:` they reach every team repository. Verify with `ox skills status`, then remove any redundant repository copy."
	switch {
	case out.Pushed:
		return fmt.Sprintf("%s published to your Team Context and pushed, without changing the repository copies; coworkers receive it on their next sync. %s", subject, after)
	case out.pushRetried:
		return fmt.Sprintf("%s committed to your Team Context but NOT pushed yet, so no coworker has it: %s. ox retries the push on its next Team Context sync; run `ox sync` to retry now. %s", subject, out.NotPushedReason, after)
	default:
		return fmt.Sprintf("%s committed to your Team Context but NOT pushed, so no coworker has it: %s. ox will not push it for you; resolve that, then run `git -C %s push`. %s", subject, out.NotPushedReason, out.TeamContext, after)
	}
}
