package daemon

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	whisperstore "github.com/sageox/ox/internal/whisper/store"
	"github.com/stretchr/testify/require"
)

// --- DBMaintenanceScheduler tests ---

type mockMaintainer struct {
	name   string
	result DBMaintenanceResult
	called atomic.Int32
}

func (m *mockMaintainer) Name() string { return m.name }
func (m *mockMaintainer) Maintain(_ context.Context) DBMaintenanceResult {
	m.called.Add(1)
	return m.result
}

func TestSchedulerRunAll(t *testing.T) {
	s := NewDBMaintenanceScheduler(nil)

	m1 := &mockMaintainer{name: "db1", result: DBMaintenanceResult{Name: "db1", Pruned: 5, Healthy: true}}
	m2 := &mockMaintainer{name: "db2", result: DBMaintenanceResult{Name: "db2", Pruned: 0, Healthy: true}}

	s.Register(m1)
	s.Register(m2)

	results := s.RunAll(context.Background())

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if m1.called.Load() != 1 {
		t.Errorf("expected db1 called once, got %d", m1.called.Load())
	}
	if m2.called.Load() != 1 {
		t.Errorf("expected db2 called once, got %d", m2.called.Load())
	}
	if results[0].Pruned != 5 {
		t.Errorf("expected db1 pruned=5, got %d", results[0].Pruned)
	}
}

func TestSchedulerRunAllCancellation(t *testing.T) {
	s := NewDBMaintenanceScheduler(nil)

	m1 := &mockMaintainer{name: "db1", result: DBMaintenanceResult{Name: "db1", Healthy: true}}
	m2 := &mockMaintainer{name: "db2", result: DBMaintenanceResult{Name: "db2", Healthy: true}}

	s.Register(m1)
	s.Register(m2)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	results := s.RunAll(ctx)

	// canceled immediately — should not run any maintainers
	if len(results) != 0 {
		t.Errorf("expected 0 results on canceled ctx, got %d", len(results))
	}
}

func TestSchedulerRunOne(t *testing.T) {
	s := NewDBMaintenanceScheduler(nil)

	m1 := &mockMaintainer{name: "whisper", result: DBMaintenanceResult{Name: "whisper", Pruned: 3, Healthy: true}}
	m2 := &mockMaintainer{name: "codedb", result: DBMaintenanceResult{Name: "codedb", Pruned: 0, Healthy: true}}

	s.Register(m1)
	s.Register(m2)

	// run only whisper
	result := s.RunOne(context.Background(), "whisper")
	if result == nil {
		t.Fatal("expected result for 'whisper'")
	}
	if result.Pruned != 3 {
		t.Errorf("expected pruned=3, got %d", result.Pruned)
	}
	if m1.called.Load() != 1 {
		t.Errorf("expected whisper called once, got %d", m1.called.Load())
	}
	if m2.called.Load() != 0 {
		t.Errorf("expected codedb not called, got %d", m2.called.Load())
	}
}

func TestSchedulerRunOneNotFound(t *testing.T) {
	s := NewDBMaintenanceScheduler(nil)
	s.Register(&mockMaintainer{name: "db1"})

	result := s.RunOne(context.Background(), "nonexistent")
	if result != nil {
		t.Error("expected nil for nonexistent maintainer")
	}
}

func TestSchedulerNames(t *testing.T) {
	s := NewDBMaintenanceScheduler(nil)
	s.Register(&mockMaintainer{name: "alpha"})
	s.Register(&mockMaintainer{name: "beta"})

	names := s.Names()
	if len(names) != 2 {
		t.Fatalf("expected 2 names, got %d", len(names))
	}
	if names[0] != "alpha" || names[1] != "beta" {
		t.Errorf("expected [alpha beta], got %v", names)
	}
}

func TestSchedulerNamesEmpty(t *testing.T) {
	s := NewDBMaintenanceScheduler(nil)
	names := s.Names()
	if len(names) != 0 {
		t.Errorf("expected 0 names, got %d", len(names))
	}
}

func TestSchedulerStartAndStop(t *testing.T) {
	s := NewDBMaintenanceScheduler(nil)
	m := &mockMaintainer{name: "db", result: DBMaintenanceResult{Name: "db", Healthy: true}}
	s.Register(m)

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	s.Start(ctx, &wg, 10*time.Millisecond)

	// wait for at least one maintenance call
	require.Eventually(t, func() bool {
		return m.called.Load() > 0
	}, 2*time.Second, 10*time.Millisecond, "expected at least one maintenance call")
	cancel()
	wg.Wait()
}

