package memoryimport

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNeedsMerge(t *testing.T) {
	now := time.Now()

	t.Run("first import ever", func(t *testing.T) {
		state := &MemoryImportState{}
		if state.NeedsMerge("-Users-ryan-Code-ox") {
			t.Error("first import should not need merge")
		}
	})

	t.Run("same single source", func(t *testing.T) {
		state := &MemoryImportState{}
		state.RecordImport("-Users-ryan-Code-ox", now)
		if state.NeedsMerge("-Users-ryan-Code-ox") {
			t.Error("same source should not need merge")
		}
	})

	t.Run("different single source", func(t *testing.T) {
		state := &MemoryImportState{}
		state.RecordImport("-Users-ryan-Code-ox", now)
		if !state.NeedsMerge("-Users-ryan-conductor-workspaces-ox-v2") {
			t.Error("different source should need merge")
		}
	})

	t.Run("multiple sources always merge", func(t *testing.T) {
		state := &MemoryImportState{}
		state.RecordImport("-Users-ryan-Code-ox", now)
		state.RecordImport("-Users-ryan-conductor-workspaces-ox-v2", now)
		if !state.NeedsMerge("-Users-ryan-Code-ox") {
			t.Error("multiple sources seen — should always need merge")
		}
	})

	t.Run("multiple sources with new hash", func(t *testing.T) {
		state := &MemoryImportState{}
		state.RecordImport("-hash-a", now)
		state.RecordImport("-hash-b", now)
		if !state.NeedsMerge("-hash-c") {
			t.Error("multiple sources seen — should need merge even for new hash")
		}
	})
}

func TestRecordImport(t *testing.T) {
	state := &MemoryImportState{}
	now := time.Now()

	state.RecordImport("-hash-a", now)
	if state.SourceCount() != 1 {
		t.Fatalf("expected 1 source, got %d", state.SourceCount())
	}

	// same source updates record, doesn't add duplicate
	state.RecordImport("-hash-a", now.Add(time.Hour))
	if state.SourceCount() != 1 {
		t.Fatalf("expected 1 source after re-import, got %d", state.SourceCount())
	}

	state.RecordImport("-hash-b", now)
	if state.SourceCount() != 2 {
		t.Fatalf("expected 2 sources, got %d", state.SourceCount())
	}
}

func TestSourceChanged(t *testing.T) {
	state := &MemoryImportState{}
	now := time.Now()

	// never imported → changed
	if !state.SourceChanged("-hash-a", now) {
		t.Error("never-imported source should be considered changed")
	}

	state.RecordImport("-hash-a", now)

	// same mtime → not changed
	if state.SourceChanged("-hash-a", now) {
		t.Error("same mtime should not be considered changed")
	}

	// newer mtime → changed
	if !state.SourceChanged("-hash-a", now.Add(time.Second)) {
		t.Error("newer mtime should be considered changed")
	}

	// older mtime → not changed
	if state.SourceChanged("-hash-a", now.Add(-time.Second)) {
		t.Error("older mtime should not be considered changed")
	}
}

func TestPruneExpired(t *testing.T) {
	state := &MemoryImportState{}
	now := time.Now()

	// record two sources: one recent, one old
	state.RecordImport("-recent", now)
	// manually backdate one source
	fpOld := sourceFingerprint("-old-workspace")
	if state.Sources == nil {
		state.Sources = make(map[string]*SourceRecord)
	}
	state.Sources[fpOld] = &SourceRecord{
		LastImport: now.Add(-8 * 24 * time.Hour), // 8 days ago
		LastMtime:  now.Add(-8 * 24 * time.Hour),
	}

	if state.SourceCount() != 2 {
		t.Fatalf("expected 2 sources before prune, got %d", state.SourceCount())
	}

	pruned := state.PruneExpired(SourceTTL)
	if pruned != 1 {
		t.Errorf("expected 1 pruned, got %d", pruned)
	}
	if state.SourceCount() != 1 {
		t.Errorf("expected 1 source after prune, got %d", state.SourceCount())
	}

	// after pruning the old workspace, only one source remains
	// so NeedsMerge should return false for the remaining source
	if state.NeedsMerge("-recent") {
		t.Error("after pruning expired source, single remaining source should not need merge")
	}
}

