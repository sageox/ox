package gitutil

import (
	"strings"
	"testing"
)

// AWS's documentation key (AKIA…EXAMPLE) is allowlisted by the upstream rule,
// and so by the guard: it is not a credential and cannot stand in for one.
const syntheticAWSKey = "AKIA" + "QWERTYUIOPASDFGH"

func TestImmutableSessionBlobRequiresSecretGuard(t *testing.T) {
	for _, path := range []string{"sessions/synthetic/raw.jsonl", "sessions/synthetic/summary.md", "sessions/synthetic/meta.json"} {
		t.Run(path, func(t *testing.T) {
			// storage=git is a valid alternate publication mode, never a scan opt-out.
			if err := validateLedgerBlob(path, []byte(`{"content":"`+syntheticAWSKey+`"}`), "git"); err == nil {
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
		"credential in produced_commits": `{"produced_commits":["` + syntheticCommitSHA + `","` + syntheticAWSKey + `"]}`,
		"credential beside commits":      `{"produced_commits":["` + syntheticCommitSHA + `"],"summary":"` + syntheticAWSKey + `"}`,
		"duplicate key hides a value":    `{"produced_commits":["` + syntheticAWSKey + `"],"produced_commits":["` + syntheticCommitSHA + `"]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateLedgerBlob("sessions/synthetic/meta.json", []byte(meta), ""); err == nil {
				t.Fatal("credential-bearing session meta was accepted")
			}
		})
	}
	// The bundled guard no longer reads a keyword-less SHA as a credential, but
	// publication also scans with the latest upstream catalog, which may add a
	// bare-hex rule. Only exact git object IDs in meta.json's own array leave
	// the scanned bytes.
	for name, view := range map[string]struct{ path, data string }{
		"commits not a string list":     {"sessions/synthetic/meta.json", `{"produced_commits":[{"sha":"` + syntheticCommitSHA + `"}]}`},
		"uppercase is not git's output": {"sessions/synthetic/meta.json", `{"produced_commits":["` + strings.ToUpper(syntheticCommitSHA) + `"]}`},
		"not the meta.json schema":      {"sessions/synthetic/summary.json", `{"produced_commits":["` + syntheticCommitSHA + `"]}`},
	} {
		if got := string(secretScanView(view.path, []byte(view.data))); got != view.data {
			t.Errorf("%s: exemption removed bytes from the scan", name)
		}
	}
}

// The view must change nothing but the array: re-encoding the object would
// drop an earlier duplicate of any other key before the guard saw it.
func TestSessionMetaScanViewKeepsEveryOtherByte(t *testing.T) {
	in := `{"summary":"first" , "summary":"second","produced_commits":[ "` + syntheticCommitSHA + `", "keep" ],"z":1}`
	want := `{"summary":"first" , "summary":"second","produced_commits":["keep"],"z":1}`
	if got := string(secretScanView("sessions/synthetic/meta.json", []byte(in))); got != want {
		t.Fatalf("scan view changed bytes outside produced_commits:\n got %s\nwant %s", got, want)
	}
	for _, in := range []string{`{"produced_commits":["` + syntheticCommitSHA + `"]} trailing`, `[{"produced_commits":["` + syntheticCommitSHA + `"]}]`} {
		if got := string(secretScanView("sessions/synthetic/meta.json", []byte(in))); got != in {
			t.Fatalf("non-object input was rewritten: %s", got)
		}
	}
}