func TestSchedulerErrorDoesNotBlockOthers(t *testing.T) {
	s := NewDBMaintenanceScheduler(nil)

	errResult := DBMaintenanceResult{Name: "broken", Error: os.ErrNotExist}
	okResult := DBMaintenanceResult{Name: "healthy", Pruned: 2, Healthy: true}

	s.Register(&mockMaintainer{name: "broken", result: errResult})
	s.Register(&mockMaintainer{name: "healthy", result: okResult})

	results := s.RunAll(context.Background())

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].Error == nil {
		t.Error("expected error from broken maintainer")
	}
	if results[1].Pruned != 2 {
		t.Error("expected healthy maintainer to still run")
	}
}

// --- WhisperDBMaintainer tests ---

func TestWhisperDBMaintainerBasic(t *testing.T) {
	store := openTestStore(t)

	// add some old entries that will be pruned
	old := time.Now().Add(-72 * time.Hour)
	for i := 0; i < 5; i++ {
		store.Add(whisperstore.WhisperEntry{
			ID:         "old-" + string(rune('a'+i)),
			Scope:      "ledger",
			Type:       whisperstore.WhisperTimeBased,
			Source:     "test",
			Topic:      "test",
			Content:    "old entry",
			Importance: whisperstore.ImportanceNormal,
			CreatedAt:  old,
		})
	}

	m := newWhisperDBMaintainerForStore("whisper-test", store, 24*time.Hour, 10*1024*1024)

	result := m.Maintain(context.Background())

	if result.Name != "whisper-test" {
		t.Errorf("expected name=whisper-test, got %s", result.Name)
	}
	if result.Pruned == 0 {
		t.Error("expected some entries to be pruned")
	}
	if result.SizeBefore < 0 {
		t.Error("expected non-negative SizeBefore")
	}
	if result.SizeAfter < 0 {
		t.Error("expected non-negative SizeAfter")
	}
	if result.Duration <= 0 {
		t.Error("expected positive duration")
	}
}

func TestWhisperDBMaintainerHealthCheck(t *testing.T) {
	store := openTestStore(t)
	m := newWhisperDBMaintainerForStore("whisper-health", store, 24*time.Hour, 10*1024*1024)

	result := m.Maintain(context.Background())

	if !result.Healthy {
		t.Error("expected healthy store")
	}
}

func TestWhisperDBMaintainerName(t *testing.T) {
	m := newWhisperDBMaintainerForStore("custom-name", nil, time.Hour, 0)
	if m.Name() != "custom-name" {
		t.Errorf("expected custom-name, got %s", m.Name())
	}
}

// --- CodeDBMaintainer tests ---

func TestCodeDBMaintainerNilManager(t *testing.T) {
	m := NewCodeDBMaintainer("codedb", nil)
	result := m.Maintain(context.Background())

	if result.Name != "codedb" {
		t.Errorf("expected name=codedb, got %s", result.Name)
	}
	if result.Duration <= 0 {
		t.Error("expected positive duration")
	}
}

func TestCodeDBMaintainerNoIndex(t *testing.T) {
	tmpDir := t.TempDir()
	manager := NewCodeDBManager(tmpDir, slog.Default(), nil)

	m := NewCodeDBMaintainer("codedb", manager)
	result := m.Maintain(context.Background())

	// no index exists — should report healthy and skip
	if !result.Healthy {
		t.Error("expected healthy when no index exists")
	}
	if result.Error != nil {
		t.Errorf("expected no error, got %v", result.Error)
	}
}

func TestCodeDBMaintainerName(t *testing.T) {
	m := NewCodeDBMaintainer("my-codedb", nil)
	if m.Name() != "my-codedb" {
		t.Errorf("expected my-codedb, got %s", m.Name())
	}
}

// --- GC preservation tests ---

