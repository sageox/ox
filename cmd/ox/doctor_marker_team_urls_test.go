package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckMarkerTeamURLsIn(t *testing.T) {
	const legacyMarker = `{
  "repo_id": "repo_01abc",
  "type": "git",
  "endpoint": "https://sageox.ai",
  "ledger_url": "https://git.sageox.ai/ledger.git",
  "team_urls": {
    "Acme Platform": "https://git.sageox.ai/sageox-team_aaa/team-context.git",
    "Secret Acquisition": "https://git.sageox.ai/sageox-team_bbb/team-context.git"
  },
  "init_by_email": "alice@example.com"
}`
	const cleanMarker = `{"repo_id": "repo_02def", "endpoint": "https://sageox.ai", "ledger_url": "https://git.sageox.ai/l.git"}`

	tests := []struct {
		name        string
		markers     map[string]string
		fix         bool
		wantPassed  bool
		wantWarning bool
		wantSkipped bool
		wantStrip   []string // markers that must no longer contain team_urls
		wantKeep    []string // markers that must still contain team_urls
	}{
		{
			name:        "no markers",
			markers:     map[string]string{},
			wantSkipped: true,
		},
		{
			name:       "clean marker passes",
			markers:    map[string]string{".repo_02def": cleanMarker},
			wantPassed: true,
		},
		{
			name:        "team_urls detected without fix",
			markers:     map[string]string{".repo_01abc": legacyMarker, ".repo_02def": cleanMarker},
			wantPassed:  true, // WarningCheck reports passed=true
			wantWarning: true,
			wantKeep:    []string{".repo_01abc"},
		},
		{
			name:        "unreadable marker warns instead of ok",
			markers:     map[string]string{".repo_04jkl": `{not json`, ".repo_02def": cleanMarker},
			wantPassed:  true, // WarningCheck reports passed=true
			wantWarning: true,
		},
		{
			name:       "team_urls removed with fix",
			markers:    map[string]string{".repo_01abc": legacyMarker, ".repo_02def": cleanMarker},
			fix:        true,
			wantPassed: true,
			wantStrip:  []string{".repo_01abc"},
		},
		{
			name:       "empty team_urls removed with fix",
			markers:    map[string]string{".repo_03ghi": `{"repo_id": "repo_03ghi", "team_urls": {}}`},
			fix:        true,
			wantPassed: true,
			wantStrip:  []string{".repo_03ghi"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sageoxDir := t.TempDir()
			for name, content := range tt.markers {
				if err := os.WriteFile(filepath.Join(sageoxDir, name), []byte(content), 0644); err != nil {
					t.Fatal(err)
				}
			}

			result := checkMarkerTeamURLsIn(sageoxDir, tt.fix)

			if result.passed != tt.wantPassed || result.warning != tt.wantWarning || result.skipped != tt.wantSkipped {
				t.Fatalf("got passed=%v warning=%v skipped=%v (%s: %s), want passed=%v warning=%v skipped=%v",
					result.passed, result.warning, result.skipped, result.message, result.detail,
					tt.wantPassed, tt.wantWarning, tt.wantSkipped)
			}

			for _, name := range tt.wantStrip {
				data, err := os.ReadFile(filepath.Join(sageoxDir, name))
				if err != nil {
					t.Fatal(err)
				}
				var fields map[string]json.RawMessage
				if err := json.Unmarshal(data, &fields); err != nil {
					t.Fatalf("%s is not valid JSON after fix: %v", name, err)
				}
				if _, ok := fields["team_urls"]; ok {
					t.Errorf("%s still has team_urls:\n%s", name, data)
				}
				for _, leaked := range []string{"Acme Platform", "Secret Acquisition", "team_aaa", "team_bbb"} {
					if strings.Contains(string(data), leaked) {
						t.Errorf("%s still contains %q", name, leaked)
					}
				}
				if _, ok := fields["repo_id"]; !ok {
					t.Errorf("%s lost repo_id", name)
				}
			}

			if tt.fix {
				fixed := checkMarkerTeamURLsIn(sageoxDir, false)
				if !fixed.passed || fixed.warning {
					t.Errorf("re-check after fix: passed=%v warning=%v (%s)", fixed.passed, fixed.warning, fixed.message)
				}
			}

			for _, name := range tt.wantKeep {
				data, err := os.ReadFile(filepath.Join(sageoxDir, name))
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(string(data), `"team_urls"`) {
					t.Errorf("%s was modified without --fix", name)
				}
			}
		})
	}
}

