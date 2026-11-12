package gitutil

import (
	"context"
	"os"
	"slices"
	"strings"
	"time"
)

// identityEnvVars override user.name and user.email (and author.* and
// committer.*) for every commit git makes, ahead of any config.
var identityEnvVars = []string{"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL"}

// authorStampKey mirrors gitserver's stamp key; gitserver imports gitutil,
// so the constant cannot be shared.
const authorStampKey = "sageox.authorstamp"

// identityReadTimeout bounds the config read even when the caller's context
// has no deadline; the read is local and normally takes milliseconds.
const identityReadTimeout = 5 * time.Second

// oxIdentityEnv returns env with the inherited commit identity replaced by
// the one ox stamped into repoPath's local config (a Ledger or Team Context
// clone, or a repo where an AI coworker works). ox's hooks run inside the
// coding agent's environment, and a runner that hands the agent GIT_AUTHOR_*
// for its code commits would otherwise author ox's Ledger commits too
// (sageox/ox#1845). The stamped identity is set rather than the variables
// removed, because a global author.email or committer.email would otherwise
// outrank the local user.email and publish that address to the Ledger.
// A repo ox never stamped keeps the inherited identity: it may be the only
// one git has, and removing it would fail the commit.
func oxIdentityEnv(ctx context.Context, repoPath string, env []string) []string {
	if repoPath == "" || !slices.ContainsFunc(env, isIdentityEntry) {
		return env
	}
	name, email, ok := stampedIdentity(ctx, repoPath)
	if !ok {
		return env
	}
	out := slices.DeleteFunc(slices.Clone(env), isIdentityEntry)
	if name != "" {
		out = append(out, "GIT_AUTHOR_NAME="+name, "GIT_COMMITTER_NAME="+name)
	}
	return append(out, "GIT_AUTHOR_EMAIL="+email, "GIT_COMMITTER_EMAIL="+email)
}

func isIdentityEntry(entry string) bool {
	name, _, _ := strings.Cut(entry, "=")
	return slices.Contains(identityEnvVars, name)
}

// stampedIdentity reads repoPath's local user.name and user.email in one git
// call, and reports ok only when ox's stamp and a user.email are both there.
func stampedIdentity(ctx context.Context, repoPath string) (name, email string, ok bool) {
	ctx, cancel := context.WithTimeout(ctx, identityReadTimeout)
	defer cancel()
	cmd := commandContext(ctx, "git", "-C", repoPath, "config", "--local", "--get-regexp",
		`^(user\.name|user\.email|sageox\.authorstamp)$`)
	cmd.Dir = repoPath
	cmd.Env = append(slices.DeleteFunc(os.Environ(), isRepoLocationEntry), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.Output()
	if err != nil {
		return "", "", false
	}
	var stamped bool
	for line := range strings.SplitSeq(string(out), "\n") {
		key, value, _ := strings.Cut(strings.TrimSpace(line), " ")
		value = strings.TrimSpace(value)
		switch key {
		case "user.name":
			name = value
		case "user.email":
			email = value
		case authorStampKey:
			stamped = value != ""
		}
	}
	return name, email, stamped && email != ""
}

// isRepoLocationEntry matches variables that would point git config at a
// repository other than repoPath, as a git hook's environment does.
func isRepoLocationEntry(entry string) bool {
	name, _, _ := strings.Cut(entry, "=")
	return name == "GIT_DIR" || name == "GIT_WORK_TREE" || name == "GIT_COMMON_DIR" || name == "GIT_CONFIG"
}

// OxIdentityEnv is oxIdentityEnv over this process's environment, for a
// caller that builds its own git command in an ox-owned clone.
func OxIdentityEnv(ctx context.Context, repoPath string) []string {
	return oxIdentityEnv(ctx, repoPath, os.Environ())
}

// networkCmdRepo is the repository a NewNetworkCmd argument list names with a
// leading -C, or "" for one that names none (a clone).
func networkCmdRepo(args []string) string {
	if len(args) >= 2 && args[0] == "-C" {
		return args[1]
	}
	return ""
}
