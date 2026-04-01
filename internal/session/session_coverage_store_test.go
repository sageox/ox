package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSessionMeta_Duration_WithEnd(t *testing.T) {
	start := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	end := time.Date(2026, 1, 1, 10, 30, 0, 0, time.UTC)
	meta := &SessionMeta{
		StartedAt: start,
		EndedAt:   end,
	}
	d := meta.Duration()
	if d != 30*time.Minute {
		t.Errorf("Duration() = %v, want 30m", d)
	}
}

func TestEntriesToPkg_TimestampPreserved(t *testing.T) {
	ts := time.Date(2026, 3, 15, 10, 30, 0, 0, time.UTC)
	entries := []Entry{
		{Timestamp: ts, Type: SessionEntryTypeUser, Content: "test"},
	}

	pkg := entriesToPkg(entries)
	if !pkg[0].Timestamp.Equal(ts) {
		t.Errorf("timestamp not preserved: got %v, want %v", pkg[0].Timestamp, ts)
	}

	back := pkgToEntries(pkg)
	if !back[0].Timestamp.Equal(ts) {
		t.Errorf("round-trip timestamp not preserved: got %v, want %v", back[0].Timestamp, ts)
	}
}

// --- store.go: ListSessions, BasePath, ResolveSessionName ---

func TestStore_BasePath(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	bp := store.BasePath()
	if bp == "" {
		t.Error("BasePath should not be empty")
	}
	if !containsSubstring(bp, "sessions") {
		t.Error("BasePath should contain 'sessions'")
	}
}

func TestStore_ListSessions_DefaultWindow(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	sessions, err := store.ListSessions()
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 0 {
		t.Errorf("expected 0 sessions, got %d", len(sessions))
	}
}

func TestStore_ResolveSessionName_ExactMatch(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	// create a session directory
	sessDir := filepath.Join(store.BasePath(), "2026-03-15T10-00-user-OxABC")
	os.MkdirAll(sessDir, 0755)

	resolved, err := store.ResolveSessionName("2026-03-15T10-00-user-OxABC")
	if err != nil {
		t.Fatalf("ResolveSessionName: %v", err)
	}
	if resolved != "2026-03-15T10-00-user-OxABC" {
		t.Errorf("resolved = %q, want exact match", resolved)
	}
}

func TestStore_ResolveSessionName_SuffixMatch(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	// create session with raw.jsonl so it shows up in listing
	sessName := "2026-03-15T10-00-user-OxDEF"
	sessDir := filepath.Join(store.BasePath(), sessName)
	os.MkdirAll(sessDir, 0755)
	os.WriteFile(filepath.Join(sessDir, "raw.jsonl"), []byte(`{"_meta":{}}`+"\n"), 0644)

	resolved, err := store.ResolveSessionName("OxDEF")
	if err != nil {
		t.Fatalf("ResolveSessionName: %v", err)
	}
	if resolved != sessName {
		t.Errorf("resolved = %q, want %q", resolved, sessName)
	}
}

func TestStore_ResolveSessionName_NoMatch(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	resolved, err := store.ResolveSessionName("nonexistent")
	if err != nil {
		t.Fatalf("ResolveSessionName: %v", err)
	}
	// returns input as-is when no match
	if resolved != "nonexistent" {
		t.Errorf("resolved = %q, want input as-is", resolved)
	}
}

func TestStore_ResolveSessionName_Ambiguous(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	// create two sessions with same suffix
	for _, name := range []string{
		"2026-03-15T10-00-user1-OxSAME",
		"2026-03-15T11-00-user2-OxSAME",
	} {
		sessDir := filepath.Join(store.BasePath(), name)
		os.MkdirAll(sessDir, 0755)
		os.WriteFile(filepath.Join(sessDir, "raw.jsonl"), []byte(`{"_meta":{}}`+"\n"), 0644)
	}

	_, err = store.ResolveSessionName("OxSAME")
	if err == nil {
		t.Fatal("expected error for ambiguous match")
	}
	if !containsSubstring(err.Error(), "ambiguous") {
		t.Errorf("error = %q, want to contain 'ambiguous'", err.Error())
	}
}

// --- store.go: ReadRawSession ---

func TestStore_ReadRawSession(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	sessName := "2026-03-15T10-00-tester-OxRaw"
	sessDir := filepath.Join(store.BasePath(), sessName)
	os.MkdirAll(sessDir, 0755)

	// write a valid raw.jsonl with meta + entries
	rawContent := `{"_meta":{"schema_version":"1","agent_type":"claude-code","session_id":"OxRaw","started_at":"2026-03-15T10:00:00Z"}}
{"type":"user","content":"hello","ts":"2026-03-15T10:01:00Z"}
{"type":"assistant","content":"hi there","ts":"2026-03-15T10:02:00Z"}
`
	os.WriteFile(filepath.Join(sessDir, "raw.jsonl"), []byte(rawContent), 0644)

	stored, err := store.ReadRawSession(sessName)
	if err != nil {
		t.Fatalf("ReadRawSession: %v", err)
	}
	if stored == nil {
		t.Fatal("expected non-nil stored session")
	}
	if len(stored.Entries) < 2 {
		t.Errorf("expected at least 2 entries, got %d", len(stored.Entries))
	}
}

