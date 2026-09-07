package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// captureUpgradeOutput redirects os.Stdout as well as the cobra writer.
//
// The --json branch writes through cmd.OutOrStdout(); the human-readable branches
// use fmt.Printf and go straight to os.Stdout. Capturing only the cobra writer
// silently returned an empty string for every text case.
func captureUpgradeOutput(t *testing.T, result upgradeResult, jsonOut bool) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	realStdout := os.Stdout
	os.Stdout = w
	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	outErr := outputUpgradeResult(cmd, result, jsonOut)
	os.Stdout = realStdout
	_ = w.Close()
	piped, _ := io.ReadAll(r)
	if outErr != nil {
		t.Fatalf("outputUpgradeResult: %v", outErr)
	}
	return buf.String() + string(piped)
}

// TestOutputUpgradeResult_JSONCarriesTheMachineFields: `ox upgrade --json` is a
// scripted surface. Losing a field silently breaks automation that has no other
// way to tell what happened.
func TestOutputUpgradeResult_JSONCarriesTheMachineFields(t *testing.T) {
	out := captureUpgradeOutput(t, upgradeResult{
		Status:     "upgraded",
		Message:    "Upgraded to v0.15.0",
		ReleaseURL: "https://example.invalid/releases/v0.15.0",
		NewVersion: "0.15.0",
	}, true)

	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("--json did not emit valid JSON: %v\n%s", err, out)
	}
	for _, key := range []string{"status", "message", "new_version"} {
		if _, ok := got[key]; !ok {
			t.Errorf("--json output is missing %q: %v", key, got)
		}
	}
}

// TestOutputUpgradeResult_UpToDateSaysSoWithoutClaimingAnUpgrade: reporting an
// upgrade that did not happen would send someone hunting for a version change
// that was never made.
func TestOutputUpgradeResult_UpToDateSaysSoWithoutClaimingAnUpgrade(t *testing.T) {
	out := captureUpgradeOutput(t, upgradeResult{
		Status:  "up-to-date",
		Message: "Already on the latest version (v0.15.0)",
	}, false)

	if !strings.Contains(out, "Already on the latest version") {
		t.Errorf("up-to-date result did not report itself:\n%s", out)
	}
	if strings.Contains(strings.ToLower(out), "release notes") {
		t.Errorf("up-to-date result advertised release notes for an upgrade that did not happen:\n%s", out)
	}
}

// TestOutputUpgradeResult_ManualPathStillTellsTheUserWhatToDo: when ox cannot
// upgrade itself (Homebrew, a source build), silence would leave the user stuck.
func TestOutputUpgradeResult_ManualPathStillTellsTheUserWhatToDo(t *testing.T) {
	out := captureUpgradeOutput(t, upgradeResult{
		Status:     "manual",
		Message:    "Run `brew upgrade sageox/tap/ox`",
		ReleaseURL: "https://example.invalid/releases/v0.15.0",
	}, false)

	if !strings.Contains(out, "brew upgrade") {
		t.Errorf("manual path did not surface the instruction:\n%s", out)
	}
}
