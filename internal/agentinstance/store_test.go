package agentinstance

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestNewStoreForUser(t *testing.T) {
	tmpDir := t.TempDir()
	sageoxDir := filepath.Join(tmpDir, ".sageox")
	if err := os.MkdirAll(sageoxDir, 0755); err != nil {
		t.Fatalf("failed to create .sageox dir: %v", err)
	}

	store, err := NewStoreForUser(tmpDir, "testuser")
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	if store == nil {
		t.Fatal("expected non-nil store")
	}

	// verify instance directory was created
	expectedPath := filepath.Join(tmpDir, ".sageox", "agent_instances", "testuser")
	info, err := os.Stat(expectedPath)
	if err != nil {
		t.Fatalf("instance directory not created: %v", err)
	}
	if !info.IsDir() {
		t.Fatal("expected instance path to be a directory")
	}
}

func TestNewStoreEmptyProjectRoot(t *testing.T) {
	_, err := NewStoreForUser("", "testuser")
	if err == nil {
		t.Error("expected error for empty project root")
	}
}

func TestNewStoreEmptyUserSlug(t *testing.T) {
	tmpDir := t.TempDir()

	store, err := NewStoreForUser(tmpDir, "")
	if err != nil {
		t.Fatalf("should not error for empty user slug: %v", err)
	}
	if store.userSlug != "anonymous" {
		t.Errorf("expected userSlug 'anonymous', got %q", store.userSlug)
	}
}

func TestAddAndGet(t *testing.T) {
	tmpDir := t.TempDir()

	store, err := NewStoreForUser(tmpDir, "testuser")
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	inst := &Instance{
		AgentID:         "OxA1b2",
		ServerSessionID: "oxsid_01KCJECKEGETGX6HC80NRYVZ3P",
		CreatedAt:       time.Now(),
		ExpiresAt:       time.Now().Add(24 * time.Hour),
		AgentType:       "claude-code",
		Model:           "claude-opus-4-5",
	}

	if err := store.Add(inst); err != nil {
		t.Fatalf("failed to add instance: %v", err)
	}

	got, err := store.Get("OxA1b2")
	if err != nil {
		t.Fatalf("failed to get instance: %v", err)
	}

	if got.AgentID != inst.AgentID {
		t.Errorf("AgentID mismatch: got %q, want %q", got.AgentID, inst.AgentID)
	}
	if got.ServerSessionID != inst.ServerSessionID {
		t.Errorf("ServerSessionID mismatch: got %q, want %q", got.ServerSessionID, inst.ServerSessionID)
	}
	if got.AgentType != inst.AgentType {
		t.Errorf("AgentType mismatch: got %q, want %q", got.AgentType, inst.AgentType)
	}
}

func TestGetNotFound(t *testing.T) {
	tmpDir := t.TempDir()

	store, err := NewStoreForUser(tmpDir, "testuser")
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	_, err = store.Get("OxNoSh")
	if !errors.Is(err, ErrInstanceNotFound) {
		t.Errorf("Get(unknown) error = %v, want ErrInstanceNotFound", err)
	}
	if errors.Is(err, ErrInstanceExpired) {
		t.Errorf("Get(unknown) error = %v, must not report expiry", err)
	}
}

func TestGetExpired(t *testing.T) {
	store := newTestStore(t)

	inst := &Instance{
		AgentID:         "OxOld1",
		ServerSessionID: "oxsid_01KCJECKEGETGX6HC80NRYVZ3P",
		CreatedAt:       time.Now().Add(-48 * time.Hour),
		ExpiresAt:       time.Now().Add(-24 * time.Hour), // expired
		AgentType:       "claude-code",
	}

	if err := store.Add(inst); err != nil {
		t.Fatalf("failed to add instance: %v", err)
	}

	// callers tell an instance that outlived its 24 hours apart from one that
	// never existed in this store
	_, err := store.Get("OxOld1")
	if !errors.Is(err, ErrInstanceExpired) {
		t.Errorf("Get(expired) error = %v, want ErrInstanceExpired", err)
	}
	if errors.Is(err, ErrInstanceNotFound) {
		t.Errorf("Get(expired) error = %v, must not report not-found", err)
	}
}