func TestCheckMarkerTeamURLs_PreservesOtherFields(t *testing.T) {
	sageoxDir := t.TempDir()
	marker := `{"repo_id": "repo_01abc", "endpoint": "https://sageox.ai", "ledger_url": "https://git.sageox.ai/l.git",` +
		` "init_by_email": "alice@example.com", "fingerprint": {"first_commit": "abc"}, "future_field": 7,` +
		` "team_urls": {"Team A": "https://git.sageox.ai/a.git"}}`
	path := filepath.Join(sageoxDir, ".repo_01abc")
	if err := os.WriteFile(path, []byte(marker), 0644); err != nil {
		t.Fatal(err)
	}

	checkMarkerTeamURLsIn(sageoxDir, true)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"repo_id":       "repo_01abc",
		"endpoint":      "https://sageox.ai",
		"ledger_url":    "https://git.sageox.ai/l.git",
		"init_by_email": "alice@example.com",
		"fingerprint":   map[string]any{"first_commit": "abc"},
		"future_field":  float64(7),
	}
	if len(got) != len(want) {
		t.Fatalf("got fields %v, want %v", got, want)
	}
	for k, v := range want {
		gb, _ := json.Marshal(got[k])
		wb, _ := json.Marshal(v)
		if string(gb) != string(wb) {
			t.Errorf("field %s = %s, want %s", k, gb, wb)
		}
	}
}

func TestMarkerTeamURLsCheckRegistered(t *testing.T) {
	check := GetDoctorCheck(CheckSlugMarkerTeamURLs)
	if check == nil {
		t.Fatalf("doctor check %q not registered", CheckSlugMarkerTeamURLs)
	}
	if check.FixLevel != FixLevelSuggested {
		t.Errorf("FixLevel = %v, want FixLevelSuggested (applied by ox doctor --fix)", check.FixLevel)
	}
}

const legacyTeamURLsMarker = `{"repo_id": "repo_01abc", "team_urls": {"Acme Platform": "https://git.sageox.ai/a.git"}}`

func TestCheckMarkerTeamURLs_DoesNotFollowSymlinkedMarker(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "outside.json")
	if err := os.WriteFile(outside, []byte(legacyTeamURLsMarker), 0644); err != nil {
		t.Fatal(err)
	}
	sageoxDir := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(sageoxDir, ".repo_01abc")); err != nil {
		t.Fatal(err)
	}

	result := checkMarkerTeamURLsIn(sageoxDir, true)

	if !result.warning || !strings.Contains(result.message+result.detail, ".repo_01abc") {
		t.Errorf("want warning naming .repo_01abc, got passed=%v warning=%v (%s: %s)",
			result.passed, result.warning, result.message, result.detail)
	}
	if data, _ := os.ReadFile(outside); string(data) != legacyTeamURLsMarker {
		t.Errorf("file outside .sageox/ was rewritten through the symlink:\n%s", data)
	}
}

func TestCheckMarkerTeamURLs_DoesNotRewriteThroughSymlinkedSageoxDir(t *testing.T) {
	realDir := t.TempDir()
	marker := filepath.Join(realDir, ".repo_01abc")
	if err := os.WriteFile(marker, []byte(legacyTeamURLsMarker), 0644); err != nil {
		t.Fatal(err)
	}
	sageoxDir := filepath.Join(t.TempDir(), ".sageox")
	if err := os.Symlink(realDir, sageoxDir); err != nil {
		t.Fatal(err)
	}

	result := checkMarkerTeamURLsIn(sageoxDir, true)

	if !result.warning {
		t.Errorf("want warning, got passed=%v (%s: %s)", result.passed, result.message, result.detail)
	}
	if data, _ := os.ReadFile(marker); string(data) != legacyTeamURLsMarker {
		t.Errorf("marker behind a symlinked .sageox/ was rewritten:\n%s", data)
	}
}

