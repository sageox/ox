package pipeline

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockFileSystem is a test double that records calls and can inject errors.
type mockFileSystem struct {
	files    map[string][]byte
	dirs     map[string]bool
	mkdirErr error
	writeErr map[string]error
	readErr  map[string]error
}

func newMockFS() *mockFileSystem {
	return &mockFileSystem{
		files:    make(map[string][]byte),
		dirs:     make(map[string]bool),
		writeErr: make(map[string]error),
		readErr:  make(map[string]error),
	}
}

func (m *mockFileSystem) ReadFile(path string) ([]byte, error) {
	if err, ok := m.readErr[path]; ok {
		return nil, err
	}
	data, ok := m.files[path]
	if !ok {
		return nil, &os.PathError{Op: "open", Path: path, Err: os.ErrNotExist}
	}
	return data, nil
}

func (m *mockFileSystem) WriteFile(path string, data []byte, _ os.FileMode) error {
	if err, ok := m.writeErr[path]; ok {
		return err
	}
	m.files[path] = data
	return nil
}

func (m *mockFileSystem) MkdirAll(path string, _ os.FileMode) error {
	if m.mkdirErr != nil {
		return m.mkdirErr
	}
	m.dirs[path] = true
	return nil
}

func (m *mockFileSystem) Stat(path string) (fs.FileInfo, error) {
	if _, ok := m.files[path]; ok {
		return mockFileInfo{name: filepath.Base(path)}, nil
	}
	if m.dirs[path] {
		return mockFileInfo{name: filepath.Base(path), dir: true}, nil
	}
	return nil, &os.PathError{Op: "stat", Path: path, Err: os.ErrNotExist}
}

func (m *mockFileSystem) Remove(path string) error {
	delete(m.files, path)
	return nil
}

func (m *mockFileSystem) RemoveAll(path string) error {
	for k := range m.files {
		if len(k) >= len(path) && k[:len(path)] == path {
			delete(m.files, k)
		}
	}
	delete(m.dirs, path)
	return nil
}

type mockFileInfo struct {
	name string
	dir  bool
}

func (m mockFileInfo) Name() string       { return m.name }
func (m mockFileInfo) Size() int64        { return 0 }
func (m mockFileInfo) Mode() os.FileMode  { return 0644 }
func (m mockFileInfo) ModTime() time.Time { return time.Time{} }
func (m mockFileInfo) IsDir() bool        { return m.dir }
func (m mockFileInfo) Sys() any           { return nil }

func TestCopySessionToLedger(t *testing.T) {
	mfs := newMockFS()
	mfs.files["/cache/raw.jsonl"] = []byte(`{"test":"data"}`)
	mfs.files["/cache/summary.md"] = []byte("# Summary")

	result := &Result{
		RawPath:       "/cache/raw.jsonl",
		SummaryMDPath: "/cache/summary.md",
		EntryCount:    5,
	}

	err := CopySessionToLedger(mfs, result, "/ledger", "session-001")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// verify raw.jsonl copied
	rawDst := filepath.Join("/ledger", "sessions", "session-001", LedgerFileRaw)
	if data, ok := mfs.files[rawDst]; !ok {
		t.Error("raw.jsonl not copied to ledger")
	} else if string(data) != `{"test":"data"}` {
		t.Errorf("raw.jsonl content mismatch: %q", string(data))
	}

	// verify secondary artifacts copied
	summaryDst := filepath.Join("/ledger", "sessions", "session-001", LedgerFileSummaryMD)
	if _, ok := mfs.files[summaryDst]; !ok {
		t.Error("summary.md not copied to ledger")
	}

	// verify directory was created
	sessionDir := filepath.Join("/ledger", "sessions", "session-001")
	if !mfs.dirs[sessionDir] {
		t.Error("session directory not created")
	}
}

func TestCopySessionToLedgerZeroEntries(t *testing.T) {
	mfs := newMockFS()
	result := &Result{
		RawPath:    "/cache/raw.jsonl",
		EntryCount: 0,
	}

	err := CopySessionToLedger(mfs, result, "/ledger", "session-empty")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// nothing should have been written
	if len(mfs.files) > 0 {
		t.Error("expected no files written for zero-entry session")
	}
}

func TestCopySessionToLedgerRawReadFails(t *testing.T) {
	mfs := newMockFS()
	mfs.readErr["/cache/raw.jsonl"] = errors.New("disk error")

	result := &Result{
		RawPath:    "/cache/raw.jsonl",
		EntryCount: 5,
	}

	err := CopySessionToLedger(mfs, result, "/ledger", "session-fail")
	if err == nil {
		t.Fatal("expected error when raw.jsonl read fails")
	}
}