// TestGetReportsExpiryWhenItsOwnPruneFinishesFirst: a lookup of an expired
// instance finds enough expired records to trigger compaction. That prune must
// not run until the lookup has told "expired" from "never existed", or it
// deletes the record the lookup is about to report on.
// Failure prevented: `ox agent <id> …` for an expired agent intermittently says
// "instance not found" instead of "instance expired" (seen as a CI flake in
// TestLiveAgentOutlivesExpiresAt and TestGetExpired).
func TestGetReportsExpiryWhenItsOwnPruneFinishesFirst(t *testing.T) {
	store := newTestStore(t)
	store.expiryCheckTestHook = store.pruneWG.Wait // a prune already launched finishes first
	past := time.Now().Add(-time.Hour)
	if err := store.Add(&Instance{AgentID: "OxOld2", ServerSessionID: "oxsid_old", CreatedAt: past.Add(-24 * time.Hour), ExpiresAt: past}); err != nil {
		t.Fatalf("add: %v", err)
	}

	if _, err := store.Get("OxOld2"); !errors.Is(err, ErrInstanceExpired) {
		t.Fatalf("Get(expired) = %v, want ErrInstanceExpired", err)
	}

	// the compaction still happens, once the lookup has returned
	store.pruneWG.Wait()
	data, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatalf("read store: %v", err)
	}
	if len(data) != 0 {
		t.Errorf("the deferred prune must still drop the expired record; store holds %q", data)
	}
}

// TestStorePathMatchesStore pins StorePath to the file a store reads and
// writes: callers use it to check for a store without creating one.
func TestStorePathMatchesStore(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewStoreForUser(tmpDir, "testuser")
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	if got, want := StorePath(tmpDir, "testuser"), store.Path(); got != want {
		t.Errorf("StorePath = %q, store.Path() = %q", got, want)
	}
	if got, want := StorePath(tmpDir, ""), StorePath(tmpDir, "anonymous"); got != want {
		t.Errorf("StorePath with empty slug = %q, want %q", got, want)
	}
}

func TestList(t *testing.T) {
	tmpDir := t.TempDir()

	store, err := NewStoreForUser(tmpDir, "testuser")
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	instances := []*Instance{
		{
			AgentID:         "OxAbc1",
			ServerSessionID: "oxsid_01KCJECKEGETGX6HC80NRYVZ3P",
			CreatedAt:       time.Now(),
			ExpiresAt:       time.Now().Add(24 * time.Hour),
		},
		{
			AgentID:         "OxAbc2",
			ServerSessionID: "oxsid_01KCJECKEGETGX6HC80NRYVZ3Q",
			CreatedAt:       time.Now(),
			ExpiresAt:       time.Now().Add(24 * time.Hour),
		},
	}

	for _, inst := range instances {
		if err := store.Add(inst); err != nil {
			t.Fatalf("failed to add instance: %v", err)
		}
	}

	list, err := store.List()
	if err != nil {
		t.Fatalf("failed to list instances: %v", err)
	}

	if len(list) != 2 {
		t.Errorf("expected 2 instances, got %d", len(list))
	}
}

func TestCount(t *testing.T) {
	tmpDir := t.TempDir()

	store, err := NewStoreForUser(tmpDir, "testuser")
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	if store.Count() != 0 {
		t.Errorf("expected 0 instances initially, got %d", store.Count())
	}

	inst := &Instance{
		AgentID:         "OxTest",
		ServerSessionID: "oxsid_01KCJECKEGETGX6HC80NRYVZ3P",
		CreatedAt:       time.Now(),
		ExpiresAt:       time.Now().Add(24 * time.Hour),
	}

	if err := store.Add(inst); err != nil {
		t.Fatalf("failed to add instance: %v", err)
	}

	if store.Count() != 1 {
		t.Errorf("expected 1 instance, got %d", store.Count())
	}
}

