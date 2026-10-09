package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/session/cursorpaths"
	"github.com/sageox/ox/pkg/adapterprotocol"
)

func cursorDiagnoseFixture(t *testing.T) (string, string, cursorDiagnoseDeps) {
	t.Helper()
	base := t.TempDir()
	home := filepath.Join(base, "home")
	repo := filepath.Join(base, "repo")
	for _, directory := range []string{home, repo} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	deps := defaultCursorDiagnoseDeps()
	deps.homeDir = func() (string, error) { return home, nil }
	deps.findOx = func() (string, error) { return filepath.Join(base, "ox"), nil }
	return home, repo, deps
}

func cursorIssue(issues []adapterprotocol.DiagnoseIssue, slug string) *adapterprotocol.DiagnoseIssue {
	for index := range issues {
		if issues[index].Slug == slug {
			return &issues[index]
		}
	}
	return nil
}

func createCursorTranscriptRoot(t *testing.T, home, repo string, deps cursorDiagnoseDeps) string {
	t.Helper()
	probe, err := deps.sessionPath(home, repo, cursorDiagnosticProbeConversation)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Dir(filepath.Dir(probe))
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestCursorDiagnoseDistinguishesNoSessionYetFromFailure(t *testing.T) {
	_, repo, deps := cursorDiagnoseFixture(t)
	result := diagnoseCursor(adapterprotocol.DiagnoseParams{RepoRoot: repo, Scope: "project"}, deps)

	issue := cursorIssue(result.Issues, "source-not-found")
	if issue == nil {
		t.Fatalf("issues = %+v, want source-not-found", result.Issues)
	}
	if issue.Severity != "info" || !strings.Contains(issue.Detail, "missing hook path hint alone") {
		t.Fatalf("source-not-found issue = %+v", *issue)
	}
	if strings.Contains(strings.ToLower(issue.Detail), "export disabled") {
		t.Fatalf("detail incorrectly equates a missing hint with disabled export: %q", issue.Detail)
	}
}

func TestCursorDiagnoseReportsMissingOxWithoutLeakingLookupError(t *testing.T) {
	home, repo, deps := cursorDiagnoseFixture(t)
	createCursorTranscriptRoot(t, home, repo, deps)
	secret := "private-user-path-and-token"
	deps.findOx = func() (string, error) { return "", errors.New(secret) }

	result := diagnoseCursor(adapterprotocol.DiagnoseParams{RepoRoot: repo, Scope: "project"}, deps)
	issue := cursorIssue(result.Issues, "adapter-missing")
	if issue == nil || !strings.Contains(issue.Title, "ox executable") {
		t.Fatalf("issues = %+v, want missing ox executable", result.Issues)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), secret) {
		t.Fatalf("diagnosis leaked private lookup error: %s", encoded)
	}
}

func TestCursorDiagnoseHookStatesAndSafeRepair(t *testing.T) {
	home, repo, deps := cursorDiagnoseFixture(t)
	createCursorTranscriptRoot(t, home, repo, deps)

	missing := diagnoseCursor(adapterprotocol.DiagnoseParams{RepoRoot: repo, Scope: "project"}, deps)
	issue := cursorIssue(missing.Issues, "hooks-missing")
	if issue == nil {
		t.Fatalf("missing issues = %+v", missing.Issues)
	}
	wantArgv := []string{"ox", "integrate", "install", "--cursor"}
	if !issue.FixSafe || strings.Join(issue.FixArgv, "\x00") != strings.Join(wantArgv, "\x00") {
		t.Fatalf("missing hook repair = %+v, want safe argv %v", *issue, wantArgv)
	}

	hooksPath := filepath.Join(repo, ".cursor", "hooks.json")
	if err := os.MkdirAll(filepath.Dir(hooksPath), 0o755); err != nil {
		t.Fatal(err)
	}
	original := `{
  "version": 1,
  "opaque": {"large": 123456789012345678901234567890},
  "hooks": {
    "sessionStart": [{"type":"prompt","prompt":"Keep this prompt exactly: \\u2603","timeout":10,"model":"cursor-small"}],
    "futureEvent": [{"type":"prompt","prompt":"future","custom":{"preserve":true}}]
  }
}`
	if err := os.WriteFile(hooksPath, []byte(original), 0o640); err != nil {
		t.Fatal(err)
	}

	stale := diagnoseCursor(adapterprotocol.DiagnoseParams{RepoRoot: repo, Scope: "project"}, deps)
	issue = cursorIssue(stale.Issues, "hooks-invalid")
	if issue == nil || !issue.FixSafe {
		t.Fatalf("stale issues = %+v, want safely repairable hooks-invalid", stale.Issues)
	}

	if _, err := handleInstallHooks(adapterprotocol.HookParams{RepoRoot: repo, Scope: "project"}); err != nil {
		t.Fatalf("install hooks: %v", err)
	}
	rechecked := diagnoseCursor(adapterprotocol.DiagnoseParams{RepoRoot: repo, Scope: "project"}, deps)
	if cursorIssue(rechecked.Issues, "hooks-missing") != nil || cursorIssue(rechecked.Issues, "hooks-invalid") != nil {
		t.Fatalf("recheck still reports hooks: %+v", rechecked.Issues)
	}
	if !rechecked.OK {
		t.Fatalf("recheck = %+v, want clean diagnosis", rechecked)
	}

	data, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, preserved := range []string{"123456789012345678901234567890", "Keep this prompt exactly", "cursor-small", "futureEvent", `"preserve": true`} {
		if !strings.Contains(string(data), preserved) {
			t.Errorf("installed config lost %q:\n%s", preserved, data)
		}
	}
	if info, err := os.Stat(hooksPath); err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("hooks permissions = %v, %v; want 0640", info, err)
	}
}