func TestCopySessionToLedgerCriticalFailuresPreserveRecoverableState(t *testing.T) {
	const (
		rawSource = "/cache/raw.jsonl"
		session   = "session-fail"
	)
	rawDestination := filepath.Join("/ledger", "sessions", session, LedgerFileRaw)
	sourceContent := []byte(`{"source":"complete"}`)
	priorDestination := []byte(`{"destination":"prior-good-copy"}`)
	wantErr := errors.New("injected failure")

	tests := []struct {
		name      string
		configure func(*mockFileSystem, *Result)
	}{
		{
			name: "missing raw source path fails before creating destination",
			configure: func(_ *mockFileSystem, result *Result) {
				result.RawPath = ""
			},
		},
		{
			name: "session directory creation fails",
			configure: func(fsys *mockFileSystem, _ *Result) {
				fsys.mkdirErr = wantErr
			},
		},
		{
			name: "raw read fails",
			configure: func(fsys *mockFileSystem, _ *Result) {
				fsys.readErr[rawSource] = wantErr
			},
		},
		{
			name: "raw destination write fails",
			configure: func(fsys *mockFileSystem, _ *Result) {
				fsys.writeErr[rawDestination] = wantErr
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fsys := newMockFS()
			fsys.files[rawSource] = append([]byte(nil), sourceContent...)
			fsys.files[rawDestination] = append([]byte(nil), priorDestination...)
			result := &Result{RawPath: rawSource, EntryCount: 2}
			tt.configure(fsys, result)

			err := CopySessionToLedger(fsys, result, "/ledger", session)

			require.Error(t, err)
			assert.Equal(t, sourceContent, fsys.files[rawSource], "cache source must remain retryable")
			assert.Equal(t, priorDestination, fsys.files[rawDestination], "failed copy must preserve prior ledger bytes")
			if result.RawPath == "" {
				assert.Empty(t, fsys.dirs, "invalid input must fail before creating ledger state")
			}
		})
	}
}

func TestCopySessionToLedgerSecondaryWriteFailureKeepsCriticalRaw(t *testing.T) {
	fsys := newMockFS()
	fsys.files["/cache/raw.jsonl"] = []byte(`{"raw":true}`)
	fsys.files["/cache/summary.md"] = []byte("summary")
	summaryDestination := filepath.Join("/ledger", "sessions", "session-partial", LedgerFileSummaryMD)
	fsys.writeErr[summaryDestination] = errors.New("disk full")
	result := &Result{
		RawPath:       "/cache/raw.jsonl",
		SummaryMDPath: "/cache/summary.md",
		EntryCount:    2,
	}

	err := CopySessionToLedger(fsys, result, "/ledger", "session-partial")

	require.NoError(t, err, "secondary artifacts are explicitly best-effort")
	assert.Equal(t, []byte(`{"raw":true}`), fsys.files[filepath.Join("/ledger", "sessions", "session-partial", LedgerFileRaw)])
	assert.NotContains(t, fsys.files, summaryDestination)
}

func TestCopySessionToLedgerSecondaryFailsGracefully(t *testing.T) {
	mfs := newMockFS()
	mfs.files["/cache/raw.jsonl"] = []byte(`{"data":true}`)
	// summary source doesn't exist — should not error
	result := &Result{
		RawPath:       "/cache/raw.jsonl",
		SummaryMDPath: "/cache/missing-summary.md",
		EntryCount:    3,
	}

	err := CopySessionToLedger(mfs, result, "/ledger", "session-partial")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// raw should be copied
	rawDst := filepath.Join("/ledger", "sessions", "session-partial", LedgerFileRaw)
	if _, ok := mfs.files[rawDst]; !ok {
		t.Error("raw.jsonl not copied")
	}

	// summary should NOT be there (source didn't exist)
	summaryDst := filepath.Join("/ledger", "sessions", "session-partial", LedgerFileSummaryMD)
	if _, ok := mfs.files[summaryDst]; ok {
		t.Error("missing summary should not have been copied")
	}
}