func TestPrune(t *testing.T) {
	tmpDir := t.TempDir()

	store, err := NewStoreForUser(tmpDir, "testuser")
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	// add expired instance
	expired := &Instance{
		AgentID:         "OxExp1",
		ServerSessionID: "oxsid_expired",
		CreatedAt:       time.Now().Add(-48 * time.Hour),
		ExpiresAt:       time.Now().Add(-24 * time.Hour),
	}
	if err := store.Add(expired); err != nil {
		t.Fatalf("failed to add expired instance: %v", err)
	}

	// add active instance
	active := &Instance{
		AgentID:         "OxAct1",
		ServerSessionID: "oxsid_active",
		CreatedAt:       time.Now(),
		ExpiresAt:       time.Now().Add(24 * time.Hour),
	}
	if err := store.Add(active); err != nil {
		t.Fatalf("failed to add active instance: %v", err)
	}

	pruned, err := store.Prune()
	if err != nil {
		t.Fatalf("failed to prune: %v", err)
	}

	if pruned != 1 {
		t.Errorf("expected 1 pruned, got %d", pruned)
	}

	// verify active instance still exists
	_, err = store.Get("OxAct1")
	if err != nil {
		t.Error("active instance should still exist after prune")
	}
}

func TestInstanceIsExpired(t *testing.T) {
	tests := []struct {
		name      string
		expiresAt time.Time
		expected  bool
	}{
		{"future", time.Now().Add(1 * time.Hour), false},
		{"past", time.Now().Add(-1 * time.Hour), true},
		{"way past", time.Now().Add(-24 * time.Hour), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inst := &Instance{ExpiresAt: tt.expiresAt}
			if got := inst.IsExpired(); got != tt.expected {
				t.Errorf("IsExpired() = %v, want %v", got, tt.expected)
			}
		})
	}
}

// TestLiveAgentOutlivesExpiresAt: an AI coworker's session can run for days
// (a desktop app session, resumed overnight), and its agent ID stays in its
// context and environment the whole time. Past ExpiresAt, an instance whose
// agent process is still running must keep resolving; only an instance whose
// process is gone (or was never recorded) expires.
// Failure prevented: after 24 hours every `ox agent <id> …` call, including the
// `session stop` that uploads the recording, fails with "instance not found".
func TestLiveAgentOutlivesExpiresAt(t *testing.T) {
	exited := exec.Command("true")
	if err := exited.Run(); err != nil {
		t.Skipf("cannot start a short-lived process: %v", err)
	}
	past := time.Now().Add(-time.Hour)
	tests := []struct {
		name      string
		parentPID int
		resolves  bool
	}{
		{"agent process still running", os.Getpid(), true},
		{"agent process exited", exited.Process.Pid, false},
		{"agent process never recorded", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newTestStore(t)
			inst := &Instance{
				AgentID: "OxLong1", ServerSessionID: "oxsid_long",
				CreatedAt: past.Add(-24 * time.Hour), ExpiresAt: past, ParentPID: tt.parentPID,
			}
			if err := store.Add(inst); err != nil {
				t.Fatalf("add: %v", err)
			}
			if got := inst.IsExpired(); got == tt.resolves {
				t.Errorf("IsExpired() = %v, want %v", got, !tt.resolves)
			}

			_, err := store.Get("OxLong1")
			if tt.resolves && err != nil {
				t.Fatalf("Get(live agent past ExpiresAt) = %v, want the instance", err)
			}
			if !tt.resolves && !errors.Is(err, ErrInstanceExpired) {
				t.Fatalf("Get(exited agent past ExpiresAt) = %v, want ErrInstanceExpired", err)
			}

			if _, err := store.Prune(); err != nil {
				t.Fatalf("prune: %v", err)
			}
			listed, err := store.List()
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			if got := len(listed) == 1; got != tt.resolves {
				t.Errorf("after prune, listed %d instances; want the instance kept = %v", len(listed), tt.resolves)
			}
		})
	}
}

