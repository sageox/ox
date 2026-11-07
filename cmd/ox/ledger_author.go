package main

import (
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/gitserver"
	"github.com/sageox/ox/internal/identity"
)

// ox commits to the Ledger and Team Context as the signed-in SageOx account, so
// session recordings save even on a machine whose git has no identity. Wired
// here because only package main reaches auth, identity and user config
// together; gitserver falls back to git's own identity when nobody is signed in.
func init() {
	gitserver.SignedInUserGetter = func(remoteURL string) (name, email string) {
		return identity.SignedInAuthor(remoteURL, config.GetDisplayName())
	}
}
