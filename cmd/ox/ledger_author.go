package main

import (
	"log/slog"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/gitserver"
	"github.com/sageox/ox/internal/identity"
)

// ox commits to the Ledger and Team Context as the AI coworker a team token
// acts as, else the signed-in SageOx account, so session recordings save even
// on a machine whose git has no identity. Wired here because only package main
// reaches auth, identity and user config together; gitserver falls back to
// git's own identity when neither applies.
func init() {
	gitserver.LedgerAuthorGetter = func(remoteURL string) (name, email string) {
		return identity.LedgerAuthor(remoteURL, config.GetDisplayName())
	}
}

// reconcileProjectCoworker makes the AI coworker a team token acts as the
// author of its commits in the project at projectRoot ("" finds it from the
// working directory), so its work is attributed to it and commits succeed on a
// machine whose git has no identity. With no coworker, it removes a stamp an
// earlier session left. A person's own local identity is never touched.
// cachedOnly never waits on the network: it uses what this machine last
// learned about a team token, and changes nothing for one it never saw.
// Best-effort: prime must not fail over it.
func reconcileProjectCoworker(projectRoot string, cachedOnly bool) {
	if projectRoot == "" {
		root, err := findProjectRoot()
		if err != nil {
			return
		}
		projectRoot = root
	}
	if !config.IsInitialized(projectRoot) {
		return
	}
	ep := endpoint.GetForProject(projectRoot)
	var name, email string
	if cachedOnly {
		var known bool
		if name, email, known = identity.CachedCoworkerAuthor(ep); !known {
			return
		}
	} else {
		name, email = identity.CoworkerAuthor(ep)
	}
	if err := gitserver.StampProjectAuthor(projectRoot, name, email); err != nil {
		slog.Warn("set AI coworker as project commit author failed", "repo", projectRoot, "error", err)
	}
}
