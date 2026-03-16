package index

import (
	"github.com/go-git/go-git/v6"
)

// plainOpenTolerant opens a git repo via go-git.
//
// In go-git v5, this required an extensionSafeStorer workaround to strip
// [extensions] from the in-memory config (objectformat, worktreeconfig, etc.)
// because v5 rejected repos with any extensions it didn't recognize.
//
// go-git v6 handles all known extensions natively, making the workaround
// unnecessary. TestV6_PlainOpenAcceptsKnownExtensions verifies this.
// If a future git version adds extensions that v6 doesn't recognize,
// the workaround can be restored from git history.
func plainOpenTolerant(path string) (*git.Repository, error) {
	return git.PlainOpen(path)
}
