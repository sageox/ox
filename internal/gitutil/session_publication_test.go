package gitutil

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/secretguard"
	"github.com/stretchr/testify/require"
)

func useFreshFixtureRules(t *testing.T) {
	t.Helper()
	data, err := os.ReadFile("../secretguard/gitleaks.toml")
	require.NoError(t, err)
	rules, err := secretguard.NewRules(data)
	require.NoError(t, err)
	original := freshSessionPublicationRules
	freshSessionPublicationRules = func(context.Context) (*secretguard.RuleSet, error) { return rules, nil }
	t.Cleanup(func() { freshSessionPublicationRules = original })
}

func TestCommittedSessionScanCannotTrustWorktreeOrFinalDiff(t *testing.T) {
	for _, mode := range []string{"clean worktree", "redacted later commit", "deleted later commit"} {
		t.Run(mode, func(t *testing.T) {
			useFreshFixtureRules(t)
			rel := "sessions/synthetic/raw.jsonl"
			ledger := publicationLedger(t, rel, `{"content":"`+syntheticAWSKey+`"}`)
			abs := filepath.Join(ledger, rel)
			if mode == "deleted later commit" {
				require.NoError(t, os.Remove(abs))
			} else {
				require.NoError(t, os.WriteFile(abs, []byte(`{"content":"ordinary notes"}`), 0600))
			}
			if mode != "clean worktree" {
				gitInRepo(t, ledger, "add", "--", rel)
				gitInRepo(t, ledger, "commit", "-m", "redact synthetic session")
			}
			require.ErrorContains(t, ValidateSessionPublication(context.Background(), ledger), "committed session content refused")
		})
	}
}

func TestCommittedSessionScanFreshnessAndPositiveControl(t *testing.T) {
	useFreshFixtureRules(t)
	ledger := publicationLedger(t, "sessions/synthetic/raw.jsonl", `{"content":"ordinary notes"}`)
	require.NoError(t, ValidateSessionPublication(context.Background(), ledger))
	freshSessionPublicationRules = func(context.Context) (*secretguard.RuleSet, error) { return nil, errors.New("catalog unavailable") }
	require.ErrorContains(t, ValidateSessionPublication(context.Background(), ledger), "current session rules required")
}

func TestCommittedSessionScanRejectsNewUpstreamFormat(t *testing.T) {
	useFreshFixtureRules(t)
	rules, err := secretguard.NewRules([]byte("[[rules]]\nid='new-format'\nregex='future_[a-z]+'\n"))
	require.NoError(t, err)
	freshSessionPublicationRules = func(context.Context) (*secretguard.RuleSet, error) { return rules, nil }
	ledger := publicationLedger(t, "sessions/synthetic/summary.md", "future_synthetic")
	require.ErrorContains(t, ValidateSessionPublication(context.Background(), ledger), "current rules")
}

func publicationLedger(t *testing.T, rel, content string) string {
	t.Helper()
	ledger, _ := initBareRemoteRepo(t)
	require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(ledger, rel)), 0700))
	addCommit(t, ledger, rel, content, "synthetic session")
	return ledger
}

func TestPushWithRetryCannotOmitSessionSecretGuard(t *testing.T) {
	for _, withCallback := range []bool{false, true} {
		t.Run(map[bool]string{false: "no optional callback", true: "permissive callback"}[withCallback], func(t *testing.T) {
			useFreshFixtureRules(t)
			ledger, bare := initBareRemoteRepo(t)
			remoteBefore := gitInRepo(t, bare, "rev-parse", "HEAD")
			rel := "sessions/synthetic/raw.jsonl"
			require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(ledger, rel)), 0700))
			addCommit(t, ledger, rel, `{"content":"`+syntheticAWSKey+`"}`, "synthetic import")
			opts := PushOpts{}
			if withCallback {
				opts.ValidatePush = func(context.Context, string) (bool, error) { return false, nil }
			}
			require.ErrorContains(t, PushWithRetry(context.Background(), ledger, opts), "committed session content refused")
			require.Equal(t, remoteBefore, gitInRepo(t, bare, "rev-parse", "HEAD"), "the remote must never receive a refused blob")
		})
	}
}

