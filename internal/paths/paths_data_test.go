package paths

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTeamsDataDir(t *testing.T) {
	saved := saveEnv("OX_XDG_ENABLE", "SAGEOX_ENDPOINT")
	defer restoreEnv(saved)

	// all endpoints now include endpoint slug in path for consistent namespacing
	t.Run("production endpoint includes sageox.ai", func(t *testing.T) {
		clearXDGEnv()
		os.Unsetenv("SAGEOX_ENDPOINT")
		dir := TeamsDataDir("https://api.sageox.ai")
		// api.sageox.ai normalizes to sageox.ai
		if !strings.HasSuffix(dir, filepath.Join("sageox.ai", "teams")) {
			t.Errorf("TeamsDataDir(production) = %q, want suffix sageox.ai/teams", dir)
		}
	})

	t.Run("production without api prefix", func(t *testing.T) {
		clearXDGEnv()
		os.Unsetenv("SAGEOX_ENDPOINT")
		dir := TeamsDataDir("https://sageox.ai")
		if !strings.HasSuffix(dir, filepath.Join("sageox.ai", "teams")) {
			t.Errorf("TeamsDataDir(sageox.ai) = %q, want suffix sageox.ai/teams", dir)
		}
	})

	t.Run("staging endpoint", func(t *testing.T) {
		clearXDGEnv()
		os.Unsetenv("SAGEOX_ENDPOINT")
		dir := TeamsDataDir("https://staging.sageox.ai")
		if !strings.HasSuffix(dir, filepath.Join("staging.sageox.ai", "teams")) {
			t.Errorf("TeamsDataDir(staging) = %q, want suffix staging.sageox.ai/teams", dir)
		}
	})

	t.Run("localhost with port strips port", func(t *testing.T) {
		clearXDGEnv()
		os.Unsetenv("SAGEOX_ENDPOINT")
		dir := TeamsDataDir("http://localhost:8080")
		// NormalizeSlug strips port numbers
		if !strings.HasSuffix(dir, filepath.Join("localhost", "teams")) {
			t.Errorf("TeamsDataDir(localhost:8080) = %q, want suffix localhost/teams", dir)
		}
	})

	t.Run("empty endpoint panics", func(t *testing.T) {
		clearXDGEnv()
		defer func() {
			if r := recover(); r == nil {
				t.Error("TeamsDataDir('') should panic when endpoint is empty")
			}
		}()
		TeamsDataDir("")
	})
}

func TestTeamContextDir(t *testing.T) {
	saved := saveEnv("OX_XDG_ENABLE", "SAGEOX_ENDPOINT")
	defer restoreEnv(saved)

	clearXDGEnv()
	os.Unsetenv("SAGEOX_ENDPOINT")

	t.Run("production endpoint paths", func(t *testing.T) {
		tests := []struct {
			name   string
			teamID string
			want   string
		}{
			{"simple id", "abc123", "abc123"},
			{"with slashes", "org/team", "org_team"},
			{"empty", "", "unknown"},
			{"with spaces", "my team", "my_team"},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				dir := TeamContextDir(tt.teamID, "https://api.sageox.ai")
				if !strings.HasSuffix(dir, tt.want) {
					t.Errorf("TeamContextDir(%q, production) = %q, want suffix %q", tt.teamID, dir, tt.want)
				}
				// should be under data/sageox.ai/teams for production (now includes endpoint)
				if !strings.Contains(dir, filepath.Join("sageox.ai", "teams")) {
					t.Errorf("TeamContextDir(%q, production) = %q, should contain sageox.ai/teams", tt.teamID, dir)
				}
			})
		}
	})

	t.Run("staging endpoint paths", func(t *testing.T) {
		dir := TeamContextDir("team123", "https://staging.sageox.ai")
		if !strings.Contains(dir, filepath.Join("staging.sageox.ai", "teams", "team123")) {
			t.Errorf("TeamContextDir(team123, staging) = %q, want to contain staging.sageox.ai/teams/team123", dir)
		}
	})

	t.Run("localhost endpoint paths", func(t *testing.T) {
		dir := TeamContextDir("team456", "http://localhost:8080")
		// port is stripped by NormalizeSlug
		if !strings.Contains(dir, filepath.Join("localhost", "teams", "team456")) {
			t.Errorf("TeamContextDir(team456, localhost) = %q, want to contain localhost/teams/team456", dir)
		}
	})
}