func TestInstanceIsPrimeExcessive(t *testing.T) {
	tests := []struct {
		name     string
		count    int
		expected bool
	}{
		{"zero", 0, false},
		{"one", 1, false},
		{"at threshold", ExcessivePrimeThreshold, false},
		{"above threshold", ExcessivePrimeThreshold + 1, true},
		{"way above", ExcessivePrimeThreshold + 10, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inst := &Instance{PrimeCallCount: tt.count}
			if got := inst.IsPrimeExcessive(); got != tt.expected {
				t.Errorf("IsPrimeExcessive() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestUpdate(t *testing.T) {
	tmpDir := t.TempDir()

	store, err := NewStoreForUser(tmpDir, "testuser")
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	inst := &Instance{
		AgentID:         "OxUpd1",
		ServerSessionID: "oxsid_01KCJECKEGETGX6HC80NRYVZ3P",
		CreatedAt:       time.Now(),
		ExpiresAt:       time.Now().Add(24 * time.Hour),
		AgentType:       "claude-code",
		PrimeCallCount:  1,
	}

	if err := store.Add(inst); err != nil {
		t.Fatalf("failed to add instance: %v", err)
	}

	// update instance
	inst.PrimeCallCount = 5
	inst.Model = "claude-sonnet"
	if err := store.Update(inst); err != nil {
		t.Fatalf("failed to update instance: %v", err)
	}

	// verify update
	got, err := store.Get("OxUpd1")
	if err != nil {
		t.Fatalf("failed to get instance: %v", err)
	}

	if got.PrimeCallCount != 5 {
		t.Errorf("PrimeCallCount = %d, want 5", got.PrimeCallCount)
	}
	if got.Model != "claude-sonnet" {
		t.Errorf("Model = %q, want 'claude-sonnet'", got.Model)
	}
}

func TestIncrementPrimeCallCount(t *testing.T) {
	tmpDir := t.TempDir()

	store, err := NewStoreForUser(tmpDir, "testuser")
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	inst := &Instance{
		AgentID:         "OxInc1",
		ServerSessionID: "oxsid_01KCJECKEGETGX6HC80NRYVZ3P",
		CreatedAt:       time.Now(),
		ExpiresAt:       time.Now().Add(24 * time.Hour),
		PrimeCallCount:  0,
	}

	if err := store.Add(inst); err != nil {
		t.Fatalf("failed to add instance: %v", err)
	}

	// increment
	updated, isExcessive, err := store.IncrementPrimeCallCount("OxInc1")
	if err != nil {
		t.Fatalf("failed to increment: %v", err)
	}

	if updated.PrimeCallCount != 1 {
		t.Errorf("PrimeCallCount = %d, want 1", updated.PrimeCallCount)
	}
	if isExcessive {
		t.Error("should not be excessive after 1 increment")
	}

	// increment to threshold
	for i := 1; i <= ExcessivePrimeThreshold; i++ {
		_, _, err = store.IncrementPrimeCallCount("OxInc1")
		if err != nil {
			t.Fatalf("failed to increment: %v", err)
		}
	}

	// one more should be excessive
	_, isExcessive, err = store.IncrementPrimeCallCount("OxInc1")
	if err != nil {
		t.Fatalf("failed to increment: %v", err)
	}
	if !isExcessive {
		t.Errorf("should be excessive after %d increments", ExcessivePrimeThreshold+2)
	}
}

func TestGenerateAgentID(t *testing.T) {
	id, err := GenerateAgentID(nil)
	if err != nil {
		t.Fatalf("failed to generate ID: %v", err)
	}

	if !IsValidAgentID(id) {
		t.Errorf("generated ID is not valid: %q", id)
	}
}

func TestGenerateAgentIDAvoidCollisions(t *testing.T) {
	existing := []string{"OxAbc1", "OxAbc2", "OxAbc3"}

	id, err := GenerateAgentID(existing)
	if err != nil {
		t.Fatalf("failed to generate ID: %v", err)
	}

	for _, e := range existing {
		if id == e {
			t.Errorf("generated ID %q collides with existing", id)
		}
	}
}

func TestIsValidAgentID(t *testing.T) {
	tests := []struct {
		id       string
		expected bool
	}{
		{"OxA1b2", true},
		{"Ox1234", true},
		{"OxZzZz", true},
		{"OxAAAA", true},
		{"ox1234", false},  // lowercase prefix
		{"OX1234", false},  // uppercase X
		{"Ox123", false},   // too short
		{"Ox12345", false}, // too long
		{"Ox12#4", false},  // invalid character
		{"", false},
		{"random", false},
		{"Ox", false},
		{"O1234", false},
		{"xO1234", false},
	}

	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			if got := IsValidAgentID(tt.id); got != tt.expected {
				t.Errorf("IsValidAgentID(%q) = %v, want %v", tt.id, got, tt.expected)
			}
		})
	}
}

func TestParseAgentID(t *testing.T) {
	tests := []struct {
		id       string
		suffix   string
		hasError bool
	}{
		{"OxA1b2", "A1b2", false},
		{"Ox1234", "1234", false},
		{"ox1234", "", true}, // invalid prefix
		{"Ox123", "", true},  // too short
		{"", "", true},       // empty
		{"random", "", true}, // no prefix
	}

	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			suffix, err := ParseAgentID(tt.id)
			if tt.hasError {
				if err == nil {
					t.Errorf("expected error for %q", tt.id)
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error for %q: %v", tt.id, err)
				}
				if suffix != tt.suffix {
					t.Errorf("suffix = %q, want %q", suffix, tt.suffix)
				}
			}
		})
	}
}

