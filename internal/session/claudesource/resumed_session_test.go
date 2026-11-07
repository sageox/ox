package claudesource

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// Claude Code writes a resumed or forked conversation to a new file named for
// its new session ID and opens it with a copy of the conversation it continues,
// still under that conversation's ID. A compacted session that was once resumed
// keeps that copy at its top.
//
// Failure prevented: every such file was refused as "session ID changed", so a
// resumed or compacted session's recording never found its transcript and
// reached the Ledger empty (#1398).
func TestValidateAcceptsTheConversationAResumedSessionInherits(t *testing.T) {
	repo, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const (
		own         = "230457d4-c384-4e38-90b5-3289ff44be62"
		parent      = "a1969243-7a0d-4f80-bc97-b71beefcb3e6"
		grandparent = "045c4e3b-1162-45ed-8ece-f0217e4bcead"
	)
	turn := func(id, cwd string) string {
		return fmt.Sprintf("{\"type\":\"user\",\"sessionId\":%q,\"cwd\":%q}\n", id, cwd)
	}
	title := func(id string) string { return fmt.Sprintf("{\"type\":\"custom-title\",\"sessionId\":%q}\n", id) }
	const snapshot = `{"type":"file-history-snapshot","messageId":"m1"}` + "\n"

	for _, tt := range []struct {
		name string
		body string
		want error // nil: accepted; errAny: refused, but neither untrusted nor uncheckable
	}{
		{name: "resumed: the parent's turns, then its own", body: turn(parent, repo) + snapshot + turn(parent, repo) + title(own) + turn(own, repo)},
		{name: "resumed twice: each ancestor in turn, then its own", body: turn(grandparent, repo) + turn(parent, repo) + turn(own, repo)},
		{name: "compacted after a resume", body: turn(parent, repo) + turn(own, repo) +
			fmt.Sprintf("{\"type\":\"system\",\"subtype\":\"compact_boundary\",\"sessionId\":%q,\"cwd\":%q}\n", own, repo) +
			fmt.Sprintf("{\"type\":\"user\",\"isCompactSummary\":true,\"sessionId\":%q,\"cwd\":%q}\n", own, repo) + turn(own, repo)},
		{name: "another session after its own", body: turn(own, repo) + turn(parent, repo), want: ErrUntrustedSource},
		{name: "an inherited ID again after its own", body: turn(parent, repo) + turn(own, repo) + turn(parent, repo), want: ErrUntrustedSource},
		{name: "an inherited turn from another repository", body: turn(parent, filepath.Dir(repo)) + turn(own, repo), want: ErrUntrustedSource},
		{name: "an inherited turn with no repository", body: fmt.Sprintf("{\"type\":\"user\",\"sessionId\":%q}\n", parent) + turn(own, repo), want: ErrUntrustedSource},
		// the copy is on disk but no turn of its own yet: retried, not quarantined
		{name: "only the inherited copy so far", body: turn(parent, repo), want: errAny},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), own+".jsonl")
			if err := os.WriteFile(path, []byte(tt.body), 0o600); err != nil {
				t.Fatal(err)
			}
			snap, err := Snapshot(path)
			if err != nil {
				t.Fatal(err)
			}
			for check, err := range map[string]error{
				"discovery":                  Validate(path, repo, own),
				"a stop of a hook recording": ValidateRecorded(path, repo, own, 0, snap),
				"a hook's first read":        ValidateRead(path, repo, own, 0, false, snap),
				"the ID-less daemon check":   Validate(path, repo, ""),
			} {
				switch tt.want {
				case nil:
					if err != nil {
						t.Fatalf("%s: %v", check, err)
					}
				case errAny:
					if err == nil || errors.Is(err, ErrUntrustedSource) || errors.Is(err, ErrUncheckable) {
						t.Fatalf("%s: want a retryable refusal, got %v", check, err)
					}
				default:
					if !errors.Is(err, tt.want) {
						t.Fatalf("%s: want %v, got %v", check, tt.want, err)
					}
				}
			}
		})
	}
}

// A scan that starts partway through cannot tell whether the file's own ID
// came before it, so it grants the inherited-block allowance to nothing.
// Failure prevented: a later hook read accepting another session's turns
// appended after the recording began.
func TestValidateFromNeverTreatsLaterRecordsAsInherited(t *testing.T) {
	repo, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const (
		own    = "230457d4-c384-4e38-90b5-3289ff44be62"
		parent = "a1969243-7a0d-4f80-bc97-b71beefcb3e6"
	)
	turn := func(id string) string {
		return fmt.Sprintf("{\"type\":\"user\",\"sessionId\":%q,\"cwd\":%q}\n", id, repo)
	}
	read := turn(parent) + turn(own)
	path := filepath.Join(t.TempDir(), own+".jsonl")
	if err := os.WriteFile(path, []byte(read+turn(own)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ValidateFrom(path, repo, own, int64(len(read))); err != nil {
		t.Fatalf("its own later turn: %v", err)
	}
	if err := os.WriteFile(path, []byte(read+turn(parent)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ValidateFrom(path, repo, own, int64(len(read))); !errors.Is(err, ErrUntrustedSource) {
		t.Fatalf("another session's turn after the read point: want untrusted, got %v", err)
	}
}

// errAny marks a refusal that must be neither ErrUntrustedSource nor ErrUncheckable.
var errAny = errors.New("any plain refusal")
