// Package testenv provides shared process-environment isolation for
// test binaries. Blank-import this from any test package whose tests
// invoke `git` so the developer's global git config (`~/.gitconfig`)
// does not leak into the subprocess environment.
//
// What this avoids: when a developer has `commit.gpgsign=true` and a
// passphrase-protected SSH/GPG signing key configured globally, every
// `git commit` in a test fixture inherits that config and blocks
// waiting for a passphrase prompt the test runner cannot satisfy.
// Result: cascade failures across `internal/codedb/index`,
// `internal/doctor`, `internal/repotools`, `internal/uninstall`,
// `internal/secrets`, etc.
//
// The Makefile (`TEST_GIT_ISOLATION` in the `test`/`test-cover`/
// `test-all`/`test-slow` recipes) sets the same env vars at the
// `make`-driver level. This package covers the direct `go test ./pkg/`
// case — which IDE-driven runs and ad-hoc `go test` use — so the
// passing/failing posture is identical whether tests are invoked via
// make or go.
//
// Usage:
//
//	package mypkg_test
//
//	import (
//	    _ "github.com/sageox/ox/internal/testenv"
//	)
//
// init() honors existing env vars; if the Makefile or a CI environment
// already set them, we leave those values in place.
package testenv

import "os"

func init() {
	setIfUnset("GIT_CONFIG_GLOBAL", "/dev/null")
	setIfUnset("GIT_CONFIG_NOSYSTEM", "1")
	setIfUnset("GIT_TERMINAL_PROMPT", "0")

	// Disabling global config wipes the developer's (or CI runner's)
	// `user.name`/`user.email`. Production code paths invoked by tests
	// — e.g. EnsureCheckoutGitignore in internal/daemon — call `git
	// commit`, which fails with "Author identity unknown" without one
	// of: per-repo config, global config, or these env vars. Supplying
	// the env vars is the only one that works without per-test setup
	// AND survives our isolation.
	setIfUnset("GIT_AUTHOR_NAME", "ox-test")
	setIfUnset("GIT_AUTHOR_EMAIL", "test@test.sageox.ai")
	setIfUnset("GIT_COMMITTER_NAME", "ox-test")
	setIfUnset("GIT_COMMITTER_EMAIL", "test@test.sageox.ai")
}

func setIfUnset(name, value string) {
	if _, set := os.LookupEnv(name); set {
		return
	}
	_ = os.Setenv(name, value)
}