// newTestStore creates a Store in a temp dir for testing.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	tmpDir := t.TempDir()
	store, err := NewStoreForUser(tmpDir, "testuser")
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	// Get/List fire go s.Prune() which creates flock/tmp files in the temp
	// dir; wait for them to finish so t.TempDir() cleanup doesn't race.
	t.Cleanup(func() { store.pruneWG.Wait() })
	return store
}

// activeInstance returns a valid non-expired Instance with the given AgentID.
func activeInstance(agentID string) *Instance {
	return &Instance{
		AgentID:         agentID,
		ServerSessionID: "oxsid_" + agentID,
		CreatedAt:       time.Now(),
		ExpiresAt:       time.Now().Add(24 * time.Hour),
	}
}

func TestNewStore(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	if store.userSlug != "anonymous" {
		t.Errorf("NewStore should default userSlug to 'anonymous', got %q", store.userSlug)
	}
}

func TestAddValidation(t *testing.T) {
	store := newTestStore(t)

	tests := []struct {
		name string
		inst *Instance
	}{
		{"nil instance", nil},
		{"empty agent_id", &Instance{ServerSessionID: "oxsid_x"}},
		{"empty server_session_id", &Instance{AgentID: "OxTest"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := store.Add(tt.inst); err == nil {
				t.Error("expected validation error")
			}
		})
	}
}

func TestUpdateValidation(t *testing.T) {
	store := newTestStore(t)

	tests := []struct {
		name string
		inst *Instance
	}{
		{"nil instance", nil},
		{"empty agent_id", &Instance{AgentID: ""}},
		{"not found", &Instance{AgentID: "OxGone", ServerSessionID: "oxsid_x", ExpiresAt: time.Now().Add(time.Hour)}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := store.Update(tt.inst); err == nil {
				t.Error("expected error")
			}
		})
	}
}

func TestIncrementPrimeCallCountValidation(t *testing.T) {
	store := newTestStore(t)

	tests := []struct {
		name    string
		agentID string
		wantErr string
	}{
		{"empty agent_id", "", "cannot be empty"},
		{"not found", "OxGone", "not found"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := store.IncrementPrimeCallCount(tt.agentID)
			if err == nil {
				t.Fatal("expected error")
			}
			if tt.name == "not found" && !errors.Is(err, ErrInstanceNotFound) {
				t.Errorf("expected ErrInstanceNotFound, got: %v", err)
			}
		})
	}
}

func TestGetEmptyAgentID(t *testing.T) {
	store := newTestStore(t)
	_, err := store.Get("")
	if err == nil {
		t.Error("expected error for empty agent_id")
	}
}

func TestIsProcessAlive(t *testing.T) {
	tests := []struct {
		name  string
		pid   int
		alive bool
	}{
		{"zero pid", 0, false},
		{"negative pid", -1, false},
		// current process is alive; use its PID
		{"current process", os.Getpid(), true},
		// PID that almost certainly doesn't exist
		{"dead process", 2147483647, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inst := &Instance{ParentPID: tt.pid}
			if got := inst.IsProcessAlive(); got != tt.alive {
				t.Errorf("IsProcessAlive() = %v, want %v (pid=%d)", got, tt.alive, tt.pid)
			}
		})
	}
}

func TestReadMalformedJSONL(t *testing.T) {
	store := newTestStore(t)

	// write a valid instance, then garbage, then another valid instance
	valid1 := activeInstance("OxGod1")
	valid2 := activeInstance("OxGod2")

	if err := store.Add(valid1); err != nil {
		t.Fatalf("add valid1: %v", err)
	}

	// append garbage line directly to the JSONL file
	f, err := os.OpenFile(store.instancesPath, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatalf("open for append: %v", err)
	}
	fmt.Fprintln(f, "this is not valid json {{{")
	f.Close()

	if err := store.Add(valid2); err != nil {
		t.Fatalf("add valid2: %v", err)
	}

	// both valid instances should be readable; malformed line silently skipped
	list, err := store.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 {
		t.Errorf("expected 2 instances (skipping malformed), got %d", len(list))
	}
}

