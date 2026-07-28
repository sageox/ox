package plan

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestVerifyCorpusComplete(t *testing.T) {
	tests := []struct {
		name        string
		localCount  int
		remoteCount int
		remoteKnown bool
		wantAbort   bool
		wantNote    bool
	}{
		{name: "complete corpus: local equals remote", localCount: 5, remoteCount: 5, remoteKnown: true, wantAbort: false, wantNote: false},
		{name: "incomplete corpus: local behind remote aborts", localCount: 1, remoteCount: 25, remoteKnown: true, wantAbort: true},
		{name: "local ahead of remote proceeds with a note", localCount: 3, remoteCount: 2, remoteKnown: true, wantAbort: false, wantNote: true},
		{name: "unknown remote skips the guard entirely", localCount: 0, remoteCount: 0, remoteKnown: false, wantAbort: false, wantNote: true},
		{name: "unknown remote skips even when counts would otherwise abort", localCount: 0, remoteCount: 99, remoteKnown: false, wantAbort: false, wantNote: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			note, err := VerifyCorpusComplete(tt.localCount, tt.remoteCount, tt.remoteKnown)

			if tt.wantAbort {
				if err == nil {
					t.Fatalf("VerifyCorpusComplete(%d, %d, %v) = nil error, want abort", tt.localCount, tt.remoteCount, tt.remoteKnown)
				}
				var cerr *CorpusCompletenessError
				if !errors.As(err, &cerr) {
					t.Fatalf("error = %v (%T), want *CorpusCompletenessError", err, err)
				}
				if cerr.LocalCount != tt.localCount || cerr.RemoteCount != tt.remoteCount {
					t.Errorf("CorpusCompletenessError = {%d, %d}, want {%d, %d}", cerr.LocalCount, cerr.RemoteCount, tt.localCount, tt.remoteCount)
				}
				return
			}
			if err != nil {
				t.Fatalf("VerifyCorpusComplete(%d, %d, %v) unexpected error: %v", tt.localCount, tt.remoteCount, tt.remoteKnown, err)
			}
			if tt.wantNote && note == "" {
				t.Errorf("expected a non-empty note, got none")
			}
			if !tt.wantNote && note != "" {
				t.Errorf("expected no note, got %q", note)
			}
		})
	}
}

func TestVerifyNotDiverged(t *testing.T) {
	tests := []struct {
		name          string
		ahead, behind int
		remoteKnown   bool
		allowDiverged bool
		wantAbort     bool
		wantNote      bool
	}{
		{name: "in sync: no note, no abort", ahead: 0, behind: 0, remoteKnown: true, wantAbort: false, wantNote: false},
		{name: "ahead only aborts without the flag", ahead: 3, behind: 0, remoteKnown: true, wantAbort: true},
		{name: "behind only aborts without the flag", ahead: 0, behind: 5, remoteKnown: true, wantAbort: true},
		{name: "diverged both directions aborts without the flag", ahead: 343, behind: 1056, remoteKnown: true, wantAbort: true},
		{name: "allow-diverged overrides the abort and notes it", ahead: 343, behind: 1056, remoteKnown: true, allowDiverged: true, wantAbort: false, wantNote: true},
		{name: "unknown remote skips the guard entirely", ahead: 0, behind: 0, remoteKnown: false, wantAbort: false, wantNote: true},
		{name: "unknown remote skips even when counts would otherwise abort", ahead: 10, behind: 10, remoteKnown: false, wantAbort: false, wantNote: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			note, err := VerifyNotDiverged(tt.ahead, tt.behind, tt.remoteKnown, tt.allowDiverged)

			if tt.wantAbort {
				if err == nil {
					t.Fatalf("VerifyNotDiverged(%d, %d, %v, %v) = nil error, want abort", tt.ahead, tt.behind, tt.remoteKnown, tt.allowDiverged)
				}
				var derr *DivergenceError
				if !errors.As(err, &derr) {
					t.Fatalf("error = %v (%T), want *DivergenceError", err, err)
				}
				if derr.Ahead != tt.ahead || derr.Behind != tt.behind {
					t.Errorf("DivergenceError = {%d, %d}, want {%d, %d}", derr.Ahead, derr.Behind, tt.ahead, tt.behind)
				}
				return
			}
			if err != nil {
				t.Fatalf("VerifyNotDiverged(%d, %d, %v, %v) unexpected error: %v", tt.ahead, tt.behind, tt.remoteKnown, tt.allowDiverged, err)
			}
			if tt.wantNote && note == "" {
				t.Errorf("expected a non-empty note, got none")
			}
			if !tt.wantNote && note != "" {
				t.Errorf("expected no note, got %q", note)
			}
		})
	}
}

func TestCountPlanDirs(t *testing.T) {
	t.Run("nonexistent plansDir counts as zero, not an error", func(t *testing.T) {
		t.Parallel()
		count, err := CountPlanDirs(filepath.Join(t.TempDir(), "does-not-exist"))
		if err != nil {
			t.Fatalf("CountPlanDirs: %v", err)
		}
		if count != 0 {
			t.Errorf("count = %d, want 0", count)
		}
	})

	t.Run("counts only directories, ignores files", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		for _, name := range []string{"2026-05-01-plan-a", "2026-05-02-plan-b"} {
			if err := os.MkdirAll(filepath.Join(dir, name), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(dir, "stray-file.txt"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}

		count, err := CountPlanDirs(dir)
		if err != nil {
			t.Fatalf("CountPlanDirs: %v", err)
		}
		if count != 2 {
			t.Errorf("count = %d, want 2 (files must not be counted)", count)
		}
	})
}
