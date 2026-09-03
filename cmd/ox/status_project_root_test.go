package main

import (
	"os"
	"path/filepath"
	"testing"
)

// --- Project resolution for `ox status` ---

// TestResolveStatusProject verifies that status describes the repository it is
// standing in, from anywhere inside it.
//
// Failure prevented: run from a subdirectory, status reported an initialized
// repo as "not initialized", suppressed the ledger and code-index rows, and
// printed `ox init` as the starred next action — on a healthy project, where
// re-running init is the one command that can delete agent hook files.
//
// The nested cases guard the opposite error: resolving by walking up without a
// boundary makes an uninitialized repo adopt an ancestor's project, which hides
// the `ox init` it genuinely needs and attributes another repo's ledger and team
// to it.
func TestResolveStatusProject(t *testing.T) {
	// initProject writes the marker config.IsInitialized actually requires.
	initProject := func(t *testing.T, dir string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(dir, ".sageox"), 0o755); err != nil {
			t.Fatalf("create .sageox in %q: %v", dir, err)
		}
		cfg := filepath.Join(dir, ".sageox", "config.json")
		if err := os.WriteFile(cfg, []byte("{}\n"), 0o644); err != nil {
			t.Fatalf("write %q: %v", cfg, err)
		}
	}

	// bareSageoxDir creates .sageox/ with no config inside it — the state
	// CLAUDE.md calls out as "directory exists != initialized".
	bareSageoxDir := func(t *testing.T, dir string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(dir, ".sageox"), 0o755); err != nil {
			t.Fatalf("create .sageox in %q: %v", dir, err)
		}
	}

	// initProjectYAML uses the other config filename IsInitialized accepts.
	initProjectYAML := func(t *testing.T, dir string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(dir, ".sageox"), 0o755); err != nil {
			t.Fatalf("create .sageox in %q: %v", dir, err)
		}
		cfg := filepath.Join(dir, ".sageox", "config.yaml")
		if err := os.WriteFile(cfg, []byte("version: \"1\"\n"), 0o644); err != nil {
			t.Fatalf("write %q: %v", cfg, err)
		}
	}

	mkdirs := func(t *testing.T, dir string) {
		t.Helper()
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %q: %v", dir, err)
		}
	}

	tests := []struct {
		name string
		// setup builds the tree and returns the arguments to resolve with,
		// plus the project root expected back.
		setup           func(t *testing.T, tmp string) (cwd, gitRoot, wantRoot string)
		wantInitialized bool
	}{
		{
			name: "from the project root",
			setup: func(t *testing.T, tmp string) (string, string, string) {
				initProject(t, tmp)
				return tmp, tmp, tmp
			},
			wantInitialized: true,
		},
		{
			name: "from an immediate subdirectory",
			setup: func(t *testing.T, tmp string) (string, string, string) {
				initProject(t, tmp)
				sub := filepath.Join(tmp, "pkg")
				mkdirs(t, sub)
				return sub, tmp, tmp
			},
			wantInitialized: true,
		},
		{
			name: "from a deeply nested subdirectory",
			setup: func(t *testing.T, tmp string) (string, string, string) {
				initProject(t, tmp)
				deep := filepath.Join(tmp, "services", "api", "internal")
				mkdirs(t, deep)
				return deep, tmp, tmp
			},
			wantInitialized: true,
		},
		{
			name: "a config.yaml project resolves from a subdirectory",
			setup: func(t *testing.T, tmp string) (string, string, string) {
				initProjectYAML(t, tmp)
				sub := filepath.Join(tmp, "pkg")
				mkdirs(t, sub)
				return sub, tmp, tmp
			},
			wantInitialized: true,
		},
		{
			name: "a bare .sageox directory is not an initialized project",
			setup: func(t *testing.T, tmp string) (string, string, string) {
				bareSageoxDir(t, tmp)
				sub := filepath.Join(tmp, "pkg")
				mkdirs(t, sub)
				return sub, tmp, tmp
			},
			wantInitialized: false,
		},
		{
			name: "uninitialized repo does not adopt an initialized ancestor",
			setup: func(t *testing.T, tmp string) (string, string, string) {
				initProject(t, tmp) // the ancestor — e.g. a stray ~/.sageox
				repo := filepath.Join(tmp, "repo")
				sub := filepath.Join(repo, "sub")
				mkdirs(t, sub)
				return sub, repo, repo
			},
			wantInitialized: false,
		},
		{
			name: "outside a git repo, an initialized cwd still reports",
			setup: func(t *testing.T, tmp string) (string, string, string) {
				initProject(t, tmp)
				return tmp, "", tmp
			},
			wantInitialized: true,
		},
		{
			name: "outside a git repo, an uninitialized cwd reports itself",
			setup: func(t *testing.T, tmp string) (string, string, string) {
				return tmp, "", tmp
			},
			wantInitialized: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cwd, gitRoot, wantRoot := tt.setup(t, t.TempDir())

			gotRoot, gotInitialized := resolveStatusProject(cwd, gitRoot)

			if gotRoot != wantRoot {
				t.Errorf("root = %q, want %q", gotRoot, wantRoot)
			}
			if gotInitialized != tt.wantInitialized {
				t.Errorf("initialized = %v, want %v", gotInitialized, tt.wantInitialized)
			}
		})
	}
}
