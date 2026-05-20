package repotools

// gitenv_test.go: blank-import internal/testenv so this package's test
// binary inherits the git-isolation env vars (GIT_CONFIG_GLOBAL=/dev/null,
// GIT_CONFIG_NOSYSTEM=1, GIT_TERMINAL_PROMPT=0). Without these, `git commit`
// invocations in test fixtures inherit the developer's global gpgsign /
// signingkey config and block on a passphrase prompt. The Makefile sets
// the same vars for `make test`/`test-all`/`test-slow`; this file covers
// direct `go test` runs and IDE-driven workflows.
//
// See internal/testenv/gitenv.go.

import _ "github.com/sageox/ox/internal/testenv"