func TestLedgersDataDir(t *testing.T) {
	saved := saveEnv("OX_XDG_ENABLE", "SAGEOX_ENDPOINT")
	defer restoreEnv(saved)

	t.Run("production endpoint includes sageox.ai", func(t *testing.T) {
		clearXDGEnv()
		os.Unsetenv("SAGEOX_ENDPOINT")
		dir := LedgersDataDir("repo123", "https://api.sageox.ai")
		// api.sageox.ai normalizes to sageox.ai
		if !strings.HasSuffix(dir, filepath.Join("sageox.ai", "ledgers", "repo123")) {
			t.Errorf("LedgersDataDir(production) = %q, want suffix sageox.ai/ledgers/repo123", dir)
		}
	})

	t.Run("production without api prefix", func(t *testing.T) {
		clearXDGEnv()
		os.Unsetenv("SAGEOX_ENDPOINT")
		dir := LedgersDataDir("repo456", "https://sageox.ai")
		if !strings.HasSuffix(dir, filepath.Join("sageox.ai", "ledgers", "repo456")) {
			t.Errorf("LedgersDataDir(sageox.ai) = %q, want suffix sageox.ai/ledgers/repo456", dir)
		}
	})

	t.Run("staging endpoint", func(t *testing.T) {
		clearXDGEnv()
		os.Unsetenv("SAGEOX_ENDPOINT")
		dir := LedgersDataDir("repo789", "https://staging.sageox.ai")
		if !strings.HasSuffix(dir, filepath.Join("staging.sageox.ai", "ledgers", "repo789")) {
			t.Errorf("LedgersDataDir(staging) = %q, want suffix staging.sageox.ai/ledgers/repo789", dir)
		}
	})

	t.Run("localhost with port strips port", func(t *testing.T) {
		clearXDGEnv()
		os.Unsetenv("SAGEOX_ENDPOINT")
		dir := LedgersDataDir("repoabc", "http://localhost:8080")
		// NormalizeSlug strips port numbers
		if !strings.HasSuffix(dir, filepath.Join("localhost", "ledgers", "repoabc")) {
			t.Errorf("LedgersDataDir(localhost:8080) = %q, want suffix localhost/ledgers/repoabc", dir)
		}
	})

	t.Run("empty repoID returns base ledgers dir", func(t *testing.T) {
		clearXDGEnv()
		os.Unsetenv("SAGEOX_ENDPOINT")
		dir := LedgersDataDir("", "https://sageox.ai")
		if !strings.HasSuffix(dir, filepath.Join("sageox.ai", "ledgers")) {
			t.Errorf("LedgersDataDir('', sageox.ai) = %q, want suffix sageox.ai/ledgers", dir)
		}
		// should NOT have a trailing path component after ledgers
		if strings.HasSuffix(dir, filepath.Join("ledgers", "unknown")) {
			t.Errorf("LedgersDataDir('', sageox.ai) = %q, should not have unknown suffix", dir)
		}
	})

	t.Run("empty endpoint panics", func(t *testing.T) {
		clearXDGEnv()
		defer func() {
			if r := recover(); r == nil {
				t.Error("LedgersDataDir should panic when endpoint is empty")
			}
		}()
		LedgersDataDir("repo123", "")
	})

	t.Run("repoID sanitization", func(t *testing.T) {
		clearXDGEnv()
		os.Unsetenv("SAGEOX_ENDPOINT")
		tests := []struct {
			name   string
			repoID string
			want   string
		}{
			{"simple id", "abc123", "abc123"},
			{"with slashes", "org/repo", "org_repo"},
			{"with spaces", "my repo", "my_repo"},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				dir := LedgersDataDir(tt.repoID, "https://sageox.ai")
				if !strings.HasSuffix(dir, tt.want) {
					t.Errorf("LedgersDataDir(%q) = %q, want suffix %q", tt.repoID, dir, tt.want)
				}
			})
		}
	})
}

