package runtime

import (
	"testing"
)

// clearEnv unsets every env var Probe consults so each subtest starts
// from a clean baseline. t.Setenv handles restoration. We don't touch
// HOME — the PersistDisk probe needs a real path to stat, and
// HOME=/some/missing/dir would make the laptop-default test ambiguous.
func clearEnv(t *testing.T) {
	t.Helper()
	vars := []string{
		"OX_EPHEMERAL",
		"CLAUDE_CODE_REMOTE",
		"DEVIN_TASK_ID",
		"CODESPACES",
		"CI",
		"GITHUB_ACTIONS",
		envPersistDisk,
		envNoDaemon,
		envBrowser,
		envNetwork,
		envOffline,
	}
	for _, v := range vars {
		t.Setenv(v, "")
	}
}

func TestProbe_LaptopDefault(t *testing.T) {
	clearEnv(t)
	c := Probe()
	if !c.PersistDisk {
		t.Errorf("laptop default: PersistDisk should be true")
	}
	if !c.DaemonViable {
		t.Errorf("laptop default: DaemonViable should be true (persist=%v lifetime=%v)", c.PersistDisk, c.EnvLifetime)
	}
	if !c.TmpdirWritable {
		t.Errorf("laptop default: TmpdirWritable should be true")
	}
	if !c.Browser {
		t.Errorf("laptop default: Browser should be true")
	}
	if !c.Network {
		t.Errorf("laptop default: Network should be true")
	}
	if c.EnvLifetime != LifetimePersistent {
		t.Errorf("laptop default: EnvLifetime should be LifetimePersistent, got %v", c.EnvLifetime)
	}
}

func TestCapabilities_ZeroValueIsLaptop(t *testing.T) {
	// the zero value should read as "laptop default" — see Lifetime doc.
	// PersistDisk / DaemonViable / Network / Browser are individually
	// false-by-default in the zero struct (they're bools), but the
	// EnvLifetime zero MUST be LifetimePersistent so a default-constructed
	// Capabilities{} doesn't accidentally read as a CI runner.
	var c Capabilities
	if c.EnvLifetime != LifetimePersistent {
		t.Errorf("zero-value Capabilities.EnvLifetime should be LifetimePersistent (the zero of the iota), got %v", c.EnvLifetime)
	}
}

func TestProbe_PersistDiskOverride(t *testing.T) {
	clearEnv(t)
	t.Setenv(envPersistDisk, "0")
	c := Probe()
	if c.PersistDisk {
		t.Errorf("OX_PERSIST_DISK=0 must force PersistDisk=false")
	}
	// follow-on: DaemonViable depends on PersistDisk
	if c.DaemonViable {
		t.Errorf("with PersistDisk=false, DaemonViable must collapse to false")
	}
}

func TestProbe_NoDaemonOverride(t *testing.T) {
	clearEnv(t)
	t.Setenv(envNoDaemon, "1")
	c := Probe()
	if c.DaemonViable {
		t.Errorf("OX_NO_DAEMON=1 must force DaemonViable=false")
	}
	if !c.PersistDisk {
		t.Errorf("OX_NO_DAEMON=1 should not affect PersistDisk; got false")
	}
}

func TestProbe_BrowserOverride(t *testing.T) {
	clearEnv(t)
	t.Setenv(envBrowser, "0")
	c := Probe()
	if c.Browser {
		t.Errorf("OX_BROWSER=0 must force Browser=false")
	}
}

func TestProbe_NetworkOverride(t *testing.T) {
	cases := []struct {
		name string
		env  string
		val  string
		want bool
	}{
		{"default_online", "", "", true},
		{"network_zero", envNetwork, "0", false},
		{"network_false", envNetwork, "false", false},
		{"network_offline", envNetwork, "offline", false},
		{"network_bogus", envNetwork, "bogus", true}, // unrecognized stays online
		{"offline_one", envOffline, "1", false},
		{"offline_true", envOffline, "true", false},
		{"offline_zero", envOffline, "0", true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			clearEnv(t)
			if tc.env != "" {
				t.Setenv(tc.env, tc.val)
			}
			if got := Probe().Network; got != tc.want {
				t.Errorf("%s=%q: Network got %v, want %v", tc.env, tc.val, got, tc.want)
			}
		})
	}
}

