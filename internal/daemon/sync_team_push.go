package daemon

import (
	"context"
	"errors"
	"strings"

	"github.com/sageox/ox/internal/gitserver"
	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/perf"
)

// TeamPublishCommitPrefix is the subject `ox skills publish` gives the commit
// that records a published skill in a Team Context. It is how both push legs —
// the CLI's own push right after the commit, and this daemon's retry — recognize
// a commit ox may publish.
const TeamPublishCommitPrefix = "add team skill: "

// ShouldPushTeamPublishes decides whether ox may push a Team Context's unpushed
// commits, given their newline-separated subjects (`git log --format=%s
// @{upstream}..HEAD`).
//
// `git push` moves the whole branch, so ox pushes only when EVERY unpushed
// commit is a publish commit. Anything else ahead of the remote is not ox's to
// publish: a coworker's own commit in their checkout, which they may be holding
// back on purpose, or a daemon housekeeping commit that must not reach the team
// (ox#1286). The same restriction daemon-git.md requires of every daemon push.
//
// Returns (blockingSubject, false) when another commit is ahead, so the caller
// can name it; ("", false) when nothing is unpushed.
func ShouldPushTeamPublishes(logOutput string) (blockingSubject string, ok bool) {
	trimmed := strings.TrimSpace(logOutput)
	if trimmed == "" {
		return "", false
	}
	sawPublish := false
	for _, subject := range strings.Split(trimmed, "\n") {
		subject = strings.TrimSpace(subject)
		if subject == "" {
			continue
		}
		if !strings.HasPrefix(subject, TeamPublishCommitPrefix) {
			return subject, false
		}
		sawPublish = true
	}
	return "", sawPublish
}

// pushTeamPublishCommits is the daemon's leg of `ox skills publish`: it pushes a
// publish commit the CLI could not push itself (offline, an expired credential,
// a remote that moved underneath it). Without it the commit sat one ahead
// forever while publish told the human distribution was automatic (ox#1477).
//
// Runs after every team pull that left the clone usable — a completed pull, or
// the remote-unchanged skip, which is how a clone that is only AHEAD of its
// remote reads.
func (s *SyncScheduler) pushTeamPublishCommits(ctx context.Context, teamPath string) {
	ctx, span := perf.Start(ctx, "daemon:push_team_publishes")
	defer span.End()

	if until, wedged := gitutil.PushWedgedUntil(teamPath); wedged {
		s.logger.Debug("skipping team publish push: push wedged", "path", teamPath, "until", until)
		return
	}
	out, err := s.git.RunGit(ctx, teamPath, "log", "--format=%s", "@{upstream}..HEAD")
	if err != nil || strings.TrimSpace(out) == "" {
		return
	}
	if blocker, ok := ShouldPushTeamPublishes(out); !ok {
		if blocker != "" {
			s.logger.Debug("skipping team publish push: another commit is unpushed",
				"path", teamPath, "blocking_subject", blocker)
		}
		return
	}

	ep := s.workspaceRegistry.GetEndpoint()
	if err := gitutil.PushWithRetry(ctx, teamPath, gitutil.PushOpts{
		// Matches this daemon's pull of the same clone (pullTeamContext): its
		// look-alike discussion dirs pair as false renames.
		ImmutablePaths:    true,
		Logger:            s.logger,
		SuspendWhenWedged: true,
		PrePush: func(repoPath string) error {
			if ep != "" {
				return gitserver.RefreshRemoteCredentials(repoPath, ep)
			}
			return nil
		},
	}); err != nil {
		if errors.Is(err, gitutil.ErrPushWedged) {
			s.logger.Debug("team publish push skipped: push wedged", "path", teamPath, "error", err)
			return
		}
		s.logger.Warn("team publish push failed (will retry next sync)", "path", teamPath, "error", err)
		return
	}
	s.logger.Info("pushed team publish commits", "path", teamPath,
		"commits", len(strings.Split(strings.TrimSpace(out), "\n")))
}
