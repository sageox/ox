package gitserver

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestCredentialHelperArgs verifies the shared credential-helper argv used by
// both the ledger full-clone and the team-context two-phase clone.
// Failure prevented: one clone path drifts and stops supplying credentials,
// reintroducing the non-interactive username prompt.
func TestCredentialHelperArgs(t *testing.T) {
	orig := DefaultHelperCommand()
	t.Cleanup(func() { SetHelperCommand(orig) })
	SetHelperCommand("!ox git-credential-helper")
	args := CredentialHelperArgs()

	// exactly: clear inherited helpers, then install the ox helper
	assert.Equal(t, []string{
		"-c", "credential.helper=",
		"-c", "credential.helper=!ox git-credential-helper",
	}, args)
}