func TestCodeDBSharedDir(t *testing.T) {
	saved := saveEnv("OX_XDG_ENABLE", "SAGEOX_ENDPOINT")
	defer restoreEnv(saved)

	clearXDGEnv()
	os.Unsetenv("SAGEOX_ENDPOINT")

	t.Run("returns path under .sageox/cache", func(t *testing.T) {
		dir := CodeDBSharedDir("repo123", "https://sageox.ai")
		if !strings.Contains(dir, filepath.Join(".sageox", "cache", "codedb")) {
			t.Errorf("CodeDBSharedDir() = %q, want path containing .sageox/cache/codedb", dir)
		}
	})

	t.Run("not at ledger root", func(t *testing.T) {
		dir := CodeDBSharedDir("repo123", "https://sageox.ai")
		ledgerRoot := LedgersDataDir("repo123", "https://sageox.ai")
		badPath := filepath.Join(ledgerRoot, "codedb")
		if dir == badPath {
			t.Errorf("CodeDBSharedDir() = %q, must not be at ledger root", dir)
		}
	})

	t.Run("empty inputs return empty", func(t *testing.T) {
		if dir := CodeDBSharedDir("", "https://sageox.ai"); dir != "" {
			t.Errorf("CodeDBSharedDir('', endpoint) = %q, want empty", dir)
		}
		if dir := CodeDBSharedDir("repo123", ""); dir != "" {
			t.Errorf("CodeDBSharedDir(repo, '') = %q, want empty", dir)
		}
	})
}

func TestCodeDBBaselineDir(t *testing.T) {
	saved := saveEnv("OX_XDG_ENABLE", "SAGEOX_ENDPOINT")
	defer restoreEnv(saved)

	clearXDGEnv()
	os.Unsetenv("SAGEOX_ENDPOINT")

	t.Run("returns path under shared dir with baseline suffix", func(t *testing.T) {
		dir := CodeDBBaselineDir("repo123", "https://sageox.ai")
		shared := CodeDBSharedDir("repo123", "https://sageox.ai")
		want := filepath.Join(shared, "baseline")
		if dir != want {
			t.Errorf("CodeDBBaselineDir() = %q, want %q", dir, want)
		}
	})

	t.Run("path is inside ledger cache not worktree", func(t *testing.T) {
		dir := CodeDBBaselineDir("repo123", "https://sageox.ai")
		if !strings.Contains(dir, filepath.Join(".sageox", "cache", "codedb", "baseline")) {
			t.Errorf("CodeDBBaselineDir() = %q, want path containing .sageox/cache/codedb/baseline", dir)
		}
	})

	t.Run("empty inputs return empty", func(t *testing.T) {
		if dir := CodeDBBaselineDir("", "https://sageox.ai"); dir != "" {
			t.Errorf("CodeDBBaselineDir('', endpoint) = %q, want empty", dir)
		}
		if dir := CodeDBBaselineDir("repo123", ""); dir != "" {
			t.Errorf("CodeDBBaselineDir(repo, '') = %q, want empty", dir)
		}
	})
}

func TestWhisperDBDir(t *testing.T) {
	saved := saveEnv("OX_XDG_ENABLE", "OX_XDG_DISABLE")
	defer restoreEnv(saved)
	clearXDGEnv()

	tests := []struct {
		name        string
		repoID      string
		endpointURL string
		wantEmpty   bool
		wantSuffix  string
	}{
		{
			name:        "valid inputs",
			repoID:      "repo123",
			endpointURL: "https://sageox.ai",
			wantSuffix:  filepath.Join(".sageox", "cache", "whisper"),
		},
		{
			name:        "empty repoID returns empty",
			repoID:      "",
			endpointURL: "https://sageox.ai",
			wantEmpty:   true,
		},
		{
			name:        "empty endpoint returns empty",
			repoID:      "repo123",
			endpointURL: "",
			wantEmpty:   true,
		},
		{
			name:        "both empty returns empty",
			repoID:      "",
			endpointURL: "",
			wantEmpty:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := WhisperDBDir(tt.repoID, tt.endpointURL)
			if tt.wantEmpty {
				if dir != "" {
					t.Errorf("WhisperDBDir(%q, %q) = %q, want empty", tt.repoID, tt.endpointURL, dir)
				}
				return
			}
			if !strings.Contains(dir, tt.wantSuffix) {
				t.Errorf("WhisperDBDir(%q, %q) = %q, want to contain %q", tt.repoID, tt.endpointURL, dir, tt.wantSuffix)
			}
		})
	}
}