func TestRewriteLedgerPaths(t *testing.T) {
	mfs := newMockFS()
	ledgerDir := "/ledger/sessions/session-001"
	mfs.files[filepath.Join(ledgerDir, LedgerFileRaw)] = []byte("raw")
	mfs.files[filepath.Join(ledgerDir, LedgerFileSummaryMD)] = []byte("summary")
	// SessionMD intentionally missing

	result := &Result{
		RawPath:          "/cache/raw.jsonl",
		SummaryMDPath:    "/cache/summary.md",
		SessionMDPath:    "/cache/session.md",
		PlanPath:         "",
		LedgerSessionDir: ledgerDir,
	}

	RewriteLedgerPaths(mfs, result)

	if result.RawPath != filepath.Join(ledgerDir, LedgerFileRaw) {
		t.Errorf("RawPath: got %q", result.RawPath)
	}
	if result.SummaryMDPath != filepath.Join(ledgerDir, LedgerFileSummaryMD) {
		t.Errorf("SummaryMDPath: got %q", result.SummaryMDPath)
	}
	// session.md didn't exist in ledger, should be cleared
	if result.SessionMDPath != "" {
		t.Errorf("SessionMDPath should be empty, got %q", result.SessionMDPath)
	}
	// PlanPath was already empty, should stay empty
	if result.PlanPath != "" {
		t.Errorf("PlanPath should be empty, got %q", result.PlanPath)
	}
}

func TestCopySessionToLedgerIncludesContextTrace(t *testing.T) {
	mfs := newMockFS()
	mfs.files["/cache/raw.jsonl"] = []byte(`{"test":"data"}`)
	mfs.files["/cache/context-trace.jsonl"] = []byte(`{"type":"provided","ts":"2026-03-31T10:00:00Z","source":"team-context"}`)

	result := &Result{
		RawPath:          "/cache/raw.jsonl",
		ContextTracePath: "/cache/context-trace.jsonl",
		EntryCount:       5,
	}

	err := CopySessionToLedger(mfs, result, "/ledger", "session-ct")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// verify context-trace.jsonl copied as secondary artifact
	ctDst := filepath.Join("/ledger", "sessions", "session-ct", LedgerFileContextTrace)
	if _, ok := mfs.files[ctDst]; !ok {
		t.Error("context-trace.jsonl not copied to ledger")
	}
}

func TestCopySessionToLedgerContextTraceMissingGraceful(t *testing.T) {
	mfs := newMockFS()
	mfs.files["/cache/raw.jsonl"] = []byte(`{"test":"data"}`)

	result := &Result{
		RawPath:          "/cache/raw.jsonl",
		ContextTracePath: "/cache/missing-context-trace.jsonl",
		EntryCount:       3,
	}

	err := CopySessionToLedger(mfs, result, "/ledger", "session-ct-missing")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// raw should be copied
	rawDst := filepath.Join("/ledger", "sessions", "session-ct-missing", LedgerFileRaw)
	if _, ok := mfs.files[rawDst]; !ok {
		t.Error("raw.jsonl not copied")
	}

	// context-trace should NOT be there (source didn't exist)
	ctDst := filepath.Join("/ledger", "sessions", "session-ct-missing", LedgerFileContextTrace)
	if _, ok := mfs.files[ctDst]; ok {
		t.Error("missing context-trace should not have been copied")
	}
}

func TestRewriteSecondaryPathsIncludesContextTrace(t *testing.T) {
	mfs := newMockFS()
	ledgerDir := "/ledger/sessions/session-ct"
	mfs.files[filepath.Join(ledgerDir, LedgerFileContextTrace)] = []byte("trace")

	result := &Result{
		RawPath:          "/cache/raw.jsonl",
		ContextTracePath: "/cache/context-trace.jsonl",
		LedgerSessionDir: ledgerDir,
	}

	RewriteSecondaryPaths(mfs, result)

	// RawPath should be unchanged (RewriteSecondaryPaths preserves it)
	if result.RawPath != "/cache/raw.jsonl" {
		t.Errorf("RawPath changed: %q", result.RawPath)
	}
	// ContextTracePath should be rewritten to ledger
	if result.ContextTracePath != filepath.Join(ledgerDir, LedgerFileContextTrace) {
		t.Errorf("ContextTracePath: got %q", result.ContextTracePath)
	}
}

func TestRewriteLedgerPathsNoLedgerDir(t *testing.T) {
	mfs := newMockFS()
	result := &Result{
		RawPath:          "/cache/raw.jsonl",
		LedgerSessionDir: "", // no ledger dir
	}

	RewriteLedgerPaths(mfs, result)

	// should be unchanged
	if result.RawPath != "/cache/raw.jsonl" {
		t.Errorf("RawPath should be unchanged, got %q", result.RawPath)
	}
}
