package ledger

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Unit tests for parseDirtyDirsFromPorcelain — validates parsing of
// "git status --porcelain -z" output without needing a real git repo.
func TestParseDirtyDirsFromPorcelain(t *testing.T) {
	t.Parallel()

	// helper: join tokens with NUL to simulate porcelain -z output
	nul := "\x00"

	tests := []struct {
		name     string
		output   string
		coneDirs []string
		want     []string
	}{
		{
			name:   "empty output",
			output: "",
			want:   nil,
		},
		{
			name:     "single modified file",
			output:   " M data/custom/file.csv" + nul,
			coneDirs: []string{"sessions"},
			want:     []string{"data"},
		},
		{
			name:     "modified file already in cone",
			output:   " M sessions/abc/raw.jsonl" + nul,
			coneDirs: []string{"sessions"},
			want:     nil,
		},
		{
			name:     "staged new file",
			output:   "A  data/linear/import.csv" + nul,
			coneDirs: []string{"sessions", ".sageox"},
			want:     []string{"data"},
		},
		{
			name:     "multiple files in different dirs",
			output:   " M data/custom/a.csv" + nul + "A  imports/b.json" + nul,
			coneDirs: []string{"sessions"},
			want:     []string{"data", "imports"},
		},
		{
			name:     "deduplicates same top-level dir",
			output:   " M data/custom/a.csv" + nul + " M data/linear/b.csv" + nul,
			coneDirs: []string{"sessions"},
			want:     []string{"data"},
		},
		{
			name: "rename entry: both new and orig dirs detected",
			// "R  archive/batch1/data.csv\0imports/batch1/data.csv\0"
			output:   "R  archive/batch1/data.csv" + nul + "imports/batch1/data.csv" + nul,
			coneDirs: []string{"sessions"},
			want:     []string{"archive", "imports"},
		},
		{
			name: "rename where dest is in cone, orig is not",
			// dest (sessions/) already in cone, but orig (imports/) is not
			output:   "R  sessions/renamed.csv" + nul + "imports/original.csv" + nul,
			coneDirs: []string{"sessions"},
			want:     []string{"imports"},
		},
		{
			name:     "rename where orig is in cone, dest is not",
			output:   "R  archive/moved.csv" + nul + "sessions/original.csv" + nul,
			coneDirs: []string{"sessions"},
			want:     []string{"archive"},
		},
		{
			name:     "copy entry: both paths detected",
			output:   "C  backup/data.csv" + nul + "data/custom/data.csv" + nul,
			coneDirs: []string{"sessions"},
			want:     []string{"backup", "data"},
		},
		{
			name:     "rename followed by normal entry",
			output:   "R  archive/f.csv" + nul + "imports/f.csv" + nul + " M data/other.json" + nul,
			coneDirs: []string{"sessions"},
			want:     []string{"archive", "imports", "data"},
		},
		{
			name: "multiple renames",
			output: "R  dest1/a.csv" + nul + "src1/a.csv" + nul +
				"R  dest2/b.csv" + nul + "src2/b.csv" + nul,
			coneDirs: []string{"sessions"},
			want:     []string{"dest1", "src1", "dest2", "src2"},
		},
		{
			name:     "root-level file (no slash)",
			output:   " M README.md" + nul,
			coneDirs: []string{"sessions"},
			want:     []string{"README.md"},
		},
		{
			name: "rename with short orig path",
			// orig path "ab" is only 2 chars — must still be parsed as bare path
			output:   "R  archive/f.csv" + nul + "ab" + nul,
			coneDirs: []string{"sessions"},
			want:     []string{"archive", "ab"},
		},
		{
			name: "deep cone entry covers dirty file",
			// dirty file at data/github/2026/03/30/prs.json is already covered
			// by cone entry "data/github/2026/03/30/" — should NOT add bare "data"
			output:   " M data/github/2026/03/30/prs.json" + nul,
			coneDirs: []string{"sessions", "data/github/2026/03/30/"},
			want:     nil,
		},
		{
			name:     "deep cone entry with trailing slash covers file",
			output:   " M data/murmurs/2026/03/30/12/whisper.json" + nul,
			coneDirs: []string{"sessions", "data/murmurs/2026/03/30/12/"},
			want:     nil,
		},
		{
			name: "deep cone covers one file but not another",
			// first file covered by deep cone, second is not
			output:   " M data/github/2026/03/30/prs.json" + nul + " M data/custom/report.csv" + nul,
			coneDirs: []string{"sessions", "data/github/2026/03/30/"},
			want:     []string{"data"},
		},
		{
			name:     "shallow cone entry covers deep dirty file",
			output:   " M data/github/2025/01/01/old.json" + nul,
			coneDirs: []string{"sessions", "data"},
			want:     nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := parseDirtyDirsFromPorcelain([]byte(tt.output), tt.coneDirs)
			if len(tt.want) == 0 {
				if len(got) != 0 {
					t.Errorf("want nil/empty, got %v", got)
				}
				return
			}
			if len(got) != len(tt.want) {
				t.Fatalf("want %v, got %v", tt.want, got)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Errorf("index %d: want %q, got %q", i, tt.want[i], got[i])
				}
			}
		})
	}
}

func TestMostRecentMurmurTime(t *testing.T) {
	tmpDir := t.TempDir()
	now := time.Now().UTC()
	hourDir := filepath.Join(tmpDir, "data", "murmurs", now.Format("2006-01-02"), fmt.Sprintf("%02d", now.Hour()))
	if err := os.MkdirAll(hourDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// write a murmur file
	m := MurmurFile{
		ID:        "test-1",
		Timestamp: now.Add(-2 * time.Second),
		AgentID:   "agent-1",
		Topic:     "test",
		Content:   "hello",
	}
	data, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(hourDir, "test-1.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	// should find the murmur
	got := MostRecentMurmurTime(tmpDir, "agent-1")
	if got.IsZero() {
		t.Fatal("expected non-zero time")
	}

	// different agent should not find it
	got2 := MostRecentMurmurTime(tmpDir, "agent-2")
	if !got2.IsZero() {
		t.Fatal("expected zero time for different agent")
	}

	// empty dir should return zero
	got3 := MostRecentMurmurTime(t.TempDir(), "agent-1")
	if !got3.IsZero() {
		t.Fatal("expected zero time for empty dir")
	}
}

func TestDefaultMurmurWindowHours(t *testing.T) {
	if DefaultMurmurWindowHours != 12 {
		t.Errorf("DefaultMurmurWindowHours = %d, want 12", DefaultMurmurWindowHours)
	}
}
