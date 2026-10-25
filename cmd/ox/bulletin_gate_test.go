package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/flags"
)

// bulletinGateFixture snapshots the process-wide flag state and the
// registration of every command syncFeatureGatedCommands touches, and restores
// both when the test ends — the same discipline TestAttestOnlyExposesPublication
// follows. It also sandboxes the home/config/cache directories so rendering the
// real root help (which probes login state per entry) reads no real credential
// and never reaches the network.
func bulletinGateFixture(t *testing.T) {
	t.Helper()
	previousFlags := flagsSnapshot{flags.Get()}
	wasScoutRegistered := commandRegistered(rootCmd, scoutCmd)
	wasBulletinRegistered := commandRegistered(rootCmd, bulletinCmd)
	oldOut, oldErr := rootCmd.OutOrStdout(), rootCmd.ErrOrStderr()
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(oldOut)
		rootCmd.SetErr(oldErr)
		// Rendering `--help` leaves cobra's parsed help flag true on the
		// shared root; pflag never resets a parsed value, so a later test
		// that executes the root would get help instead of its command.
		for _, name := range []string{"help", "version"} {
			if f := rootCmd.Flags().Lookup(name); f != nil {
				_ = f.Value.Set(f.DefValue)
				f.Changed = false
			}
		}
		setCommandRegistered(rootCmd, scoutCmd, wasScoutRegistered)
		setCommandRegistered(rootCmd, bulletinCmd, wasBulletinRegistered)
		flags.Init(context.Background(), previousFlags)
	})

	sandbox := t.TempDir()
	t.Setenv("HOME", sandbox)
	t.Setenv("XDG_CONFIG_HOME", sandbox)
	t.Setenv("XDG_CACHE_HOME", sandbox)
	t.Setenv("SAGEOX_TOKEN", "")
	t.Setenv("FEATURE_SCOUT", "")
}

// bulletinGateBool returns a *bool for hand-built CLIFeatures values.
func bulletinGateBool(b bool) *bool { return &b }

// bulletinGateSettingsJSON decodes a real settings payload the way the daemon
// cache does, so a JSON null for features.bulletin goes through the actual
// decoder rather than being hand-assembled as a nil pointer.
func bulletinGateSettingsJSON(t *testing.T, features string) *flags.CLISettingsResponse {
	t.Helper()
	raw := `{"features":` + features + `,"killswitches":{},"fetched_at":"` +
		time.Now().UTC().Format(time.RFC3339) + `"}`
	var resp flags.CLISettingsResponse
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatalf("unmarshal %s: %v", raw, err)
	}
	return &resp
}

// assertBulletinGate checks the customer-observable gate on the real rootCmd:
// registration, presence in `ox --help`, and whether `ox bulletin post`
// resolves or is an unknown command.
func assertBulletinGate(t *testing.T, want bool) {
	t.Helper()
	if got := commandRegistered(rootCmd, bulletinCmd); got != want {
		t.Fatalf("bulletin registered = %v, want %v", got, want)
	}

	// brandedHelp prints straight to os.Stdout, not the cobra writer.
	help := captureStdoutForPlanCLI(t, func() {
		rootCmd.SetArgs([]string{"--help"})
		if err := rootCmd.Execute(); err != nil {
			t.Errorf("render root help: %v", err)
		}
	})
	if got := strings.Contains(help, "bulletin"); got != want {
		t.Fatalf("`ox --help` mentions bulletin = %v, want %v:\n%s", got, want, help)
	}

	if want {
		found, rest, err := rootCmd.Find([]string{"bulletin", "post"})
		if err != nil || found != bulletinPostCmd || len(rest) != 0 {
			t.Fatalf("lookup command=%v args=%v err=%v, want bulletinPostCmd", found, rest, err)
		}
		return
	}
	rootCmd.SetArgs([]string{"bulletin", "post", "x", "--ttl", "1d"})
	if err := rootCmd.Execute(); err == nil || !strings.Contains(err.Error(), "unknown command") {
		t.Fatalf("removed command execution error = %v, want unknown command", err)
	}
}