func TestGCPreservesWhisperDB(t *testing.T) {
	srcRepo := t.TempDir()
	backupDir := t.TempDir()
	dstRepo := t.TempDir()

	// create a whisper DB in the source cache
	cacheDir := filepath.Join(srcRepo, ".sageox", "cache", "whisper")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(cacheDir, "whisper.db")
	if err := os.WriteFile(dbPath, []byte("test-whisper-data"), 0o644); err != nil {
		t.Fatal(err)
	}

	// preserve
	if err := gcPreserveCache(srcRepo, backupDir); err != nil {
		t.Fatalf("preserve: %v", err)
	}

	// verify backup exists
	backupDB := filepath.Join(backupDir, "whisper", "whisper.db")
	if _, err := os.Stat(backupDB); err != nil {
		t.Fatalf("backup should exist: %v", err)
	}

	// restore to new location
	if err := gcRestoreCache(backupDir, dstRepo); err != nil {
		t.Fatalf("restore: %v", err)
	}

	// verify restored
	restoredDB := filepath.Join(dstRepo, ".sageox", "cache", "whisper", "whisper.db")
	data, err := os.ReadFile(restoredDB)
	if err != nil {
		t.Fatalf("restored DB should exist: %v", err)
	}
	if string(data) != "test-whisper-data" {
		t.Error("restored DB content mismatch")
	}
}

func TestGCPreservesCodeDB(t *testing.T) {
	srcRepo := t.TempDir()
	backupDir := t.TempDir()
	dstRepo := t.TempDir()

	// create codedb files in the source cache
	codedbDir := filepath.Join(srcRepo, ".sageox", "cache", "codedb")
	if err := os.MkdirAll(codedbDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(codedbDir, "metadata.db"), []byte("codedb-data"), 0o644); err != nil {
		t.Fatal(err)
	}
	bleveDir := filepath.Join(codedbDir, "bleve", "code")
	if err := os.MkdirAll(bleveDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bleveDir, "index.bleve"), []byte("bleve-data"), 0o644); err != nil {
		t.Fatal(err)
	}

	// preserve + restore
	if err := gcPreserveCache(srcRepo, backupDir); err != nil {
		t.Fatalf("preserve: %v", err)
	}
	if err := gcRestoreCache(backupDir, dstRepo); err != nil {
		t.Fatalf("restore: %v", err)
	}

	// verify metadata.db
	data, err := os.ReadFile(filepath.Join(dstRepo, ".sageox", "cache", "codedb", "metadata.db"))
	if err != nil {
		t.Fatalf("restored codedb should exist: %v", err)
	}
	if string(data) != "codedb-data" {
		t.Error("restored codedb content mismatch")
	}

	// verify bleve index
	data, err = os.ReadFile(filepath.Join(dstRepo, ".sageox", "cache", "codedb", "bleve", "code", "index.bleve"))
	if err != nil {
		t.Fatalf("restored bleve should exist: %v", err)
	}
	if string(data) != "bleve-data" {
		t.Error("restored bleve content mismatch")
	}
}

func TestGCPreservesMultipleDBs(t *testing.T) {
	srcRepo := t.TempDir()
	backupDir := t.TempDir()
	dstRepo := t.TempDir()

	// create both whisper and codedb in cache
	for _, sub := range []string{"whisper", "codedb"} {
		dir := filepath.Join(srcRepo, ".sageox", "cache", sub)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "data.db"), []byte(sub+"-content"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if err := gcPreserveCache(srcRepo, backupDir); err != nil {
		t.Fatalf("preserve: %v", err)
	}
	if err := gcRestoreCache(backupDir, dstRepo); err != nil {
		t.Fatalf("restore: %v", err)
	}

	for _, sub := range []string{"whisper", "codedb"} {
		data, err := os.ReadFile(filepath.Join(dstRepo, ".sageox", "cache", sub, "data.db"))
		if err != nil {
			t.Fatalf("restored %s should exist: %v", sub, err)
		}
		if string(data) != sub+"-content" {
			t.Errorf("restored %s content mismatch", sub)
		}
	}
}

func TestGCPreserveNoCacheDir(t *testing.T) {
	srcRepo := t.TempDir()
	backupDir := t.TempDir()

	// no .sageox/cache/ — should succeed silently
	err := gcPreserveCache(srcRepo, backupDir)
	if err != nil {
		t.Fatalf("expected no error when cache dir absent, got %v", err)
	}
}

func TestGCRestoreEmptyBackup(t *testing.T) {
	backupDir := t.TempDir()
	dstRepo := t.TempDir()

	// empty backup — should succeed silently
	err := gcRestoreCache(backupDir, dstRepo)
	if err != nil {
		t.Fatalf("expected no error restoring empty backup, got %v", err)
	}
}