func TestLastWriteWinsDeduplication(t *testing.T) {
	store := newTestStore(t)

	// append same AgentID twice with different models
	inst1 := activeInstance("OxDup1")
	inst1.Model = "first-model"
	if err := store.Add(inst1); err != nil {
		t.Fatalf("add first: %v", err)
	}

	inst2 := activeInstance("OxDup1")
	inst2.Model = "second-model"
	if err := store.Add(inst2); err != nil {
		t.Fatalf("add second: %v", err)
	}

	got, err := store.Get("OxDup1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Model != "second-model" {
		t.Errorf("expected last-write-wins model 'second-model', got %q", got.Model)
	}

	// List should deduplicate
	list, err := store.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("expected 1 deduplicated instance, got %d", len(list))
	}
}

func TestPruneHardCap(t *testing.T) {
	store := newTestStore(t)

	// add maxActive+5 instances to exceed the hard cap
	overCount := maxActive + 5
	for i := 0; i < overCount; i++ {
		id := fmt.Sprintf("Ox%04d", i)
		inst := &Instance{
			AgentID:         id,
			ServerSessionID: "oxsid_" + id,
			CreatedAt:       time.Now().Add(time.Duration(i) * time.Millisecond),
			ExpiresAt:       time.Now().Add(24 * time.Hour),
		}
		if err := store.Add(inst); err != nil {
			t.Fatalf("add %s: %v", id, err)
		}
	}

	pruned, err := store.Prune()
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if pruned != 5 {
		t.Errorf("expected 5 evicted, got %d", pruned)
	}

	// should have exactly maxActive remaining
	remaining := store.Count()
	if remaining != maxActive {
		t.Errorf("expected %d remaining, got %d", maxActive, remaining)
	}

	// oldest instances (lowest IDs) should have been evicted
	_, err = store.Get("Ox0000")
	if err == nil {
		t.Error("oldest instance Ox0000 should have been evicted")
	}
	// newest should survive
	newestID := fmt.Sprintf("Ox%04d", overCount-1)
	_, err = store.Get(newestID)
	if err != nil {
		t.Errorf("newest instance %s should survive: %v", newestID, err)
	}
}

func TestPruneNoExpired(t *testing.T) {
	store := newTestStore(t)

	inst := activeInstance("OxOnly")
	if err := store.Add(inst); err != nil {
		t.Fatalf("add: %v", err)
	}

	pruned, err := store.Prune()
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if pruned != 0 {
		t.Errorf("expected 0 pruned when nothing expired, got %d", pruned)
	}
}

func TestListFiltersExpired(t *testing.T) {
	store := newTestStore(t)

	expired := &Instance{
		AgentID:         "OxDead",
		ServerSessionID: "oxsid_dead",
		CreatedAt:       time.Now().Add(-48 * time.Hour),
		ExpiresAt:       time.Now().Add(-1 * time.Hour),
	}
	active := activeInstance("OxLive")

	if err := store.Add(expired); err != nil {
		t.Fatalf("add expired: %v", err)
	}
	if err := store.Add(active); err != nil {
		t.Fatalf("add active: %v", err)
	}

	list, err := store.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("expected 1 active instance, got %d", len(list))
	}
	if list[0].AgentID != "OxLive" {
		t.Errorf("expected OxLive, got %q", list[0].AgentID)
	}
}

func TestShouldCompact(t *testing.T) {
	store := newTestStore(t)

	tests := []struct {
		name         string
		totalCount   int
		expiredCount int
		want         bool
	}{
		{"no expired", 10, 0, false},
		{"high expired ratio", 10, 6, true},             // 60% > compactionExpiredRatio (50%)
		{"entry count exceeds threshold", 501, 1, true}, // > compactionCountThreshold
		{"low expired ratio, low count", 10, 1, false},  // 10% < pruned ratio threshold
		{"at pruned ratio boundary", 100, 11, true},     // 11% > compactionPrunedRatio (10%)
		{"just below pruned ratio", 100, 10, false},     // 10% == threshold, not >
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := store.shouldCompact(tt.totalCount, tt.expiredCount)
			if got != tt.want {
				t.Errorf("shouldCompact(%d, %d) = %v, want %v", tt.totalCount, tt.expiredCount, got, tt.want)
			}
		})
	}
}