// TestProbe_DevinPath asserts the Devin sandbox shape: hours of
// lifetime, no persistent disk (per the plan's framing — Devin snapshots
// are opaque), DaemonViable unavailable (no FS to anchor sockets).
//
// We piggyback on the OX_EPHEMERAL canary because DEVIN_TASK_ID alone
// signals the lifetime hint but not disk loss — the actual disk-loss
// signal is OX_EPHEMERAL=1, which the runtime treats as authoritative.
// In production both are set; we wire them together here.
func TestProbe_DevinPath(t *testing.T) {
	clearEnv(t)
	t.Setenv("DEVIN_TASK_ID", "t_abc")
	t.Setenv("OX_EPHEMERAL", "1")
	c := Probe()
	if c.EnvLifetime != LifetimeHours {
		t.Errorf("Devin: EnvLifetime should be LifetimeHours, got %v", c.EnvLifetime)
	}
	if c.PersistDisk {
		t.Errorf("Devin (with OX_EPHEMERAL=1): PersistDisk should be false")
	}
	if c.DaemonViable {
		t.Errorf("Devin (no persistent disk): DaemonViable should be false")
	}
}

// TestProbe_CIPath asserts the GitHub Actions runner shape: minutes-shaped
// lifetime, no browser. CI runners DO have persistent disk for the
// duration of the job, so PersistDisk stays true — but EnvLifetime is
// minutes so DaemonViable still collapses to false.
func TestProbe_CIPath(t *testing.T) {
	clearEnv(t)
	t.Setenv("CI", "true")
	t.Setenv("GITHUB_ACTIONS", "true")
	c := Probe()
	if c.EnvLifetime != LifetimeMinutes {
		t.Errorf("CI: EnvLifetime should be LifetimeMinutes, got %v", c.EnvLifetime)
	}
	if c.Browser {
		t.Errorf("CI: Browser should be false")
	}
	if c.DaemonViable {
		t.Errorf("CI: DaemonViable should be false (minutes-shaped lifetime)")
	}
	if !c.PersistDisk {
		t.Errorf("CI: PersistDisk should remain true — runners have writable FS within the job")
	}
}

func TestProbe_CodespacesPath(t *testing.T) {
	clearEnv(t)
	t.Setenv("CODESPACES", "true")
	c := Probe()
	if c.EnvLifetime != LifetimePersistent {
		t.Errorf("Codespaces: EnvLifetime should be LifetimePersistent, got %v", c.EnvLifetime)
	}
	if !c.PersistDisk {
		t.Errorf("Codespaces: PersistDisk should be true (this is the bugfix)")
	}
	if !c.DaemonViable {
		t.Errorf("Codespaces: DaemonViable should be true (the daemon should run)")
	}
}

func TestProbe_OxEphemeralCanary(t *testing.T) {
	clearEnv(t)
	t.Setenv("OX_EPHEMERAL", "1")
	c := Probe()
	// OX_EPHEMERAL is treated as authoritative for disk loss, so both
	// PersistDisk and DaemonViable must collapse.
	if c.PersistDisk {
		t.Errorf("OX_EPHEMERAL=1: PersistDisk should be false")
	}
	if c.DaemonViable {
		t.Errorf("OX_EPHEMERAL=1: DaemonViable should be false")
	}
	if c.Browser {
		t.Errorf("OX_EPHEMERAL=1: Browser should be false")
	}
}

// TestCaps_Cached asserts the sync.Once-backed cache. We Reset() in
// t.Cleanup so other tests in the package start clean. Production code
// must NEVER call Reset.
func TestCaps_Cached(t *testing.T) {
	t.Cleanup(Reset)
	Reset()
	a := Caps()
	b := Caps()
	if a != b {
		t.Errorf("Caps() should return the cached struct; got %#v vs %#v", a, b)
	}
}