func TestSourceFingerprint_opaque(t *testing.T) {
	fp := sourceFingerprint("-Users-ryan-Code-ox")
	if len(fp) != 16 {
		t.Errorf("expected 16 hex chars, got %d: %q", len(fp), fp)
	}
	if fp == "-Users-ryan-Code-ox" {
		t.Error("fingerprint should be opaque, not the raw hash")
	}

	// deterministic
	fp2 := sourceFingerprint("-Users-ryan-Code-ox")
	if fp != fp2 {
		t.Errorf("fingerprint not deterministic: %q != %q", fp, fp2)
	}

	// different input → different output
	fp3 := sourceFingerprint("-Users-ryan-conductor-workspaces-ox-v2")
	if fp == fp3 {
		t.Error("different inputs should produce different fingerprints")
	}
}

func TestLoadSaveImportState(t *testing.T) {
	tmpDir := t.TempDir()
	now := time.Now()

	// load from empty dir returns empty state
	state := LoadImportState(tmpDir)
	if state.SourceCount() != 0 {
		t.Fatalf("expected 0 sources, got %d", state.SourceCount())
	}

	// record imports and save
	state.RecordImport("-hash-a", now)
	state.RecordImport("-hash-b", now)

	if err := SaveImportState(tmpDir, state); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded := LoadImportState(tmpDir)
	if loaded.SourceCount() != 2 {
		t.Fatalf("expected 2 sources, got %d", loaded.SourceCount())
	}

	// verify file is in .sageox/cache/
	stateFile := filepath.Join(tmpDir, ".sageox", "cache", "memory-import-state.json")
	if _, err := os.Stat(stateFile); os.IsNotExist(err) {
		t.Errorf("state file not found at expected path: %s", stateFile)
	}
}

func TestLoadImportState_corrupt(t *testing.T) {
	tmpDir := t.TempDir()
	dir := filepath.Join(tmpDir, ".sageox", "cache")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "memory-import-state.json"), []byte("invalid json"), 0644); err != nil {
		t.Fatal(err)
	}

	state := LoadImportState(tmpDir)
	if state.SourceCount() != 0 {
		t.Fatalf("corrupt file should return empty state, got %d sources", state.SourceCount())
	}
}

