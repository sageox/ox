package daemon

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/sageox/ox/internal/addons"
	"github.com/sageox/ox/internal/gitserver"
	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/perf"
)

// TeamPublishCommitPrefix is the subject `ox skills publish` gives the commit
// that records a published skill in a Team Context. It is how both push legs —
// the CLI's own push right after the commit, and this daemon's retry — recognize
// a commit ox may publish.
const TeamPublishCommitPrefix = "add team skill: "

// TeamObservationCommitPrefix and TeamObservationDir identify the commits and
// paths written by the Team Context observation recorder.
const (
	TeamObservationCommitPrefix = "observation: "
	TeamObservationDir          = "memory/.observations"
)

type teamPublishScope int

const (
	foreignCommit teamPublishScope = iota
	skillCommit
	addonCommit
	observationCommit
)

func teamPublishScopeForSubject(subject string) teamPublishScope {
	switch {
	case strings.HasPrefix(subject, TeamPublishCommitPrefix):
		return skillCommit
	case strings.HasPrefix(subject, TeamObservationCommitPrefix):
		return observationCommit
	case strings.HasPrefix(subject, addons.CommitSubjectPrefix+"install "),
		strings.HasPrefix(subject, addons.CommitSubjectPrefix+"update "),
		strings.HasPrefix(subject, addons.CommitSubjectPrefix+"remove "):
		return addonCommit
	default:
		return foreignCommit
	}
}

func (s teamPublishScope) allowsPath(path string) bool {
	switch s {
	case skillCommit:
		return strings.HasPrefix(path, "agents/skills/")
	case addonCommit:
		return addons.IsTeamWritePath(path)
	case observationCommit:
		return strings.HasPrefix(path, TeamObservationDir+"/") && strings.HasSuffix(path, ".jsonl")
	default:
		return false
	}
}

// TeamPublishLogArgs lists the unpushed commits' subjects, one NUL-terminated
// record per commit. A newline-separated %s cannot tell a commit whose message
// is empty from a blank separator, and such a commit must block a push like any
// other commit that is not a publish.
var TeamPublishLogArgs = []string{"log", "--format=%s%x00", "@{upstream}..HEAD"}

// ShouldPushTeamPublishes decides whether ox may push a Team Context's unpushed
// commits, given `git log` output in TeamPublishLogArgs' format.
//
// `git push` moves the whole branch, so ox pushes only when EVERY unpushed
// commit is a publish commit. Anything else ahead of the remote is not ox's to
// publish: a coworker's own commit in their checkout, which they may be holding
// back on purpose, or a daemon housekeeping commit that must not reach the team
// (ox#1286). The same restriction daemon-git.md requires of every daemon push.
//
// Returns (blockingSubject, false) when another commit is ahead, so the caller
// can name it; ("", false) when nothing is unpushed or the output carries no
// records at all.
func ShouldPushTeamPublishes(logOutput string) (blockingSubject string, ok bool) {
	records := strings.Split(logOutput, "\x00")
	// Whatever follows the last NUL is git's line terminator, not a commit.
	records = records[:len(records)-1]
	if len(records) == 0 {
		return "", false
	}
	for _, record := range records {
		subject := strings.TrimSpace(record) // drops the newline git writes between records
		if teamPublishScopeForSubject(subject) != foreignCommit {
			continue
		}
		if subject == "" {
			return "(a commit with an empty message)", false
		}
		return subject, false
	}
	return "", true
}

// ErrForeignCommitAhead means a commit outside the team publish scope is ahead of
// the remote, so a publish push must not run.
var ErrForeignCommitAhead = errors.New("a commit outside the Team Context publish scope is ahead of the remote")

// ValidateTeamPublishPush is the PushOpts.ValidatePush for both publish push
// legs. It repeats ShouldPushTeamPublishes under the repository lock that
// PushWithRetry holds through the push, so a commit made after the caller's own
// check, or during a retry's rebase, can never ride a publish out.
func ValidateTeamPublishPush(ctx context.Context, repoPath string) (alreadyPublished bool, err error) {
	out, err := gitutil.RunGit(ctx, repoPath, "log", "--format=%H%x00%P%x00%s%x00", "@{upstream}..HEAD")
	if err != nil {
		return false, fmt.Errorf("list unpushed commits: %w", err)
	}
	if out == "" {
		return true, nil // a concurrent sync already pushed it
	}
	records := strings.Split(out, "\x00")
	if len(records)%3 != 1 || records[len(records)-1] != "" {
		return false, fmt.Errorf("%w: could not read the unpushed history", ErrForeignCommitAhead)
	}
	for i := 0; i < len(records)-1; i += 3 {
		hash, parents, subject := strings.TrimPrefix(records[i], "\n"), records[i+1], strings.TrimSpace(records[i+2])
		scope := teamPublishScopeForSubject(subject)
		// These writers never create merge commits. A merge can carry changes
		// hidden from a per-commit path listing, so it must stay local.
		if scope == foreignCommit || len(strings.Fields(parents)) != 1 {
			return false, fmt.Errorf("%w: %q", ErrForeignCommitAhead, subject)
		}
		// Validate each commit against its own scope. --no-renames exposes
		// both source and destination. Keep the hash before the NUL-terminated
		// paths so RunGit's outer whitespace trim cannot alter the first path.
		paths, err := gitutil.RunGit(ctx, repoPath, "diff-tree", "--name-only", "-r", "-z", "--no-renames",
			"--ignore-submodules=none", hash, "--")
		if err != nil {
			return false, fmt.Errorf("list paths of unpushed commit %s: %w", hash, err)
		}
		changed := strings.Split(paths, "\x00")
		if len(changed) < 3 || changed[0] != hash || changed[len(changed)-1] != "" {
			return false, fmt.Errorf("%w: no verifiable paths for %q", ErrForeignCommitAhead, subject)
		}
		for _, path := range changed[1 : len(changed)-1] {
			if !scope.allowsPath(path) {
				return false, fmt.Errorf("%w: %q changes %q outside its publish scope", ErrForeignCommitAhead, subject, path)
			}
		}
	}
	return false, nil
}

// pushTeamPublishCommits is the daemon's retry for Team Context writes: it pushes a
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
	out, err := s.git.RunGit(ctx, teamPath, TeamPublishLogArgs...)
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
		ValidatePush:      ValidateTeamPublishPush,
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
