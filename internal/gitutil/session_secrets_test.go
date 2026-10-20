package gitutil

import "testing"

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