func TestValidateOpts(t *testing.T) {
	tests := []struct {
		name    string
		opts    ImportOptions
		wantErr bool
	}{
		{
			name: "valid",
			opts: ImportOptions{
				LedgerPath:  "/tmp/ledger",
				UserID:      "user123",
				AgentType:   "claude",
				ProjectHash: "-hash",
			},
			wantErr: false,
		},
		{
			name:    "missing ledger",
			opts:    ImportOptions{UserID: "user123", AgentType: "claude", ProjectHash: "-hash"},
			wantErr: true,
		},
		{
			name:    "missing user",
			opts:    ImportOptions{LedgerPath: "/tmp/ledger", AgentType: "claude", ProjectHash: "-hash"},
			wantErr: true,
		},
		{
			name:    "missing agent type",
			opts:    ImportOptions{LedgerPath: "/tmp/ledger", UserID: "user123", ProjectHash: "-hash"},
			wantErr: true,
		},
		{
			name:    "missing hash",
			opts:    ImportOptions{LedgerPath: "/tmp/ledger", UserID: "user123", AgentType: "claude"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateOpts(tt.opts)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateOpts() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestWriteMemoryFiles(t *testing.T) {
	tmpDir := t.TempDir()
	targetDir := filepath.Join(tmpDir, "data", "claude", "memory", "user123")

	files := map[string][]byte{
		"MEMORY.md":    []byte("# Memory\nSome learnings"),
		"debugging.md": []byte("# Debugging tips"),
	}

	result, err := writeMemoryFiles(targetDir, files)
	if err != nil {
		t.Fatalf("writeMemoryFiles: %v", err)
	}
	if result.FilesWritten != 2 {
		t.Errorf("FilesWritten = %d, want 2", result.FilesWritten)
	}

	// verify files exist
	for name, expected := range files {
		content, err := os.ReadFile(filepath.Join(targetDir, name))
		if err != nil {
			t.Errorf("read %s: %v", name, err)
			continue
		}
		if string(content) != string(expected) {
			t.Errorf("%s content = %q, want %q", name, content, expected)
		}
	}
}

func TestReadLedgerMemory(t *testing.T) {
	tmpDir := t.TempDir()

	// non-existent returns empty
	files, err := readLedgerMemory(filepath.Join(tmpDir, "nonexistent"))
	if err != nil {
		t.Fatalf("non-existent: %v", err)
	}
	if len(files) != 0 {
		t.Errorf("expected empty, got %d files", len(files))
	}

	// create some files
	targetDir := filepath.Join(tmpDir, "memory")
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(targetDir, "MEMORY.md"), []byte("# test"), 0644); err != nil {
		t.Fatal(err)
	}

	files, err = readLedgerMemory(targetDir)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}
	if string(files["MEMORY.md"]) != "# test" {
		t.Errorf("content = %q", files["MEMORY.md"])
	}
}

func TestSourceMtime(t *testing.T) {
	tmpDir := t.TempDir()

	// write a file with known time
	f := filepath.Join(tmpDir, "test.md")
	if err := os.WriteFile(f, []byte("content"), 0644); err != nil {
		t.Fatal(err)
	}

	mtime, err := SourceMtime(tmpDir)
	if err != nil {
		t.Fatalf("SourceMtime: %v", err)
	}

	if time.Since(mtime) > 5*time.Second {
		t.Errorf("mtime too old: %v", mtime)
	}
}

// --- ImportMemory integration tests (DryRun to avoid git operations) ---

func TestImportMemory_CopyPath_FirstImport(t *testing.T) {
	// first import ever: no prior state → copy strategy, no merge called
	ledger := t.TempDir()
	sourceDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(sourceDir, "MEMORY.md"), []byte("# test"), 0644); err != nil {
		t.Fatal(err)
	}

	mergeCalled := false
	mockMerge := func(_ context.Context, _, _ map[string][]byte) (map[string][]byte, error) {
		mergeCalled = true
		return nil, fmt.Errorf("should not be called")
	}

	result, err := ImportMemory(context.Background(), ImportOptions{
		LedgerPath:  ledger,
		UserID:      "user1",
		AgentType:   "claude",
		ProjectHash: "-hash-a",
		SourceDir:   sourceDir,
		SourceFiles: map[string][]byte{"MEMORY.md": []byte("# test")},
		DryRun:      true,
		MergeFunc:   mockMerge,
	})
	if err != nil {
		t.Fatalf("ImportMemory: %v", err)
	}

	if result.Strategy != "copy" {
		t.Errorf("strategy = %q, want copy", result.Strategy)
	}
	if result.FilesWritten != 1 {
		t.Errorf("FilesWritten = %d, want 1", result.FilesWritten)
	}
	if mergeCalled {
		t.Error("MergeFunc should not be called for first import (copy path)")
	}
}

func TestImportMemory_CopyPath_SameSource(t *testing.T) {
	// single source re-importing → copy strategy, no merge
	ledger := t.TempDir()
	sourceDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(sourceDir, "MEMORY.md"), []byte("# updated"), 0644); err != nil {
		t.Fatal(err)
	}

	// seed state with one prior import from the same source
	state := &MemoryImportState{}
	state.RecordImport("-hash-a", time.Now())
	if err := SaveImportState(ledger, state); err != nil {
		t.Fatal(err)
	}

	mergeCalled := false
	mockMerge := func(_ context.Context, _, _ map[string][]byte) (map[string][]byte, error) {
		mergeCalled = true
		return nil, fmt.Errorf("should not be called")
	}

	result, err := ImportMemory(context.Background(), ImportOptions{
		LedgerPath:  ledger,
		UserID:      "user1",
		AgentType:   "claude",
		ProjectHash: "-hash-a", // same source
		SourceDir:   sourceDir,
		SourceFiles: map[string][]byte{"MEMORY.md": []byte("# updated")},
		DryRun:      true,
		MergeFunc:   mockMerge,
	})
	if err != nil {
		t.Fatalf("ImportMemory: %v", err)
	}

	if result.Strategy != "copy" {
		t.Errorf("strategy = %q, want copy", result.Strategy)
	}
	if mergeCalled {
		t.Error("MergeFunc should not be called for same-source re-import")
	}
}

