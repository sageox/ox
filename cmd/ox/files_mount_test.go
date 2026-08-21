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

// Detection is free, but moving every team-document read onto the mount is a
// deliberate flip. An upgrade must not change where context comes from.
func TestMountedTeamRootIsOptIn(t *testing.T) {
	mountHome(t, "team_abc")
	t.Setenv(filesMountEnv, "")

	if _, ok := mountedTeamRoot("team_abc"); ok {
		t.Fatal("reads moved to the mount without the flag set")
	}
	if _, ok := discoverMountedTeamRoot("team_abc"); !ok {
		t.Fatal("status could not see a mount that is present")
	}
}

func TestMountedTeamRootReadsTheMountWhenEnabled(t *testing.T) {
	mountHome(t, "team_abc")
	t.Setenv(filesMountEnv, "1")

	path, ok := mountedTeamRoot("team_abc")
	if !ok {
		t.Fatal("the flag was set but the mount was not used")
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

// Every failure has to look the same to callers: no mount, use the checkout.
func TestMountedTeamRootFallsBackWhenNothingIsMounted(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv(filesMountEnv, "1")

	if _, ok := mountedTeamRoot("team_abc"); ok {
		t.Fatal("a mount was reported on a machine with none")
	}
}