func TestPushWithRetryCurrentRulesUnavailableKeepsSessionLocal(t *testing.T) {
	useFreshFixtureRules(t)
	ledger, bare := initBareRemoteRepo(t)
	remoteBefore := gitInRepo(t, bare, "rev-parse", "HEAD")
	rel := "sessions/synthetic/raw.jsonl"
	require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(ledger, rel)), 0700))
	addCommit(t, ledger, rel, `{"content":"ordinary notes"}`, "synthetic import")
	freshSessionPublicationRules = func(context.Context) (*secretguard.RuleSet, error) { return nil, errors.New("upstream unavailable") }
	require.ErrorContains(t, PushWithRetry(context.Background(), ledger, PushOpts{}), "current session rules required")
	require.Equal(t, remoteBefore, gitInRepo(t, bare, "rev-parse", "HEAD"))
	_, err := os.Stat(filepath.Join(ledger, rel))
	require.NoError(t, err, "pending content must stay available")
}

func TestCommittedSessionScanIncludesMergedDeletedSideHistory(t *testing.T) {
	useFreshFixtureRules(t)
	ledger, bare := initBareRemoteRepo(t)
	remoteBefore := gitInRepo(t, bare, "rev-parse", "HEAD")
	main := gitInRepo(t, ledger, "branch", "--show-current")
	gitInRepo(t, ledger, "checkout", "-b", "synthetic-side")
	rel := "sessions/synthetic/raw.jsonl"
	require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(ledger, rel)), 0700))
	addCommit(t, ledger, rel, `{"content":"`+syntheticAWSKey+`"}`, "synthetic side capture")
	gitInRepo(t, ledger, "rm", "--", rel)
	gitInRepo(t, ledger, "commit", "-m", "remove side capture")
	gitInRepo(t, ledger, "checkout", main)
	addCommit(t, ledger, "ordinary.txt", "ordinary notes", "main change")
	gitInRepo(t, ledger, "merge", "--no-ff", "synthetic-side", "-m", "merge side history")
	require.ErrorContains(t, PushWithRetry(context.Background(), ledger, PushOpts{}), "committed session content refused")
	require.Equal(t, remoteBefore, gitInRepo(t, bare, "rev-parse", "HEAD"))
}

func TestCommittedSessionScanChecksEveryFilenameForSharedBlob(t *testing.T) {
	useFreshFixtureRules(t)
	rules, err := secretguard.NewRules([]byte("[[rules]]\nid='sensitive-extension'\npath='\\.p12$'\n"))
	require.NoError(t, err)
	freshSessionPublicationRules = func(context.Context) (*secretguard.RuleSet, error) { return rules, nil }
	ledger, bare := initBareRemoteRepo(t)
	remoteBefore := gitInRepo(t, bare, "rev-parse", "HEAD")
	for _, rel := range []string{"sessions/synthetic/ordinary.txt", "sessions/synthetic/archive.p12"} {
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(ledger, rel)), 0700))
		addCommit(t, ledger, rel, "same benign-looking bytes", "synthetic filename")
	}
	require.ErrorContains(t, PushWithRetry(context.Background(), ledger, PushOpts{}), "filename refused")
	require.Equal(t, remoteBefore, gitInRepo(t, bare, "rev-parse", "HEAD"))
}

// A sensitive filename must be refused even when its blob is already reachable
// from upstream: rev-list then reports no new blob, so a filename check gated on
// "some blob was scanned" never runs.
// Failure prevented: renaming or copying an already-published (for example
// empty) file to a credential-bearing name publishes the name unchecked.
func TestCommittedSessionScanChecksFilenamesWhenNoBlobIsNew(t *testing.T) {
	useFreshFixtureRules(t)
	ledger, _ := initBareRemoteRepo(t)
	require.NoError(t, os.MkdirAll(filepath.Join(ledger, "sessions", "synthetic"), 0700))
	addCommit(t, ledger, "sessions/synthetic/notes.txt", "", "empty placeholder")
	gitInRepo(t, ledger, "push", "--quiet")
	require.NoError(t, ValidateSessionPublication(context.Background(), ledger), "control: nothing unpushed is sensitive")

	addCommit(t, ledger, "sessions/synthetic/archive.p12", "", "same empty blob, sensitive name")
	require.ErrorContains(t, ValidateSessionPublication(context.Background(), ledger), "session filename refused")
}