func TestImportMemory_MergePath_DifferentSources(t *testing.T) {
	// two sources recorded → merge strategy; MergeFunc receives correct args
	ledger := t.TempDir()
	sourceDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(sourceDir, "MEMORY.md"), []byte("# new"), 0644); err != nil {
		t.Fatal(err)
	}

	// seed state with two prior sources → NeedsMerge returns true
	state := &MemoryImportState{}
	state.RecordImport("-hash-a", time.Now())
	state.RecordImport("-hash-b", time.Now())
	if err := SaveImportState(ledger, state); err != nil {
		t.Fatal(err)
	}

	// write existing ledger files so merge has something to work with
	targetDir := filepath.Join(ledger, "data", "claude", "memory", "user1")
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(targetDir, "MEMORY.md"), []byte("# existing"), 0644); err != nil {
		t.Fatal(err)
	}

	var gotExisting, gotNew map[string][]byte
	mockMerge := func(_ context.Context, existing, newFiles map[string][]byte) (map[string][]byte, error) {
		gotExisting = existing
		gotNew = newFiles
		// simulate merge: return new files (favor fresh)
		return newFiles, nil
	}

	result, err := ImportMemory(context.Background(), ImportOptions{
		LedgerPath:  ledger,
		UserID:      "user1",
		AgentType:   "claude",
		ProjectHash: "-hash-a",
		SourceDir:   sourceDir,
		SourceFiles: map[string][]byte{"MEMORY.md": []byte("# new")},
		DryRun:      true,
		MergeFunc:   mockMerge,
	})
	if err != nil {
		t.Fatalf("ImportMemory: %v", err)
	}

	if result.Strategy != "merge" {
		t.Errorf("strategy = %q, want merge", result.Strategy)
	}

	// verify MergeFunc received correct existing files
	if string(gotExisting["MEMORY.md"]) != "# existing" {
		t.Errorf("existing MEMORY.md = %q, want %q", gotExisting["MEMORY.md"], "# existing")
	}

	// verify MergeFunc received correct new files
	if string(gotNew["MEMORY.md"]) != "# new" {
		t.Errorf("new MEMORY.md = %q, want %q", gotNew["MEMORY.md"], "# new")
	}
}

func TestImportMemory_NilMergeFunc_FallsToCopy(t *testing.T) {
	// merge needed (two sources) but MergeFunc is nil → falls back to copy
	ledger := t.TempDir()
	sourceDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(sourceDir, "MEMORY.md"), []byte("# new"), 0644); err != nil {
		t.Fatal(err)
	}

	// seed state with two sources → NeedsMerge returns true
	state := &MemoryImportState{}
	state.RecordImport("-hash-a", time.Now())
	state.RecordImport("-hash-b", time.Now())
	if err := SaveImportState(ledger, state); err != nil {
		t.Fatal(err)
	}

	result, err := ImportMemory(context.Background(), ImportOptions{
		LedgerPath:  ledger,
		UserID:      "user1",
		AgentType:   "claude",
		ProjectHash: "-hash-a",
		SourceDir:   sourceDir,
		SourceFiles: map[string][]byte{"MEMORY.md": []byte("# new")},
		DryRun:      true,
		MergeFunc:   nil, // no merge function → copy
	})
	if err != nil {
		t.Fatalf("ImportMemory: %v", err)
	}

	if result.Strategy != "copy" {
		t.Errorf("strategy = %q, want copy (nil MergeFunc fallback)", result.Strategy)
	}
	if result.FilesWritten != 1 {
		t.Errorf("FilesWritten = %d, want 1", result.FilesWritten)
	}
}

func TestImportMemory_MergeWithEmptyLedger(t *testing.T) {
	// merge needed but ledger has no existing files → falls to copy
	ledger := t.TempDir()
	sourceDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(sourceDir, "MEMORY.md"), []byte("# new"), 0644); err != nil {
		t.Fatal(err)
	}

	// seed state with two sources → merge needed
	state := &MemoryImportState{}
	state.RecordImport("-hash-a", time.Now())
	state.RecordImport("-hash-b", time.Now())
	if err := SaveImportState(ledger, state); err != nil {
		t.Fatal(err)
	}

	// do NOT create any files in the ledger target dir

	mergeCalled := false
	mockMerge := func(_ context.Context, _, _ map[string][]byte) (map[string][]byte, error) {
		mergeCalled = true
		return nil, fmt.Errorf("should not be called")
	}

	result, err := ImportMemory(context.Background(), ImportOptions{
		LedgerPath:  ledger,
		UserID:      "user1",
		AgentType:   "claude",
		ProjectHash: "-hash-a",
		SourceDir:   sourceDir,
		SourceFiles: map[string][]byte{"MEMORY.md": []byte("# new")},
		DryRun:      true,
		MergeFunc:   mockMerge,
	})
	if err != nil {
		t.Fatalf("ImportMemory: %v", err)
	}

	// empty ledger → straight copy even though merge was needed
	if result.Strategy != "copy" {
		t.Errorf("strategy = %q, want copy (empty ledger fallback)", result.Strategy)
	}
	if mergeCalled {
		t.Error("MergeFunc should not be called when ledger has no existing files")
	}
}

