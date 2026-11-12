package gitutil

import (
	"os"
	"os/exec"
	"slices"
	"strings"
)

// identityEnvVars override user.name and user.email for every commit git
// makes, ahead of any config.
var identityEnvVars = []string{"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL"}

// authorStampKey mirrors gitserver's stamp key; gitserver imports gitutil,
// so the constant cannot be shared.
const authorStampKey = "sageox.authorstamp"

// oxIdentityEnv returns env without the inherited commit identity when ox
// stamped repoPath's own identity (a Ledger or Team Context clone, or a repo
// where an AI coworker works). ox's hooks run inside the coding agent's
// environment, and a runner that hands the agent GIT_AUTHOR_* for its code
// commits would otherwise author ox's Ledger commits too (sageox/ox#1845).
// A repo ox never stamped keeps the inherited identity: it may be the only
// one git has, and removing it would fail the commit.
func oxIdentityEnv(repoPath string, env []string) []string {
	if repoPath == "" || !slices.ContainsFunc(env, isIdentityEntry) || !hasAuthorStamp(repoPath) {
		return env
	}
	return slices.DeleteFunc(slices.Clone(env), isIdentityEntry)
}

func isIdentityEntry(entry string) bool {
	name, _, _ := strings.Cut(entry, "=")
	return slices.Contains(identityEnvVars, name)
}

// hasAuthorStamp reports whether repoPath's local config holds ox's identity
// stamp alongside a user.email for it to stand behind.
func hasAuthorStamp(repoPath string) bool {
	for _, key := range []string{authorStampKey, "user.email"} {
		cmd := exec.Command("git", "-C", repoPath, "config", "--local", "--get", key)
		cmd.Dir = repoPath
		cmd.Env = append(slices.DeleteFunc(os.Environ(), isRepoLocationEntry), "GIT_TERMINAL_PROMPT=0")
		out, err := cmd.Output()
		if err != nil || strings.TrimSpace(string(out)) == "" {
			return false
		}
	}
	return true
}

// isRepoLocationEntry matches variables that would point git config at a
// repository other than repoPath, as a git hook's environment does.
func isRepoLocationEntry(entry string) bool {
	name, _, _ := strings.Cut(entry, "=")
	return name == "GIT_DIR" || name == "GIT_WORK_TREE" || name == "GIT_COMMON_DIR" || name == "GIT_CONFIG"
}

// OxIdentityEnv is oxIdentityEnv over this process's environment, for a
// caller that builds its own git command in an ox-owned clone.
func OxIdentityEnv(repoPath string) []string {
	return oxIdentityEnv(repoPath, os.Environ())
}