func TestCursorDiagnoseMalformedHooksAreNotAdvertisedAsSafeRepair(t *testing.T) {
	home, repo, deps := cursorDiagnoseFixture(t)
	createCursorTranscriptRoot(t, home, repo, deps)
	hooksPath := filepath.Join(repo, ".cursor", "hooks.json")
	if err := os.MkdirAll(filepath.Dir(hooksPath), 0o755); err != nil {
		t.Fatal(err)
	}
	secret := "native-secret-payload"
	if err := os.WriteFile(hooksPath, []byte(`{"version":1,"hooks":{"sessionStart":[`+secret), 0o600); err != nil {
		t.Fatal(err)
	}

	result := diagnoseCursor(adapterprotocol.DiagnoseParams{RepoRoot: repo, Scope: "project"}, deps)
	issue := cursorIssue(result.Issues, "hooks-invalid")
	if issue == nil {
		t.Fatalf("issues = %+v, want hooks-invalid", result.Issues)
	}
	if issue.FixSafe || len(issue.FixArgv) != 0 {
		t.Fatalf("malformed hooks repair must remain manual: %+v", *issue)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), secret) || strings.Contains(string(encoded), hooksPath) {
		t.Fatalf("diagnosis leaked hook contents or private path: %s", encoded)
	}
}

func TestCursorDiagnoseSourceFailuresAreBoundedAndPrivate(t *testing.T) {
	home, repo, deps := cursorDiagnoseFixture(t)
	root := createCursorTranscriptRoot(t, home, repo, deps)
	secret := "credential=do-not-print"
	baseStat := deps.stat
	deps.stat = func(path string) (os.FileInfo, error) {
		if path == root {
			return nil, &os.PathError{Op: "open", Path: "/Users/private/native.jsonl", Err: errors.New(secret)}
		}
		return baseStat(path)
	}

	result := diagnoseCursor(adapterprotocol.DiagnoseParams{RepoRoot: repo, Scope: "project"}, deps)
	if cursorIssue(result.Issues, "source-unreadable") == nil {
		t.Fatalf("issues = %+v, want source-unreadable", result.Issues)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{secret, "/Users/private", root} {
		if strings.Contains(string(encoded), private) {
			t.Fatalf("diagnosis leaked %q: %s", private, encoded)
		}
	}
}

func TestCursorDiagnoseRejectsSymlinkedNativeRootWithoutTargetDisclosure(t *testing.T) {
	home, repo, deps := cursorDiagnoseFixture(t)
	probe, err := cursorpaths.SessionPath(home, repo, cursorDiagnosticProbeConversation)
	if err != nil {
		t.Fatal(err)
	}
	nativeRoot := filepath.Dir(filepath.Dir(probe))
	if err := os.MkdirAll(filepath.Dir(nativeRoot), 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "private-native-target")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, nativeRoot); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	result := diagnoseCursor(adapterprotocol.DiagnoseParams{RepoRoot: repo, Scope: "project"}, deps)
	if cursorIssue(result.Issues, "invalid-source-path") == nil {
		t.Fatalf("issues = %+v, want invalid-source-path", result.Issues)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{target, nativeRoot} {
		if strings.Contains(string(encoded), private) {
			t.Fatalf("diagnosis leaked symlink path %q: %s", private, encoded)
		}
	}
}

func TestCursorDiagnoseRejectsUnsupportedScopeAndWorkspace(t *testing.T) {
	_, repo, deps := cursorDiagnoseFixture(t)
	for _, tc := range []struct {
		name   string
		params adapterprotocol.DiagnoseParams
		slug   string
	}{
		{name: "user scope", params: adapterprotocol.DiagnoseParams{RepoRoot: repo, Scope: "user"}, slug: "unsupported-scope"},
		{name: "relative workspace", params: adapterprotocol.DiagnoseParams{RepoRoot: "relative", Scope: "project"}, slug: "workspace-mismatch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := diagnoseCursor(tc.params, deps)
			if result.OK || cursorIssue(result.Issues, tc.slug) == nil {
				t.Fatalf("result = %+v, want %s", result, tc.slug)
			}
		})
	}
}