func TestShouldCompactFileSize(t *testing.T) {
	store := newTestStore(t)

	// write enough data to exceed compactionSizeThreshold (200KB)
	for i := 0; i < 1500; i++ {
		inst := &Instance{
			AgentID:         fmt.Sprintf("Ox%04d", i),
			ServerSessionID: fmt.Sprintf("oxsid_%04d", i),
			CreatedAt:       time.Now(),
			ExpiresAt:       time.Now().Add(24 * time.Hour),
			AgentType:       "claude-code",
			Model:           "claude-opus-4-5-20250120",
		}
		if err := store.Add(inst); err != nil {
			t.Fatalf("add %d: %v", i, err)
		}
	}

	info, err := os.Stat(store.instancesPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Size() <= compactionSizeThreshold {
		t.Skip("file not large enough to test size-based compaction")
	}

	// at least 1 expired needed for shouldCompact to return true
	got := store.shouldCompact(1500, 1)
	if !got {
		t.Error("shouldCompact should trigger based on file size exceeding threshold")
	}
}

func TestCountReturnsZeroOnEmptyStore(t *testing.T) {
	store := newTestStore(t)
	if got := store.Count(); got != 0 {
		t.Errorf("Count() = %d on empty store, want 0", got)
	}
}

func TestReadInstancesFromNonExistentFile(t *testing.T) {
	store := newTestStore(t)
	// remove the directory so the file cannot exist
	os.RemoveAll(filepath.Dir(store.instancesPath))

	instances, total, expired, err := store.readInstances()
	if err != nil {
		t.Fatalf("readInstances should handle missing file: %v", err)
	}
	if len(instances) != 0 || total != 0 || expired != 0 {
		t.Errorf("expected empty results, got %d instances, %d total, %d expired", len(instances), total, expired)
	}
}

func TestUpdatePreservesOtherInstances(t *testing.T) {
	store := newTestStore(t)

	a := activeInstance("OxAaa1")
	a.Model = "model-a"
	b := activeInstance("OxBbb1")
	b.Model = "model-b"

	if err := store.Add(a); err != nil {
		t.Fatalf("add a: %v", err)
	}
	if err := store.Add(b); err != nil {
		t.Fatalf("add b: %v", err)
	}

	// update only b
	b.Model = "model-b-v2"
	if err := store.Update(b); err != nil {
		t.Fatalf("update b: %v", err)
	}

	// a should be untouched
	gotA, err := store.Get("OxAaa1")
	if err != nil {
		t.Fatalf("get a: %v", err)
	}
	if gotA.Model != "model-a" {
		t.Errorf("instance a model changed to %q, expected 'model-a'", gotA.Model)
	}

	gotB, err := store.Get("OxBbb1")
	if err != nil {
		t.Fatalf("get b: %v", err)
	}
	if gotB.Model != "model-b-v2" {
		t.Errorf("instance b model = %q, want 'model-b-v2'", gotB.Model)
	}
}

func TestJSONRoundTrip(t *testing.T) {
	// verify all Instance fields survive JSON serialization
	inst := &Instance{
		AgentID:         "OxRt01",
		ServerSessionID: "oxsid_roundtrip",
		CreatedAt:       time.Now().Truncate(time.Millisecond),
		ExpiresAt:       time.Now().Add(24 * time.Hour).Truncate(time.Millisecond),
		AgentType:       "claude-code",
		AgentVer:        "1.0.42",
		Model:           "claude-opus-4-5",
		ParentPID:       12345,
		ParentAgentID:   "OxPrnt",
		PrimeCallCount:  3,
	}

	data, err := json.Marshal(inst)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got Instance
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.AgentID != inst.AgentID {
		t.Errorf("AgentID: got %q, want %q", got.AgentID, inst.AgentID)
	}
	if got.ServerSessionID != inst.ServerSessionID {
		t.Errorf("ServerSessionID: got %q, want %q", got.ServerSessionID, inst.ServerSessionID)
	}
	if got.AgentVer != inst.AgentVer {
		t.Errorf("AgentVer: got %q, want %q", got.AgentVer, inst.AgentVer)
	}
	if got.ParentPID != inst.ParentPID {
		t.Errorf("ParentPID: got %d, want %d", got.ParentPID, inst.ParentPID)
	}
	if got.ParentAgentID != inst.ParentAgentID {
		t.Errorf("ParentAgentID: got %q, want %q", got.ParentAgentID, inst.ParentAgentID)
	}
	if got.PrimeCallCount != inst.PrimeCallCount {
		t.Errorf("PrimeCallCount: got %d, want %d", got.PrimeCallCount, inst.PrimeCallCount)
	}

	// verify "oxsid" JSON tag
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal raw: %v", err)
	}
	if _, ok := raw["oxsid"]; !ok {
		t.Error("ServerSessionID should serialize as 'oxsid' in JSON")
	}
}

