package main

import (
	"github.com/sageox/ox/internal/gitserver"
	"github.com/sageox/ox/internal/ledger"
	"github.com/sageox/ox/internal/session"
)

// init wires cross-cutting hooks that internal packages need but cannot
// resolve themselves without creating import cycles. Two hooks today:
//
//   1. ledger.SetFieldRedactor — credential redaction for GitHub PR/Issue
//      cache writers (ox-8bkk). internal/ledger can't import
//      internal/session (cycle via session/health → ledger).
//
//   2. gitserver.SetHelperCommand — the shell command git invokes to
//      resolve credentials via ox-managed credential storage (ox-eeqi).
//      internal/gitserver can't import cmd/ox to discover the running
//      binary's absolute path.
//
// Both hooks apply to every ox CLI invocation, including the embedded
// daemon — the daemon does not have a separate main package.
func init() {
	r := session.NewRedactor()
	ledger.SetFieldRedactor(func(s string) string {
		if s == "" {
			return s
		}
		out, _ := r.RedactString(s)
		return out
	})

	gitserver.SetHelperCommand(HelperCommandString())
}
