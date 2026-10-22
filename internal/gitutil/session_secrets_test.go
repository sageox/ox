package gitutil

import (
	"strings"
	"testing"
)

func TestImmutableSessionBlobRequiresSecretGuard(t *testing.T) {
	for _, path := range []string{"sessions/synthetic/raw.jsonl", "sessions/synthetic/summary.md", "sessions/synthetic/meta.json"} {
		t.Run(path, func(t *testing.T) {
			// storage=git is a valid alternate publication mode, never a scan opt-out.
			if err := validateLedgerBlob(path, []byte(`{"content":"AKIAIOSFODNN7EXAMPLE"}`), "git"); err == nil {
				t.Fatal("immutable credential-bearing session blob was accepted")
			}
			if err := validateLedgerBlob(path, []byte(`{"content":"ordinary meeting notes"}`), "git"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// Every bare full SHA-1 is also the legacy Sourcegraph token shape, so scanning
// produced_commits verbatim refused every recording that made a commit.
const syntheticCommitSHA = "54629b67a1c3e9f0d2b4c6e8a0f1d3b5c7e9a2b4"

func TestSessionMetaProducedCommitsAreNotCredentials(t *testing.T) {
	meta := `{"session_name":"synthetic","produced_commits":["` + syntheticCommitSHA + `","` + strings.Repeat("ab12", 16) + `"]}`
	if err := validateLedgerBlob("sessions/synthetic/meta.json", []byte(meta), ""); err != nil {
		t.Fatalf("commit SHAs refused as credentials: %v", err)
	}
}

func TestSessionMetaCommitExemptionStaysNarrow(t *testing.T) {
	for name, meta := range map[string]string{
		"credential in produced_commits": `{"produced_commits":["` + syntheticCommitSHA + `","AKIAIOSFODNN7EXAMPLE"]}`,
		"credential beside commits":      `{"produced_commits":["` + syntheticCommitSHA + `"],"summary":"AKIAIOSFODNN7EXAMPLE"}`,
		"duplicate key hides a value":    `{"produced_commits":["AKIAIOSFODNN7EXAMPLE"],"produced_commits":["` + syntheticCommitSHA + `"]}`,
		"commits not a string list":      `{"produced_commits":[{"sha":"` + syntheticCommitSHA + `"}]}`,
		"uppercase is not git's output":  `{"produced_commits":["` + strings.ToUpper(syntheticCommitSHA) + `"]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateLedgerBlob("sessions/synthetic/meta.json", []byte(meta), ""); err == nil {
				t.Fatal("credential-bearing session meta was accepted")
			}
		})
	}
	// The exemption belongs to the meta.json schema, not to any session file.
	if err := validateLedgerBlob("sessions/synthetic/summary.json", []byte(`{"produced_commits":["`+syntheticCommitSHA+`"]}`), "git"); err == nil {
		t.Fatal("bare SHA outside session meta was exempted")
	}
}