func TestImportMemory_EmptySourceFiles(t *testing.T) {
	result, err := ImportMemory(context.Background(), ImportOptions{
		LedgerPath:  "/tmp/ledger",
		UserID:      "user1",
		AgentType:   "claude",
		ProjectHash: "-hash",
		SourceFiles: map[string][]byte{},
		DryRun:      true,
	})
	if err != nil {
		t.Fatalf("ImportMemory: %v", err)
	}
	if result.Strategy != "copy" {
		t.Errorf("strategy = %q, want copy", result.Strategy)
	}
	if result.FilesWritten != 0 {
		t.Errorf("FilesWritten = %d, want 0", result.FilesWritten)
	}
}

func TestImportMemory_MergeFuncError(t *testing.T) {
	// merge fails → ImportMemory returns error
	ledger := t.TempDir()
	sourceDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(sourceDir, "MEMORY.md"), []byte("# new"), 0644); err != nil {
		t.Fatal(err)
	}

	// seed two sources → merge needed
	state := &MemoryImportState{}
	state.RecordImport("-hash-a", time.Now())
	state.RecordImport("-hash-b", time.Now())
	if err := SaveImportState(ledger, state); err != nil {
		t.Fatal(err)
	}

	// write existing ledger files so merge path is taken
	targetDir := filepath.Join(ledger, "data", "claude", "memory", "user1")
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(targetDir, "MEMORY.md"), []byte("# old"), 0644); err != nil {
		t.Fatal(err)
	}

	mockMerge := func(_ context.Context, _, _ map[string][]byte) (map[string][]byte, error) {
		return nil, fmt.Errorf("LLM unavailable")
	}

	_, err := ImportMemory(context.Background(), ImportOptions{
		LedgerPath:  ledger,
		UserID:      "user1",
		AgentType:   "claude",
		ProjectHash: "-hash-a",
		SourceDir:   sourceDir,
		SourceFiles: map[string][]byte{"MEMORY.md": []byte("# new")},
		DryRun:      true,
		MergeFunc:   mockMerge,
	})
	if err == nil {
		t.Fatal("expected error from failed MergeFunc")
	}
	if got := err.Error(); got != "merge memory: LLM unavailable" {
		t.Errorf("error = %q, want %q", got, "merge memory: LLM unavailable")
	}
}

// TestImportState_FreshAfterReclone documents the blue/green GC behavior:
// when the ledger is recloned, the .sageox/cache/ state file is lost (local-only,
// gitignored). LoadImportState returns empty state, so the first import after
// reclone always takes the cheap copy path — which is correct because committed
// memory files persist in the ledger git repo and will be read if merge is needed.
func TestImportState_FreshAfterReclone(t *testing.T) {
	// simulate reclone: fresh directory with no .sageox/cache/
	freshLedger := t.TempDir()

	state := LoadImportState(freshLedger)
	if state.SourceCount() != 0 {
		t.Fatalf("expected 0 sources after reclone, got %d", state.SourceCount())
	}

	// first import into fresh ledger → copy (not merge)
	if state.NeedsMerge("-any-hash") {
		t.Error("fresh state (post-reclone) should not need merge")
	}

	// even though state was lost, the memory data files persist in the git repo.
	// if a second source imports later, NeedsMerge kicks in correctly.
	state.RecordImport("-hash-a", time.Now())
	if state.NeedsMerge("-hash-a") {
		t.Error("same source should not need merge")
	}
	if !state.NeedsMerge("-hash-b") {
		t.Error("different source should trigger merge")
	}
}

func TestDryRunSummary(t *testing.T) {
	result := &ImportResult{
		Strategy:     "merge",
		FilesWritten: 2,
		BytesWritten: 150,
	}
	files := map[string][]byte{
		"MEMORY.md":    []byte("# Memory\nSome learnings"),
		"debugging.md": []byte("# Debug tips"),
	}

	summary := DryRunSummary(result, files)
	if summary == "" {
		t.Fatal("empty summary")
	}

	// verify key fields present
	for _, want := range []string{"Strategy: merge", "Files: 2", "Bytes: 150"} {
		if !strings.Contains(summary, want) {
			t.Errorf("summary missing %q:\n%s", want, summary)
		}
	}
}
