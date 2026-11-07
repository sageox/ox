package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/filesmount"
)

// mountHome lays out a home directory carrying one mounted team.
func mountHome(t *testing.T, teamID string) string {
	t.Helper()
	home := t.TempDir()
	drive := filepath.Join(home, "SageOx")
	if err := os.MkdirAll(filepath.Join(drive, "SageOx Internal"), 0o755); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(filesmount.Sentinel{
		SentinelVersion: 1,
		Product:         "sageox-files",
		ReadOnly:        true,
		Teams: []filesmount.Team{{
			TeamID: teamID, DisplayName: "SageOx Internal", Path: "SageOx Internal",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(drive, filesmount.SentinelName), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	return home
}

// Detection is free, but adding a second source that context comes from is a
// deliberate flip. An upgrade must not change what an agent is primed with.
func TestMountedTeamRootIsOptIn(t *testing.T) {
	mountHome(t, "team_abc")
	t.Setenv(filesMountEnv, "")

	if _, ok := mountedTeamRoot("team_abc"); ok {
		t.Fatal("the mount joined the read sources without the flag set")
	}
	if _, ok := discoverMountedTeamRoot("team_abc"); !ok {
		t.Fatal("status could not see a mount that is present")
	}
}

func TestMountedTeamRootAddsTheMountWhenEnabled(t *testing.T) {
	mountHome(t, "team_abc")
	t.Setenv(filesMountEnv, "1")

	path, ok := mountedTeamRoot("team_abc")
	if !ok {
		t.Fatal("the flag was set but the mount did not join the read sources")
	}
	if filepath.Base(path) != "SageOx Internal" {
		t.Errorf("resolved %q, want the team folder", path)
	}
}

// A value other than "1" is not consent — the house convention is an exact
// match, and "0"/"false" must not read as enabled.
func TestMountedTeamRootIgnoresOtherFlagValues(t *testing.T) {
	mountHome(t, "team_abc")
	for _, value := range []string{"0", "false", "true", "yes"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv(filesMountEnv, value)
			if _, ok := mountedTeamRoot("team_abc"); ok {
				t.Fatalf("%q was treated as opting in", value)
			}
		})
	}
}

// Every failure has to look the same to callers: no mount, read the git
// sources alone.
func TestMountedTeamRootFallsBackWhenNothingIsMounted(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv(filesMountEnv, "1")

	if _, ok := mountedTeamRoot("team_abc"); ok {
		t.Fatal("a mount was reported on a machine with none")
	}
}

// SAGEOX_* is the canonical namespace for customer-facing environment variables
// (AGENTS.md, sageox-mono ADR-047). Pinned because the name is printed by `ox
// status` as instructions a user is expected to type: a drifted name shows them
// a variable the command does not read.
func TestFilesMountEnvUsesTheCanonicalNamespace(t *testing.T) {
	if filesMountEnv != "SAGEOX_FILES_MOUNT" {
		t.Fatalf("env var = %q, want SAGEOX_FILES_MOUNT", filesMountEnv)
	}
}

// `ox status` renders a card per team and resolves each from one scan. A drive
// discovered once has to answer for every team it carries, or the shared budget
// buys nothing.
func TestOneScanResolvesEveryTeamItCarries(t *testing.T) {
	home := t.TempDir()
	drive := filepath.Join(home, "SageOx")
	for _, folder := range []string{"Team One", "Team Two"} {
		if err := os.MkdirAll(filepath.Join(drive, folder), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := json.Marshal(filesmount.Sentinel{
		SentinelVersion: 1,
		Product:         "sageox-files",
		ReadOnly:        true,
		Teams: []filesmount.Team{
			{TeamID: "team_one", DisplayName: "Team One", Path: "Team One"},
			{TeamID: "team_two", DisplayName: "Team Two", Path: "Team Two"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(drive, filesmount.SentinelName), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)

	scan := scanFilesMounts()
	defer scan.close()

	for _, teamID := range []string{"team_one", "team_two"} {
		if _, ok := scan.teamRoot(teamID); !ok {
			t.Errorf("%s was not resolved from the shared scan", teamID)
		}
	}
	if _, ok := scan.teamRoot("team_absent"); ok {
		t.Error("a team the drive does not carry resolved anyway")
	}
}

// A closed scan must not report mounts: `ox status` closes it when the command
// ends, and a stale answer after that would be a use-after-budget.
func TestAClosedScanStopsResolving(t *testing.T) {
	mountHome(t, "team_abc")

	scan := scanFilesMounts()
	scan.close()

	if _, ok := scan.teamRoot("team_abc"); ok {
		t.Fatal("a closed scan still resolved a team")
	}
}
