package manifest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name            string
		input           string
		wantErr         bool
		wantErrContains string
		wantVersion     int
		wantIncludes    []string
		wantDenies      []string
		wantSyncMin     int
		wantGCDays      int
	}{
		{
			name: "valid manifest",
			input: `version 1

# Control plane
include .sageox/
include SOUL.md
include TEAM.md
include memory/

deny data/

sync_interval_minutes 10
gc_interval_days 14`,
			wantVersion:  1,
			wantIncludes: []string{".sageox/", "SOUL.md", "TEAM.md", "memory/"},
			wantDenies:   []string{"data/"},
			wantSyncMin:  10,
			wantGCDays:   14,
		},
		{
			name:            "missing version",
			input:           "include .sageox/\ninclude SOUL.md\n",
			wantErr:         true,
			wantErrContains: "missing version",
		},
		{
			name:            "unknown version",
			input:           "version 99\ninclude .sageox/\n",
			wantErr:         true,
			wantErrContains: "unknown version",
		},
		{
			name:            "non-numeric version",
			input:           "version abc\ninclude .sageox/\n",
			wantErr:         true,
			wantErrContains: "unknown version",
		},
		{
			name:         "path traversal rejected",
			input:        "version 1\ninclude ../etc/passwd\ninclude .sageox/\n",
			wantVersion:  1,
			wantIncludes: []string{".sageox/"},
		},
		{
			name:         "deny overrides include",
			input:        "version 1\ninclude data/\ndeny data/\n",
			wantVersion:  1,
			wantDenies:   []string{"data/"},
			wantIncludes: nil,
		},
		{
			name:         "include after deny wins (last-one-wins)",
			input:        "version 1\ndeny memory/\ninclude memory/\n",
			wantVersion:  1,
			wantIncludes: []string{"memory/"},
			wantDenies:   nil,
		},
		{
			name:         "unknown directive skipped",
			input:        "version 1\nfuture_directive value\ninclude .sageox/\n",
			wantVersion:  1,
			wantIncludes: []string{".sageox/"},
		},
		{
			name:    "empty file",
			input:   "",
			wantErr: true,
		},
		{
			name:         "comments and blank lines only besides version",
			input:        "version 1\n# just a comment\n\n# another\ninclude docs/\n",
			wantVersion:  1,
			wantIncludes: []string{"docs/"},
		},
		{
			name:         "sync interval clamped to minimum",
			input:        "version 1\nsync_interval_minutes 0\ninclude .sageox/\n",
			wantVersion:  1,
			wantSyncMin:  MinSyncIntervalMin,
			wantIncludes: []string{".sageox/"},
		},
		{
			name:         "gc interval clamped to maximum",
			input:        "version 1\ngc_interval_days 365\ninclude .sageox/\n",
			wantVersion:  1,
			wantGCDays:   MaxGCIntervalDays,
			wantIncludes: []string{".sageox/"},
		},
		{
			name:         "gc interval clamped to minimum",
			input:        "version 1\ngc_interval_days 0\ninclude .sageox/\n",
			wantVersion:  1,
			wantGCDays:   MinGCIntervalDays,
			wantIncludes: []string{".sageox/"},
		},
		{
			name:         "defaults when not specified",
			input:        "version 1\ninclude .sageox/\n",
			wantVersion:  1,
			wantSyncMin:  DefaultSyncIntervalMin,
			wantGCDays:   DefaultGCIntervalDays,
			wantIncludes: []string{".sageox/"},
		},
		{
			name:         "tab-separated directives parsed",
			input:        "version\t1\ninclude\t.sageox/\ndeny\tdata/\n",
			wantVersion:  1,
			wantIncludes: []string{".sageox/"},
			wantDenies:   []string{"data/"},
		},
		{
			name:         "mixed tabs and spaces",
			input:        "version \t 1\ninclude\t\t.sageox/\n",
			wantVersion:  1,
			wantIncludes: []string{".sageox/"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := Parse(strings.NewReader(tt.input))

			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if tt.wantErrContains != "" && !strings.Contains(err.Error(), tt.wantErrContains) {
					t.Errorf("error %q does not contain %q", err.Error(), tt.wantErrContains)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if cfg.Version != tt.wantVersion {
				t.Errorf("version = %d, want %d", cfg.Version, tt.wantVersion)
			}

			if tt.wantSyncMin > 0 && cfg.SyncIntervalMin != tt.wantSyncMin {
				t.Errorf("sync_interval = %d, want %d", cfg.SyncIntervalMin, tt.wantSyncMin)
			}

			if tt.wantGCDays > 0 && cfg.GCIntervalDays != tt.wantGCDays {
				t.Errorf("gc_interval = %d, want %d", cfg.GCIntervalDays, tt.wantGCDays)
			}

			assertStringSliceMatch(t, "includes", cfg.Includes, tt.wantIncludes)
			assertStringSliceMatch(t, "denies", cfg.Denies, tt.wantDenies)
		})
	}
}

