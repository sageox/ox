package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// TestBuildExportOutput_CarriesPhilosophyAndGuidance verifies the JSON payload
// always carries the ownership philosophy and agent guidance, and preserves the
// ledger/team data it is handed. This is the contract an AI coworker consumes.
func TestBuildExportOutput_CarriesPhilosophyAndGuidance(t *testing.T) {
	ledgers := []exportLedger{
		{RepoID: "repo_abc", Path: "/data/sageox/example/ledgers/repo_abc", Symlink: ".sageox/ledger", Primary: true},
	}
	teams := []exportTeamContext{
		{TeamID: "team_1", Name: "Acme", Slug: "acme", Path: "/data/sageox/example/teams/team_1", Primary: true},
	}

	out := buildExportOutput(ledgers, teams, true)

	// round-trip through JSON to prove the tags/shape are stable for consumers
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal export output: %v", err)
	}
	var decoded exportOutput
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal export output: %v", err)
	}

	if !strings.Contains(decoded.Philosophy, "walled garden") {
		t.Errorf("philosophy missing walled-garden framing: %q", decoded.Philosophy)
	}
	if decoded.Guidance == "" {
		t.Error("guidance must be non-empty so AI coworkers know how to reach the data")
	}
	if !decoded.Synced {
		t.Error("synced flag not preserved")
	}
	if len(decoded.Ledgers) != 1 || decoded.Ledgers[0].Path == "" {
		t.Fatalf("ledger path not preserved: %+v", decoded.Ledgers)
	}
	if !decoded.Ledgers[0].Primary {
		t.Error("primary ledger flag not preserved")
	}
	if len(decoded.TeamContexts) != 1 || decoded.TeamContexts[0].Path == "" {
		t.Fatalf("team context path not preserved: %+v", decoded.TeamContexts)
	}
}

// TestBuildExportOutput_ReportsCompletedSyncNotRequested verifies the payload
// reports the sync that ACTUALLY completed. runExport passes syncSucceeded
// (true only when runExportSync returned nil), so a failed `--sync` refresh must
// surface synced=false rather than claiming success.
func TestBuildExportOutput_ReportsCompletedSyncNotRequested(t *testing.T) {
	// sync requested but failed → caller passes false → payload must not lie
	failed := buildExportOutput(nil, nil, false)
	if failed.Synced {
		t.Error("synced must be false when the refresh did not complete")
	}
	if failed.Note == "" {
		t.Error("note should still explain --sync scope")
	}

	// sync completed → true is preserved
	ok := buildExportOutput(nil, nil, true)
	if !ok.Synced {
		t.Error("synced must be true when the refresh completed")
	}
}

// TestRenderExportHuman_TeachesLocationAndAccess verifies the human output
// leads with the ownership philosophy, shows each repo's on-disk location, and
// teaches plain-git access — the whole point of the command.
func TestRenderExportHuman_TeachesLocationAndAccess(t *testing.T) {
	ledgers := []exportLedger{
		{RepoID: "repo_abc", Path: "/data/sageox/example/ledgers/repo_abc", Symlink: ".sageox/ledger", Primary: true},
	}
	teams := []exportTeamContext{
		{TeamID: "team_1", Name: "Acme", Slug: "acme", Path: "/data/sageox/example/teams/team_1", Primary: true},
	}

	var buf bytes.Buffer
	renderExportHuman(&buf, ledgers, teams, false)
	got := stripANSI(buf.String())

	wants := []string{
		"not a walled garden",        // philosophy (body)
		"WHERE YOUR DATA LIVES",      // location section header (RenderCategory uppercases)
		".sageox/ledger",             // ledger short path shown
		"/data/sageox/example/teams", // team context path shown
		"IT'S JUST GIT",              // access section header
		"cd .sageox/ledger",          // access example uses the short symlink
		"git pull",                   // plain-git access example
		"ox teams",                   // related command
		"ox export --sync",           // sync tip (shown when not synced)
	}
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("human output missing %q\n---\n%s", w, got)
		}
	}
}

// TestRenderExportHuman_EmptyState guides an un-set-up user rather than showing
// a blank block.
func TestRenderExportHuman_EmptyState(t *testing.T) {
	var buf bytes.Buffer
	renderExportHuman(&buf, nil, nil, false)
	got := stripANSI(buf.String())

	if !strings.Contains(got, "ox export --sync") {
		t.Errorf("empty ledger state should hint --sync:\n%s", got)
	}
	if !strings.Contains(got, "ox login") {
		t.Errorf("empty team state should hint login/init:\n%s", got)
	}
}
