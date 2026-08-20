package format

import (
	"os"
	"path/filepath"
	"testing"
)

const discussionsRoot = "testdata/discussions"

func fixture(t *testing.T, folder string) string {
	t.Helper()
	return filepath.Join(discussionsRoot, folder)
}

func TestLoadManifest(t *testing.T) {
	tests := []struct {
		name         string
		folder       string
		wantNil      bool
		wantTitle    string
		wantID       string
		wantWarnings []string
		wantPauses   int
	}{
		{
			name:      "layers.json name, empty title valid, empty pauses",
			folder:    "2026-08-11-22-32-full",
			wantTitle: "",
			wantID:    "cnv_019ff2f5-2079-7be1-b05e-8caad2772e61",
		},
		{
			name:      "conversation.json spec name, null pauses",
			folder:    "2026-08-13-01-00-conv-manifest",
			wantTitle: "Spec-Name Manifest",
			wantID:    "cnv_019ff394-7db1-7473-bf86-0667a3816c30",
		},
		{
			name:         "both names present prefers layers.json with warning",
			folder:       "2026-08-14-01-00-both-manifests",
			wantTitle:    "From layers.json",
			wantID:       "cnv_019ff400-0000-7000-8000-000000000001",
			wantWarnings: []string{WarnBothManifestNames},
		},
		{
			name:    "legacy folder without any manifest",
			folder:  "2026-08-12-01-00-legacy",
			wantNil: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, warnings, err := LoadManifest(fixture(t, tt.folder))
			if err != nil {
				t.Fatalf("LoadManifest: %v", err)
			}
			if tt.wantNil {
				if m != nil {
					t.Fatalf("want nil manifest, got %+v", m)
				}
				return
			}
			if m == nil {
				t.Fatal("want manifest, got nil")
			}
			if m.Title != tt.wantTitle {
				t.Errorf("Title = %q, want %q", m.Title, tt.wantTitle)
			}
			if m.ID != tt.wantID {
				t.Errorf("ID = %q, want %q", m.ID, tt.wantID)
			}
			if len(m.Clock.Pauses) != tt.wantPauses {
				t.Errorf("Pauses = %d, want %d", len(m.Clock.Pauses), tt.wantPauses)
			}
			if len(warnings) != len(tt.wantWarnings) {
				t.Fatalf("warnings = %v, want %v", warnings, tt.wantWarnings)
			}
			for i := range warnings {
				if warnings[i] != tt.wantWarnings[i] {
					t.Errorf("warnings[%d] = %q, want %q", i, warnings[i], tt.wantWarnings[i])
				}
			}
		})
	}
}

func TestLoadManifestSchemaVersion(t *testing.T) {
	tests := []struct {
		name    string
		content string
		wantErr bool
	}{
		{name: "absent schema_version is legacy-legal", content: `{"id":"cnv_x","title":"t"}`},
		{name: "explicit zero is invalid", content: `{"$schema_version":0,"id":"cnv_x"}`, wantErr: true},
		{name: "explicit one is valid", content: `{"$schema_version":1,"id":"cnv_x"}`},
		{name: "malformed json errors", content: `{nope`, wantErr: true},
		{name: "pauses null tolerated", content: `{"id":"cnv_x","clock":{"t0":"","pauses":null}}`},
		{name: "pauses list tolerated", content: `{"id":"cnv_x","clock":{"pauses":[{"paused_at":"a","resumed_at":"b"}]}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, ManifestNameLayers), []byte(tt.content), 0o644); err != nil {
				t.Fatal(err)
			}
			m, _, err := LoadManifest(dir)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("want error, got manifest %+v", m)
				}
				return
			}
			if err != nil {
				t.Fatalf("LoadManifest: %v", err)
			}
			if m == nil {
				t.Fatal("want manifest, got nil")
			}
		})
	}
}