// TestBulletinGate_ServerDecidesRegistration drives the real startup gate —
// flags.Init from a hand-built daemon cache, then syncFeatureGatedCommands on
// the real rootCmd — for the five states the cache can be in. Hidden is not
// enough (a hidden command still runs if typed) and a RunE guard is not enough
// (a run-time guard still shows in help), so the assertion is on registration:
// in `ox --help` and resolvable only when the server said true.
//
// Failure prevented: someone re-adding rootCmd.AddCommand(bulletinCmd) in
// init(), keying the gate on the wrong flag, or letting a JSON null (team
// service token), an empty cache, or a stale cache from a dead daemon keep the
// pilot command alive for a person the server never enrolled.
func TestBulletinGate_ServerDecidesRegistration(t *testing.T) {
	if testing.Short() {
		t.Skip("short: renders the real root help five times, each probing repo and login state per entry")
	}
	tests := []struct {
		name  string
		cache func(t *testing.T) *flags.CLISettingsResponse
		want  bool
	}{
		{
			name: "true registers the command",
			cache: func(*testing.T) *flags.CLISettingsResponse {
				return &flags.CLISettingsResponse{
					Features:  flags.CLIFeatures{Bulletin: bulletinGateBool(true)},
					FetchedAt: time.Now(),
				}
			},
			want: true,
		},
		{
			name: "false removes the command",
			cache: func(*testing.T) *flags.CLISettingsResponse {
				return &flags.CLISettingsResponse{
					Features:  flags.CLIFeatures{Bulletin: bulletinGateBool(false)},
					FetchedAt: time.Now(),
				}
			},
			want: false,
		},
		{
			name: "JSON null (team service token) removes the command",
			cache: func(t *testing.T) *flags.CLISettingsResponse {
				return bulletinGateSettingsJSON(t, `{"bulletin":null}`)
			},
			want: false,
		},
		{
			name:  "no cache yet removes the command",
			cache: func(*testing.T) *flags.CLISettingsResponse { return nil },
			want:  false,
		},
		{
			name: "stale cache older than twice the max age removes the command",
			cache: func(*testing.T) *flags.CLISettingsResponse {
				return &flags.CLISettingsResponse{
					Features:  flags.CLIFeatures{Bulletin: bulletinGateBool(true)},
					FetchedAt: time.Now().Add(-2*flags.CLISettingsMaxAge - time.Minute),
				}
			},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bulletinGateFixture(t)
			flags.Init(context.Background(), flags.DaemonProvider{CachedSettings: tt.cache(t)})
			syncFeatureGatedCommands(rootCmd)
			assertBulletinGate(t, tt.want)
		})
	}
}

// TestBulletinGate_EnvCannotEnable proves there is no local override: with
// FEATURE_BULLETIN=true in the environment, the command stays unregistered
// whether the server has no opinion yet or has said false. This is the same
// startup layering the CLI uses (daemon cache, then env).
//
// Failure prevented: the env-var pattern scout uses quietly becoming a way to
// bypass server enrollment for a pilot the server is supposed to control.
func TestBulletinGate_EnvCannotEnable(t *testing.T) {
	if testing.Short() {
		t.Skip("short: renders the real root help, which probes repo and login state per entry")
	}
	tests := []struct {
		name  string
		cache *flags.CLISettingsResponse
	}{
		{name: "no server opinion", cache: nil},
		{
			name: "server said false",
			cache: &flags.CLISettingsResponse{
				Features:  flags.CLIFeatures{Bulletin: bulletinGateBool(false)},
				FetchedAt: time.Now(),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bulletinGateFixture(t)
			t.Setenv("FEATURE_BULLETIN", "true")
			flags.Init(context.Background(), flags.DaemonProvider{CachedSettings: tt.cache}, flags.EnvProvider{})
			if flags.Get().BulletinEnabled {
				t.Fatal("FEATURE_BULLETIN=true resolved the pilot flag to true; the env provider must have no opinion")
			}
			syncFeatureGatedCommands(rootCmd)
			assertBulletinGate(t, false)
		})
	}
}

// TestBulletinGate_RegistrationFastTier is the fast-tier guard for the same
// gate: registration and command lookup only, no help render, so it runs on
// every commit. The two tests above additionally prove the customer-visible
// surface (`ox --help`, "unknown command") and run in the full tier.
//
// Failure prevented: the gate regressing between full-tier runs — an
// unconditional AddCommand or a flag mix-up would otherwise only be caught by
// the skipped tests.
func TestBulletinGate_RegistrationFastTier(t *testing.T) {
	tests := []struct {
		name  string
		cache *flags.CLISettingsResponse
		want  bool
	}{
		{name: "true", cache: &flags.CLISettingsResponse{Features: flags.CLIFeatures{Bulletin: bulletinGateBool(true)}, FetchedAt: time.Now()}, want: true},
		{name: "false", cache: &flags.CLISettingsResponse{Features: flags.CLIFeatures{Bulletin: bulletinGateBool(false)}, FetchedAt: time.Now()}, want: false},
		{name: "null", cache: nil, want: false},
		{name: "stale", cache: &flags.CLISettingsResponse{Features: flags.CLIFeatures{Bulletin: bulletinGateBool(true)}, FetchedAt: time.Now().Add(-2*flags.CLISettingsMaxAge - time.Minute)}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bulletinGateFixture(t)
			if tt.name == "null" {
				tt.cache = bulletinGateSettingsJSON(t, `{"bulletin":null}`)
			}
			flags.Init(context.Background(), flags.DaemonProvider{CachedSettings: tt.cache})
			syncFeatureGatedCommands(rootCmd)
			if got := commandRegistered(rootCmd, bulletinCmd); got != tt.want {
				t.Fatalf("bulletin registered = %v, want %v", got, tt.want)
			}
			found, rest, err := rootCmd.Find([]string{"bulletin", "post"})
			if tt.want {
				if err != nil || found != bulletinPostCmd || len(rest) != 0 {
					t.Fatalf("lookup command=%v args=%v err=%v, want bulletinPostCmd", found, rest, err)
				}
				return
			}
			if err == nil && found == bulletinPostCmd {
				t.Fatalf("removed command still resolves: %v", found)
			}
		})
	}
}