// --- history_store.go: LoadHistoryFromSession ---

func TestLoadHistoryFromSession_NoFile(t *testing.T) {
	dir := t.TempDir()
	_, err := LoadHistoryFromSession(dir)
	if err == nil {
		t.Error("expected error for missing history file")
	}
}

// --- storage.go: List and Exists ---

func TestStore_List(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	sessions, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(sessions) != 0 {
		t.Errorf("expected 0 sessions, got %d", len(sessions))
	}
}

func TestStore_Exists_NotFound(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	if store.Exists("nonexistent") {
		t.Error("Exists should return false for missing session")
	}
}

func TestStore_Exists_Found(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	// create a valid session
	sessName := "2026-03-15T10-00-user-OxExists"
	sessDir := filepath.Join(store.BasePath(), sessName)
	os.MkdirAll(sessDir, 0755)
	rawContent := `{"_meta":{"schema_version":"1","agent_type":"claude-code","session_id":"OxExists","started_at":"2026-03-15T10:00:00Z"}}
{"type":"user","content":"test","ts":"2026-03-15T10:01:00Z"}
`
	os.WriteFile(filepath.Join(sessDir, "raw.jsonl"), []byte(rawContent), 0644)

	if !store.Exists(sessName) {
		t.Error("Exists should return true for existing session")
	}
}

// --- store.go: IsSessionHydrated, CheckNeedsDownload ---

func TestStore_IsSessionHydrated_RawExists(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	sessName := "2026-03-15T10-00-user-OxHydrated"
	sessDir := filepath.Join(store.BasePath(), sessName)
	os.MkdirAll(sessDir, 0755)
	os.WriteFile(filepath.Join(sessDir, "raw.jsonl"), []byte("data\n"), 0644)

	if !store.IsSessionHydrated(sessName) {
		t.Error("IsSessionHydrated should return true when raw.jsonl exists")
	}
}

func TestStore_IsSessionHydrated_NoFiles(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	sessName := "2026-03-15T10-00-user-OxEmpty"
	sessDir := filepath.Join(store.BasePath(), sessName)
	os.MkdirAll(sessDir, 0755)

	if store.IsSessionHydrated(sessName) {
		t.Error("IsSessionHydrated should return false when no files exist")
	}
}

func TestStore_CheckNeedsDownload_NoSession(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	result := store.CheckNeedsDownload("nonexistent")
	if result != "" {
		t.Errorf("CheckNeedsDownload = %q, want empty for missing session", result)
	}
}

func TestStore_CheckNeedsDownload_NoMeta(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	sessName := "2026-03-15T10-00-user-OxNoMeta"
	sessDir := filepath.Join(store.BasePath(), sessName)
	os.MkdirAll(sessDir, 0755)

	result := store.CheckNeedsDownload(sessName)
	if result != "" {
		t.Errorf("CheckNeedsDownload = %q, want empty when no meta.json", result)
	}
}

func TestStore_CheckNeedsDownload_Hydrated(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	sessName := "2026-03-15T10-00-user-OxHyd"
	sessDir := filepath.Join(store.BasePath(), sessName)
	os.MkdirAll(sessDir, 0755)
	// meta.json must list the files that exist, and those files must not be LFS pointers
	metaJSON := `{"version":"1.0","session_name":"test","files":{"raw.jsonl":{"oid":"sha256:abc","size":8}}}`
	os.WriteFile(filepath.Join(sessDir, "meta.json"), []byte(metaJSON), 0644)
	os.WriteFile(filepath.Join(sessDir, "raw.jsonl"), []byte("content\n"), 0644)

	result := store.CheckNeedsDownload(sessName)
	if result != "" {
		t.Errorf("CheckNeedsDownload = %q, want empty for hydrated session", result)
	}
}

// --- store.go: ReadLFSSessionMeta ---

func TestStore_ReadLFSSessionMeta_NoMeta(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	sessName := "2026-03-15T10-00-user-OxNoLFS"
	sessDir := filepath.Join(store.BasePath(), sessName)
	os.MkdirAll(sessDir, 0755)

	meta, err := store.ReadLFSSessionMeta(sessName)
	if err == nil && meta != nil {
		t.Error("expected nil meta or error for session without meta.json")
	}
}