func TestCommittedSessionScanAcceptsProducedCommits(t *testing.T) {
	useFreshFixtureRules(t)
	ledger := publicationLedger(t, "sessions/synthetic/meta.json", `{"produced_commits":["`+syntheticCommitSHA+`"]}`)
	require.NoError(t, ValidateSessionPublication(context.Background(), ledger))

	leaked := publicationLedger(t, "sessions/synthetic/meta.json", `{"produced_commits":["`+syntheticCommitSHA+`","`+syntheticAWSKey+`"]}`)
	require.ErrorContains(t, ValidateSessionPublication(context.Background(), leaked), "committed session content refused")
}

// rev-list names one path per blob; the meta.json exemption must not reach the
// same bytes published under another session filename. The current catalog
// gates its bare-hex rule on keywords, so the fresh rules add a keyword-less
// one, as a later upstream catalog could.
func TestCommittedSessionScanSharedBlobKeepsFullScan(t *testing.T) {
	data, err := os.ReadFile("../secretguard/gitleaks.toml")
	require.NoError(t, err)
	rules, err := secretguard.NewRules(append(data, "\n[[rules]]\nid = \"future-bare-hex\"\nregex = '''\\b[a-f0-9]{40}\\b'''\n"...))
	require.NoError(t, err)
	original := freshSessionPublicationRules
	freshSessionPublicationRules = func(context.Context) (*secretguard.RuleSet, error) { return rules, nil }
	t.Cleanup(func() { freshSessionPublicationRules = original })

	exempt, _ := initBareRemoteRepo(t)
	require.NoError(t, os.MkdirAll(filepath.Join(exempt, "sessions", "synthetic"), 0700))
	addCommit(t, exempt, "sessions/synthetic/meta.json", `{"produced_commits":["`+syntheticCommitSHA+`"]}`, "synthetic meta")
	require.NoError(t, ValidateSessionPublication(context.Background(), exempt))

	ledger, _ := initBareRemoteRepo(t)
	content := `{"produced_commits":["` + syntheticCommitSHA + `"]}`
	for _, rel := range []string{"sessions/synthetic/meta.json", "sessions/synthetic/summary.json"} {
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(ledger, rel)), 0700))
		addCommit(t, ledger, rel, content, "synthetic shared blob")
	}
	require.ErrorContains(t, ValidateSessionPublication(context.Background(), ledger), "committed session content refused")
}

// A current catalog is the bundled one plus what upstream added since; after
// the bundled check, publication validates only the additions.
// Failure prevented: that narrower second check drops a rule the bundled
// catalog lacks, and a credential format or sensitive filename added after
// this release reaches the Ledger.
func TestCommittedSessionScanRefusesWhatOnlyCurrentRulesCatch(t *testing.T) {
	data, err := os.ReadFile("../secretguard/gitleaks.toml")
	require.NoError(t, err)
	rules, err := secretguard.NewRules(append(data, "\n[[rules]]\nid = \"future-format\"\nregex = '''future_[a-z]{8}'''\n[[rules]]\nid = \"future-file\"\npath = '''\\.futurekey$'''\n"...))
	require.NoError(t, err)
	original := freshSessionPublicationRules
	freshSessionPublicationRules = func(context.Context) (*secretguard.RuleSet, error) { return rules, nil }
	t.Cleanup(func() { freshSessionPublicationRules = original })

	require.NoError(t, ValidateSessionPublication(context.Background(), publicationLedger(t, "sessions/synthetic/summary.md", "ordinary notes")))
	for _, tc := range []struct{ rel, content, refusal string }{
		{"sessions/synthetic/summary.md", "ordinary notes, then future_qzxwvjkp", "committed session content refused by current rules"},
		{"sessions/synthetic/notes.futurekey", "ordinary notes", "session filename refused by current rules"},
	} {
		ledger := publicationLedger(t, tc.rel, tc.content)
		require.ErrorContains(t, ValidateSessionPublication(context.Background(), ledger), tc.refusal)
	}
}