func TestTeamWhisperDBDir(t *testing.T) {
	saved := saveEnv("OX_XDG_ENABLE", "OX_XDG_DISABLE")
	defer restoreEnv(saved)
	clearXDGEnv()

	tests := []struct {
		name        string
		teamID      string
		endpointURL string
		wantEmpty   bool
		wantSuffix  string
	}{
		{
			name:        "valid inputs",
			teamID:      "team456",
			endpointURL: "https://sageox.ai",
			wantSuffix:  filepath.Join("team456", ".sageox", "cache", "whisper"),
		},
		{
			name:        "empty teamID returns empty",
			teamID:      "",
			endpointURL: "https://sageox.ai",
			wantEmpty:   true,
		},
		{
			name:        "empty endpoint returns empty",
			teamID:      "team456",
			endpointURL: "",
			wantEmpty:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := TeamWhisperDBDir(tt.teamID, tt.endpointURL)
			if tt.wantEmpty {
				if dir != "" {
					t.Errorf("TeamWhisperDBDir(%q, %q) = %q, want empty", tt.teamID, tt.endpointURL, dir)
				}
				return
			}
			if !strings.Contains(dir, tt.wantSuffix) {
				t.Errorf("TeamWhisperDBDir(%q, %q) = %q, want to contain %q", tt.teamID, tt.endpointURL, dir, tt.wantSuffix)
			}
		})
	}
}

func TestLedgerSessionCacheBase(t *testing.T) {
	saved := saveEnv("OX_XDG_ENABLE", "OX_XDG_DISABLE")
	defer restoreEnv(saved)
	clearXDGEnv()

	tests := []struct {
		name        string
		repoID      string
		endpointURL string
		wantEmpty   bool
		wantSuffix  string
	}{
		{
			name:        "valid inputs",
			repoID:      "repo123",
			endpointURL: "https://sageox.ai",
			wantSuffix:  filepath.Join(".sageox", "cache"),
		},
		{
			name:        "empty repoID returns empty",
			repoID:      "",
			endpointURL: "https://sageox.ai",
			wantEmpty:   true,
		},
		{
			name:        "empty endpoint returns empty",
			repoID:      "repo123",
			endpointURL: "",
			wantEmpty:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := LedgerSessionCacheBase(tt.repoID, tt.endpointURL)
			if tt.wantEmpty {
				if dir != "" {
					t.Errorf("LedgerSessionCacheBase(%q, %q) = %q, want empty", tt.repoID, tt.endpointURL, dir)
				}
				return
			}
			if !strings.HasSuffix(dir, tt.wantSuffix) {
				t.Errorf("LedgerSessionCacheBase(%q, %q) = %q, want suffix %q", tt.repoID, tt.endpointURL, dir, tt.wantSuffix)
			}
			// must be under the ledger data dir
			ledgerDir := LedgersDataDir(tt.repoID, tt.endpointURL)
			if !strings.HasPrefix(dir, ledgerDir) {
				t.Errorf("LedgerSessionCacheBase result %q not under LedgersDataDir %q", dir, ledgerDir)
			}
		})
	}
}

func TestCodeDBDataDir(t *testing.T) {
	dir := CodeDBDataDir("/home/user/project")
	want := filepath.Join("/home/user/project", ".sageox", "cache", "codedb")
	if dir != want {
		t.Errorf("CodeDBDataDir() = %q, want %q", dir, want)
	}
}