func TestPruneEmptyStore(t *testing.T) {
	store := newTestStore(t)
	pruned, err := store.Prune()
	if err != nil {
		t.Fatalf("prune empty store: %v", err)
	}
	if pruned != 0 {
		t.Errorf("expected 0 pruned on empty store, got %d", pruned)
	}
}

// TestSharedStoreConcurrentSessions runs what every worktree of a repository
// now does against one shared store: sessions priming, re-priming, and looking
// themselves up at the same time, while a prune compacts expired records.
// Each Store stands in for a separate ox process, so only the file lock
// serializes them.
// Failure prevented: a concurrent write drops another session's record, or a
// lookup during a rewrite misses an instance that exists — "instance not
// found" for a live session.
func TestSharedStoreConcurrentSessions(t *testing.T) {
	if testing.Short() {
		t.Skip("short: contended file locks retry every 100ms")
	}
	root := t.TempDir()
	open := func() *Store {
		s, err := NewStoreForUser(root, "testuser")
		if err != nil {
			t.Fatalf("failed to create store: %v", err)
		}
		t.Cleanup(func() { s.pruneWG.Wait() })
		return s
	}

	seed := open()
	now := time.Now()
	if err := seed.Add(&Instance{AgentID: "OxLive", ServerSessionID: "oxsid_live", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatalf("seed live: %v", err)
	}
	for i := range 20 {
		if err := seed.Add(&Instance{AgentID: fmt.Sprintf("OxOld%d", i), ServerSessionID: "oxsid_old", CreatedAt: now.Add(-48 * time.Hour), ExpiresAt: now.Add(-time.Hour)}); err != nil {
			t.Fatalf("seed expired: %v", err)
		}
	}

	const sessions, reprimes = 8, 3
	var wg sync.WaitGroup
	errs := make(chan error, sessions*(reprimes+1)+100)
	done := make(chan struct{})

	wg.Add(1)
	go func() { // a session in yet another worktree keeps looking itself up
		defer wg.Done()
		reader := open()
		for {
			select {
			case <-done:
				return
			default:
			}
			if _, err := reader.Get("OxLive"); err != nil {
				errs <- fmt.Errorf("lookup of a live instance during writes: %w", err)
				return
			}
		}
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		if _, err := open().Prune(); err != nil {
			errs <- fmt.Errorf("prune: %w", err)
		}
	}()

	var writers sync.WaitGroup
	for i := range sessions {
		writers.Add(1)
		go func() {
			defer writers.Done()
			s := open()
			id := fmt.Sprintf("OxW%03d", i)
			if err := s.Add(&Instance{AgentID: id, ServerSessionID: "oxsid_" + id, CreatedAt: now, ExpiresAt: now.Add(time.Hour), PrimeCallCount: 1}); err != nil {
				errs <- fmt.Errorf("add %s: %w", id, err)
				return
			}
			for range reprimes {
				if _, _, err := s.IncrementPrimeCallCount(id); err != nil {
					errs <- fmt.Errorf("re-prime %s: %w", id, err)
					return
				}
			}
		}()
	}
	writers.Wait()
	close(done)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}

	check := open()
	for i := range sessions {
		id := fmt.Sprintf("OxW%03d", i)
		inst, err := check.Get(id)
		if err != nil {
			t.Errorf("session %s lost: %v", id, err)
			continue
		}
		if inst.PrimeCallCount != 1+reprimes {
			t.Errorf("session %s prime count = %d, want %d (a concurrent rewrite lost an update)", id, inst.PrimeCallCount, 1+reprimes)
		}
	}
	if _, err := check.Get("OxLive"); err != nil {
		t.Errorf("live instance lost: %v", err)
	}
}