func TestParseFile(t *testing.T) {
	t.Run("missing file returns fallback", func(t *testing.T) {
		cfg := ParseFile("/nonexistent/path/sync.manifest")
		fb := FallbackConfig()
		assertStringSliceMatch(t, "includes", cfg.Includes, fb.Includes)
	})

	t.Run("valid file parsed", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "sync.manifest")
		content := "version 1\ninclude .sageox/\ninclude SOUL.md\ndeny data/\n"
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}

		cfg := ParseFile(path)
		if cfg.Version != 1 {
			t.Errorf("version = %d, want 1", cfg.Version)
		}
		assertContains(t, "includes", cfg.Includes, ".sageox/")
		assertContains(t, "includes", cfg.Includes, "SOUL.md")
		assertContains(t, "denies", cfg.Denies, "data/")
	})

	t.Run("invalid file returns fallback", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "sync.manifest")
		if err := os.WriteFile(path, []byte("garbage content\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		cfg := ParseFile(path)
		fb := FallbackConfig()
		assertStringSliceMatch(t, "includes", cfg.Includes, fb.Includes)
	})

	t.Run("no includes returns fallback", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "sync.manifest")
		if err := os.WriteFile(path, []byte("version 1\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		cfg := ParseFile(path)
		fb := FallbackConfig()
		assertStringSliceMatch(t, "includes", cfg.Includes, fb.Includes)
	})
}

func TestComputeSparseSet(t *testing.T) {
	tests := []struct {
		name     string
		cfg      *ManifestConfig
		want     []string
		wantNone []string // paths that should NOT be in result
	}{
		{
			name: "includes minus denies",
			cfg: &ManifestConfig{
				Includes: []string{".sageox/", "memory/", "data/"},
				Denies:   []string{"data/"},
			},
			want:     []string{"/*", "!/*/", ".sageox/", "memory/"},
			wantNone: []string{"data/"},
		},
		{
			name: "deny parent blocks child include",
			cfg: &ManifestConfig{
				Includes: []string{"data/slack/"},
				Denies:   []string{"data/"},
			},
			want:     []string{"/*", "!/*/"},
			wantNone: []string{"data/slack/"},
		},
		{
			name: "no denies passes all includes",
			cfg: &ManifestConfig{
				Includes: []string{".sageox/", "SOUL.md", "memory/"},
			},
			want: []string{"/*", "!/*/", ".sageox/", "SOUL.md", "memory/"},
		},
		{
			name: "nil config",
			cfg:  nil,
			want: nil,
		},
		{
			name: "include parent of deny is excluded",
			cfg: &ManifestConfig{
				Includes: []string{"data/"},
				Denies:   []string{"data/secrets/"},
			},
			want:     []string{"/*", "!/*/"},
			wantNone: []string{"data/"},
		},
		{
			name: "exact file deny blocks matching include",
			cfg: &ManifestConfig{
				Includes: []string{"README.md", ".sageox/"},
				Denies:   []string{"README.md"},
			},
			want:     []string{"/*", "!/*/", ".sageox/"},
			wantNone: []string{"README.md"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ComputeSparseSet(tt.cfg)

			for _, w := range tt.want {
				assertContains(t, "sparse set", result, w)
			}
			for _, w := range tt.wantNone {
				if contains(result, w) {
					t.Errorf("sparse set should not contain %q", w)
				}
			}
		})
	}
}

func TestFallbackConfig(t *testing.T) {
	cfg := FallbackConfig()

	if cfg.Version != SupportedVersion {
		t.Errorf("version = %d, want %d", cfg.Version, SupportedVersion)
	}
	if cfg.SyncIntervalMin != DefaultSyncIntervalMin {
		t.Errorf("sync_interval = %d, want %d", cfg.SyncIntervalMin, DefaultSyncIntervalMin)
	}
	if cfg.GCIntervalDays != DefaultGCIntervalDays {
		t.Errorf("gc_interval = %d, want %d", cfg.GCIntervalDays, DefaultGCIntervalDays)
	}

	expectedPaths := []string{".sageox/", "SOUL.md", "TEAM.md", "MEMORY.md", "AGENTS.md", "memory/", "docs/", "coworkers/"}
	for _, p := range expectedPaths {
		assertContains(t, "fallback includes", cfg.Includes, p)
	}

	// verify it returns a copy
	cfg.Includes[0] = "modified"
	cfg2 := FallbackConfig()
	if cfg2.Includes[0] == "modified" {
		t.Error("FallbackConfig should return a fresh copy")
	}
}

func assertStringSliceMatch(t *testing.T, label string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s: got %d items %v, want %d items %v", label, len(got), got, len(want), want)
		return
	}
	for _, w := range want {
		if !contains(got, w) {
			t.Errorf("%s: missing %q in %v", label, w, got)
		}
	}
}

func assertContains(t *testing.T, label string, slice []string, item string) {
	t.Helper()
	if !contains(slice, item) {
		t.Errorf("%s: missing %q in %v", label, item, slice)
	}
}

func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}
