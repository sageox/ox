package main

import (
	"encoding/json"
	"os"
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
