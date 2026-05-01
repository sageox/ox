package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRepoIDFromLedgerPath covers the ledger-path → repo_id extractor.
//
// Why this matters: the ledger path is the *only* on-disk source of the
// canonical repo_id when .sageox/config.json has been removed by a git
// reset. If this parser silently returns the wrong path component, we
// either (a) mint a "valid-looking" but wrong repo_id (orphans the
// existing ledger checkout and the running daemon's registry entry) or
// (b) fall back to default config (same orphan outcome). Each subcase
// guards a real shape we've observed: the canonical user-dir layout
// from production, trailing slashes that filepath.Clean does NOT strip
// for some inputs, sub-paths into the ledger that callers may pass in,
// and malformed inputs that must NOT match a "looks plausible"
// fallback.
func TestRepoIDFromLedgerPath(t *testing.T) {
	tests := []struct {
		name string
		path string
		want string
	}{
		{
			name: "canonical user-dir layout",
			path: "/Users/ryan/.local/share/sageox/sageox.ai/ledgers/repo_019ddf8e-d63b-7dd3-8358-43b70ab31740",
			want: "repo_019ddf8e-d63b-7dd3-8358-43b70ab31740",
		},
		{
			name: "trailing slash tolerated",
			path: "/Users/ryan/.local/share/sageox/sageox.ai/ledgers/repo_abc-123/",
			want: "repo_abc-123",
		},
		{
			name: "with subpath after repo_id (e.g. .sageox/cache/)",
			path: "/Users/ryan/.local/share/sageox/sageox.ai/ledgers/repo_xyz/.sageox/cache",
			want: "repo_xyz",
		},
		{
			name: "non-canonical path without ledgers segment",
			path: "/tmp/random/path",
			want: "",
		},
		{
			name: "ledgers segment present but next part missing repo_ prefix",
			path: "/Users/ryan/.local/share/sageox/foo/ledgers/notarepo",
			want: "",
		},
		{
			name: "empty input",
			path: "",
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := RepoIDFromLedgerPath(tt.path); got != tt.want {
				t.Errorf("RepoIDFromLedgerPath(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestEndpointSlugFromLedgerPath(t *testing.T) {
	tests := []struct {
		name string
		path string
		want string
	}{
		{
			name: "canonical sageox.ai layout",
			path: "/Users/ryan/.local/share/sageox/sageox.ai/ledgers/repo_xyz",
			want: "sageox.ai",
		},
		{
			name: "self-hosted endpoint slug",
			path: "/home/u/.local/share/sageox/git.acme.example/ledgers/repo_xyz",
			want: "git.acme.example",
		},
		{
			name: "no ledgers segment",
			path: "/tmp/whatever",
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := EndpointSlugFromLedgerPath(tt.path); got != tt.want {
				t.Errorf("EndpointSlugFromLedgerPath(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

// TestRecoverRepoIDFromLocalState_RepoMarkerPriority verifies that the
// .repo_<uuid> marker takes priority over the ledger path — the marker is
// authoritative and may diverge if the ledger path was stale.
func TestRecoverRepoIDFromLocalState_RepoMarkerPriority(t *testing.T) {
	root := t.TempDir()
	sageox := filepath.Join(root, ".sageox")
	if err := os.MkdirAll(sageox, 0o755); err != nil {
		t.Fatal(err)
	}
	// drop a marker
	markerID := "repo_marker-uuid-001"
	if err := os.WriteFile(filepath.Join(sageox, ".repo_marker-uuid-001"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	// drop a divergent ledger path
	cfg := &LocalConfig{Ledger: &LedgerConfig{Path: "/x/sageox/foo/ledgers/repo_other-uuid"}}
	if err := SaveLocalConfig(root, cfg); err != nil {
		t.Fatal(err)
	}

	got, _ := RecoverRepoIDFromLocalState(root)
	if got != markerID {
		t.Errorf("expected marker repo_id %q, got %q", markerID, got)
	}
}

// TestRecoverRepoIDFromLocalState_LedgerPathFallback verifies recovery from
// the ledger path when no .repo_<uuid> marker exists. This is the realistic
// "init committed on a feature branch and reset away" case: the marker was
// tracked and removed by `git reset`, but the gitignored config.local.toml
// survives and still encodes the canonical repo_id in its ledger path.
func TestRecoverRepoIDFromLocalState_LedgerPathFallback(t *testing.T) {
	root := t.TempDir()
	sageox := filepath.Join(root, ".sageox")
	if err := os.MkdirAll(sageox, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := &LocalConfig{Ledger: &LedgerConfig{
		Path: "/Users/x/.local/share/sageox/sageox.ai/ledgers/repo_recovered-uuid",
	}}
	if err := SaveLocalConfig(root, cfg); err != nil {
		t.Fatal(err)
	}

	repoID, slug := RecoverRepoIDFromLocalState(root)
	if repoID != "repo_recovered-uuid" {
		t.Errorf("repo_id = %q, want repo_recovered-uuid", repoID)
	}
	if slug != "sageox.ai" {
		t.Errorf("slug = %q, want sageox.ai", slug)
	}
}

func TestRecoverRepoIDFromLocalState_NoState(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".sageox"), 0o755); err != nil {
		t.Fatal(err)
	}
	// no marker, no local config
	id, _ := RecoverRepoIDFromLocalState(root)
	if id != "" {
		t.Errorf("expected empty repo_id, got %q", id)
	}
}

// TestBackfillProjectConfigFromLocalState_HappyPath simulates the exact
// scenario from PR #568: feature-branch init was reset away, leaving
// config.local.toml as the only surviving link to the running daemon's
// repo_id. After backfill, GetRepoID returns the recovered ID and any future
// CurrentWorkspaceID computation will match the daemon's registered
// workspace_id (sha256(repo_id)[:8]).
func TestBackfillProjectConfigFromLocalState_HappyPath(t *testing.T) {
	root := t.TempDir()
	sageox := filepath.Join(root, ".sageox")
	if err := os.MkdirAll(sageox, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := &LocalConfig{Ledger: &LedgerConfig{
		Path: "/Users/x/.local/share/sageox/sageox.ai/ledgers/repo_recovered-uuid",
	}}
	if err := SaveLocalConfig(root, cfg); err != nil {
		t.Fatal(err)
	}

	wrote, err := BackfillProjectConfigFromLocalState(root)
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if !wrote {
		t.Fatal("expected backfill to write config.json")
	}
	if got := GetRepoID(root); got != "repo_recovered-uuid" {
		t.Errorf("GetRepoID = %q, want repo_recovered-uuid", got)
	}

	// idempotency: calling again must be a no-op (file already exists).
	wrote2, err := BackfillProjectConfigFromLocalState(root)
	if err != nil {
		t.Fatalf("second backfill: %v", err)
	}
	if wrote2 {
		t.Error("second backfill unexpectedly wrote a new file")
	}
}

// TestBackfillProjectConfigFromLocalState_NothingRecoverable ensures the
// backfill is a no-op (not an error, not a fresh-bootstrap) for genuinely
// uninitialized projects — preserving SaveLocalConfig's "do not bootstrap an
// uninitialized project" stance.
func TestBackfillProjectConfigFromLocalState_NothingRecoverable(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".sageox"), 0o755); err != nil {
		t.Fatal(err)
	}
	wrote, err := BackfillProjectConfigFromLocalState(root)
	if err != nil {
		t.Fatalf("backfill returned err on uninitialized project: %v", err)
	}
	if wrote {
		t.Error("backfill should not have written anything without recoverable repo_id")
	}
	if _, err := os.Stat(filepath.Join(root, ".sageox", "config.json")); !os.IsNotExist(err) {
		t.Error("config.json should not exist after no-op backfill")
	}
}

// TestBackfillProjectConfigFromLocalState_NoSageoxDir guards against
// accidentally bootstrapping a fully uninitialized project.
func TestBackfillProjectConfigFromLocalState_NoSageoxDir(t *testing.T) {
	root := t.TempDir()
	wrote, err := BackfillProjectConfigFromLocalState(root)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if wrote {
		t.Error("must not bootstrap when .sageox/ is absent")
	}
}
