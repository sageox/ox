package filesmount

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

const testTeamID = "team_abc"

// writeDrive lays out a mount: a sentinel naming the teams, and a folder for
// each team that claims one.
func writeDrive(t *testing.T, root string, sentinel Sentinel, teamFolders ...string) {
	t.Helper()
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(sentinel)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, SentinelName), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, folder := range teamFolders {
		if err := os.MkdirAll(filepath.Join(root, folder), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func validSentinel() Sentinel {
	return Sentinel{
		SentinelVersion: supportedSentinelVersion,
		Product:         productSageOxFiles,
		ReadOnly:        true,
		Teams: []Team{{
			TeamID:      testTeamID,
			DisplayName: "SageOx Internal",
			Path:        "SageOx Internal",
		}},
	}
}

// homeWithDrive points HOME at a scratch directory holding one mount, reached
// through the ~/SageOx shortcut the Files app maintains.
func homeWithDrive(t *testing.T, sentinel Sentinel, folders ...string) (home, drive string) {
	t.Helper()
	home = t.TempDir()
	drive = filepath.Join(home, "Library", "CloudStorage", "SageOxFiles-SageOxFiles")
	writeDrive(t, drive, sentinel, folders...)
	if err := os.Symlink(drive, filepath.Join(home, "SageOx")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	return home, drive
}

func TestDiscoverFindsADriveThroughTheShortcut(t *testing.T) {
	_, drive := homeWithDrive(t, validSentinel(), "SageOx Internal")

	mounts := Discover(context.Background())
	if len(mounts) != 1 {
		t.Fatalf("discovered %d mounts, want 1", len(mounts))
	}
	if mounts[0].Root != resolved(t, drive) {
		t.Errorf("root = %q, want %q", mounts[0].Root, drive)
	}
}

// ~/SageOx normally points into CloudStorage, so both candidates name the same
// drive. Reporting it twice would double every downstream lookup.
func TestDiscoverReportsOneDriveReachedTwoWays(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("CloudStorage is scanned only on macOS")
	}
	homeWithDrive(t, validSentinel(), "SageOx Internal")

	if mounts := Discover(context.Background()); len(mounts) != 1 {
		t.Fatalf("discovered %d mounts, want 1", len(mounts))
	}
}

// A drive found only by scanning CloudStorage still counts: the shortcut is a
// convenience the app may not have been able to create.
func TestDiscoverFindsADriveWithoutTheShortcut(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("CloudStorage is scanned only on macOS")
	}
	home := t.TempDir()
	drive := filepath.Join(home, "Library", "CloudStorage", "SageOxFiles-Acme")
	writeDrive(t, drive, validSentinel(), "SageOx Internal")
	t.Setenv("HOME", home)

	mounts := Discover(context.Background())
	if len(mounts) != 1 {
		t.Fatalf("discovered %d mounts, want 1", len(mounts))
	}
}

// Other cloud providers live in the same directory. Only the marker file makes
// a folder ours.
func TestDiscoverIgnoresOtherCloudFolders(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("CloudStorage is scanned only on macOS")
	}
	home := t.TempDir()
	for _, name := range []string{"Dropbox", "GoogleDrive-someone", "iCloud Drive"} {
		if err := os.MkdirAll(filepath.Join(home, "Library", "CloudStorage", name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)

	if mounts := Discover(context.Background()); len(mounts) != 0 {
		t.Fatalf("discovered %d mounts in a home with no SageOx drive", len(mounts))
	}
}

func TestDiscoverRejectsAnotherProductsMarker(t *testing.T) {
	sentinel := validSentinel()
	sentinel.Product = "some-other-drive"
	homeWithDrive(t, sentinel, "SageOx Internal")

	if mounts := Discover(context.Background()); len(mounts) != 0 {
		t.Fatal("a marker naming another product was accepted as a SageOx drive")
	}
}

// A newer publisher bumps the version only for a change this build cannot
// absorb, so guessing at it is worse than reporting no mount.
func TestDiscoverDeclinesAFutureSentinelVersion(t *testing.T) {
	sentinel := validSentinel()
	sentinel.SentinelVersion = supportedSentinelVersion + 1
	homeWithDrive(t, sentinel, "SageOx Internal")

	if mounts := Discover(context.Background()); len(mounts) != 0 {
		t.Fatal("a sentinel from a newer publisher was accepted")
	}
}

func TestDiscoverRejectsAMalformedSentinel(t *testing.T) {
	home := t.TempDir()
	drive := filepath.Join(home, "SageOx")
	if err := os.MkdirAll(drive, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(drive, SentinelName), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)

	if mounts := Discover(context.Background()); len(mounts) != 0 {
		t.Fatal("a malformed sentinel was accepted")
	}
}

func TestDiscoverOnAMachineWithNoDrive(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	if mounts := Discover(context.Background()); len(mounts) != 0 {
		t.Fatalf("discovered %d mounts on a bare home", len(mounts))
	}
}

func TestFindTeamResolvesTheTeamFolder(t *testing.T) {
	_, drive := homeWithDrive(t, validSentinel(), "SageOx Internal")

	path, ok := FindTeam(context.Background(), testTeamID)
	if !ok {
		t.Fatal("the mounted team was not found")
	}
	if want := filepath.Join(resolved(t, drive), "SageOx Internal"); path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
}

func TestFindTeamReportsATeamTheDriveDoesNotCarry(t *testing.T) {
	homeWithDrive(t, validSentinel(), "SageOx Internal")

	if _, ok := FindTeam(context.Background(), "team_elsewhere"); ok {
		t.Fatal("a team absent from the sentinel was reported as mounted")
	}
}

// The sentinel lists the folder, but the folder is what gets read. Claiming a
// path that is not there would send a caller into a missing directory.
func TestFindTeamRequiresTheFolderToExist(t *testing.T) {
	homeWithDrive(t, validSentinel()) // sentinel lists the team, no folder on disk

	if _, ok := FindTeam(context.Background(), testTeamID); ok {
		t.Fatal("a team folder that does not exist was reported as mounted")
	}
}

// The sentinel is remote data. A path that climbs out of the mount would turn a
// team lookup into a read of somewhere else on the machine.
func TestFindTeamRefusesAPathThatEscapesTheMount(t *testing.T) {
	for _, escape := range []string{
		"../../../etc",
		"..",
		"SageOx Internal/../../elsewhere",
	} {
		t.Run(escape, func(t *testing.T) {
			sentinel := validSentinel()
			sentinel.Teams[0].Path = escape
			home, _ := homeWithDrive(t, sentinel)
			// Make the escape target real, so only the bounds check can reject it.
			if err := os.MkdirAll(filepath.Join(home, "elsewhere"), 0o755); err != nil {
				t.Fatal(err)
			}

			if path, ok := FindTeam(context.Background(), testTeamID); ok {
				t.Fatalf("escaping path %q resolved to %q", escape, path)
			}
		})
	}
}

func TestFindTeamRefusesAnAbsolutePath(t *testing.T) {
	sentinel := validSentinel()
	sentinel.Teams[0].Path = os.TempDir()
	homeWithDrive(t, sentinel)

	if _, ok := FindTeam(context.Background(), testTeamID); ok {
		t.Fatal("an absolute team path was accepted")
	}
}

func TestTeamPathIgnoresAnEmptyTeamID(t *testing.T) {
	mount := Mount{Root: t.TempDir(), Sentinel: Sentinel{Teams: []Team{{TeamID: "", Path: "x"}}}}

	if _, ok := mount.TeamPath(""); ok {
		t.Fatal("an empty team id matched an entry")
	}
}

// Reads against a mount are not local: they can block on the Drive API. A
// caller's deadline has to win, or an ox command stalls on an optional
// accelerator.
func TestDiscoverHonorsACallersDeadline(t *testing.T) {
	homeWithDrive(t, validSentinel(), "SageOx Internal")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if mounts := Discover(ctx); len(mounts) != 0 {
		t.Fatal("discovery ignored a canceled context")
	}
}

func TestReadFileWithinReturnsTheDeadlineError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(path, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := readFileWithin(ctx, path, time.Second); err == nil {
		t.Fatal("a canceled read reported success")
	}
}

func resolved(t *testing.T, path string) string {
	t.Helper()
	out, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