func TestCheckMarkerTeamURLs_PartialFailureReportsCleanupAndProblems(t *testing.T) {
	sageoxDir := t.TempDir()
	write := func(name, content string, perm os.FileMode) {
		t.Helper()
		path := filepath.Join(sageoxDir, name)
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, perm); err != nil {
			t.Fatal(err)
		}
	}
	write(".repo_01abc", legacyTeamURLsMarker, 0644)
	write(".repo_02def", legacyTeamURLsMarker, 0444) // cannot be replaced
	write(".repo_03ghi", `{not json`, 0644)

	result := checkMarkerTeamURLsIn(sageoxDir, true)

	if !result.warning {
		t.Fatalf("want warning, got passed=%v (%s: %s)", result.passed, result.message, result.detail)
	}
	got := result.message + " " + result.detail
	for _, want := range []string{".repo_01abc", "commit .sageox/", ".repo_02def", ".repo_03ghi"} {
		if !strings.Contains(got, want) {
			t.Errorf("result missing %q: %s", want, got)
		}
	}
	if data, _ := os.ReadFile(filepath.Join(sageoxDir, ".repo_02def")); string(data) != legacyTeamURLsMarker {
		t.Errorf("marker that could not be replaced was changed:\n%s", data)
	}
}

func TestCheckMarkerTeamURLs_FindsMarkersFromGitRoot(t *testing.T) {
	t.Chdir(t.TempDir())
	if got := checkMarkerTeamURLs(true); !got.skipped {
		t.Errorf("outside a git repo: want skipped, got %+v", got)
	}

	repo := t.TempDir()
	cmd := exec.Command("git", "init", "-q")
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	t.Chdir(repo)
	if got := checkMarkerTeamURLs(true); !got.skipped {
		t.Errorf("without .sageox/: want skipped, got %+v", got)
	}

	sageoxDir := filepath.Join(repo, ".sageox")
	if err := os.Mkdir(sageoxDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sageoxDir, ".repo_01abc"), []byte(legacyTeamURLsMarker), 0644); err != nil {
		t.Fatal(err)
	}
	got := checkMarkerTeamURLs(true)
	if !got.passed || got.warning || !strings.Contains(got.message, "removed team_urls from .repo_01abc") {
		t.Errorf("with a legacy marker: want cleanup, got %+v", got)
	}
}

func TestCheckMarkerTeamURLs_MissingSageoxDirSkips(t *testing.T) {
	if got := checkMarkerTeamURLsIn(filepath.Join(t.TempDir(), ".sageox"), true); !got.skipped {
		t.Errorf("want skipped, got %+v", got)
	}
}

func TestCheckMarkerTeamURLs_SageoxPathErrorWarns(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0644); err != nil {
		t.Fatal(err)
	}
	if got := checkMarkerTeamURLsIn(filepath.Join(file, ".sageox"), true); !got.warning {
		t.Errorf("want warning for a path under a regular file, got %+v", got)
	}
}

func TestCheckMarkerTeamURLs_DetectionListsProblemsToo(t *testing.T) {
	sageoxDir := t.TempDir()
	for name, content := range map[string]string{".repo_01abc": legacyTeamURLsMarker, ".repo_02def": `{not json`, "config.json": `{}`} {
		if err := os.WriteFile(filepath.Join(sageoxDir, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	got := checkMarkerTeamURLsIn(sageoxDir, false)

	if !got.warning || !strings.Contains(got.message, ".repo_01abc") || !strings.Contains(got.detail, "could not read .repo_02def") {
		t.Errorf("want warning naming both markers, got %+v", got)
	}
}

func TestCheckMarkerTeamURLs_UnreadableFileWarns(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read a mode-0000 file")
	}
	sageoxDir := t.TempDir()
	path := filepath.Join(sageoxDir, ".repo_01abc")
	if err := os.WriteFile(path, []byte(legacyTeamURLsMarker), 0o000); err != nil {
		t.Fatal(err)
	}

	got := checkMarkerTeamURLsIn(sageoxDir, true)

	if !got.warning || !strings.Contains(got.message, "could not read .repo_01abc") {
		t.Errorf("want warning naming the unreadable marker, got %+v", got)
	}
}
