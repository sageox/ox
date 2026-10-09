package githubmirror

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeStateFile(t *testing.T, ledger string, contents []byte) {
	t.Helper()
	path := StatePath(ledger)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatalf("write state file: %v", err)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

func assertFreshState(t *testing.T, s *State) {
	t.Helper()
	if s == nil {
		t.Fatal("LoadState returned a nil State; callers must always get a usable one")
	}
	if s.Version != StateVersion {
		t.Errorf("Version = %d, want %d", s.Version, StateVersion)
	}
	if s.Items == nil || len(s.Items) != 0 {
		t.Errorf("Items = %v, want a non-nil empty map", s.Items)
	}
	if s.ColdStartDone || !s.PullRequestCursor.IsZero() || !s.IssueCursor.IsZero() || s.Repo != "" || s.Team != "" {
		t.Errorf("fresh state carries leftovers: %+v", s)
	}
}

func TestStatePath(t *testing.T) {
	t.Parallel()
	ledger := filepath.Join("some", "ledger")
	want := filepath.Join(ledger, ".sageox", "cache", "github_mirror", "state.json")
	if got := StatePath(ledger); got != want {
		t.Errorf("StatePath = %q, want %q", got, want)
	}
}

// Failure prevented: a first run (or a fresh ledger clone) treated as a fault,
// or handed a nil map the daemon would panic writing to.
func TestLoadState_MissingFileIsEmptyNotError(t *testing.T) {
	t.Parallel()
	s, err := LoadState(t.TempDir())
	if err != nil {
		t.Fatalf("missing file must not be an error: %v", err)
	}
	assertFreshState(t, s)
}

// Failure prevented: relay state lost across daemon restarts, so every item is
// re-fetched and re-relayed after each restart.
func TestSaveLoadState_RoundTrip(t *testing.T) {
	t.Parallel()
	ledger := t.TempDir()
	in := &State{
		Repo:              "acme/api",
		Team:              "team_acme",
		PullRequestCursor: at(100),
		IssueCursor:       at(90),
		ColdStartDone:     true,
		LastAttemptAt:     at(101),
		LastSuccessAt:     at(99),
		LastError:         "relay: 503",
		LastErrorAt:       at(98),
		NextAllowedAt:     at(102),
		RepoStatus:        RepoEnabled,
		RepoMeta:          &Repo{Owner: "acme", Name: "api", FullName: "acme/api", ID: 4242, Private: true},
		RepoMetaAt:        at(97),
		Items: map[string]ItemState{
			"github.com/acme/api/pull/1287": {
				UpdatedAt:            at(50),
				ChangeHash:           goldenHash,
				LastMaterialChangeAt: at(40),
				Status:               ResultAccepted,
				RelayedAt:            at(60),
			},
			"github.com/acme/api/issues/9": {
				UpdatedAt:  at(51),
				ChangeHash: goldenEscapingHash,
				Status:     ResultRejected,
				Reason:     "validation: title too long",
				RelayedAt:  at(61),
			},
		},
	}
	if err := SaveState(ledger, in); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	if in.Version != StateVersion {
		t.Errorf("SaveState must stamp Version, got %d", in.Version)
	}

	out, err := LoadState(ledger)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	// compare the serialized form: time.Time round-trips lose monotonic and
	// location pointers that reflect.DeepEqual would trip over.
	if got, want := mustJSON(t, out), mustJSON(t, in); got != want {
		t.Errorf("round trip changed the state\n got  %s\n want %s", got, want)
	}
	if got := out.Items["github.com/acme/api/pull/1287"]; got.ChangeHash != goldenHash || got.Status != ResultAccepted || !got.LastMaterialChangeAt.Equal(at(40)) {
		t.Errorf("item state lost fields: %+v", got)
	}
	if out.RepoMeta == nil || !out.RepoMeta.Private {
		t.Errorf("RepoMeta lost: %+v", out.RepoMeta)
	}
	if out.Team != "team_acme" || !out.PullRequestCursor.Equal(at(100)) || !out.IssueCursor.Equal(at(90)) {
		t.Errorf("team or per-kind cursors lost: team=%q pr=%v issue=%v", out.Team, out.PullRequestCursor, out.IssueCursor)
	}
}

// Failure prevented: a state file written before relay history recorded its
// team (or before cursors were per kind) failing to load, which would throw the
// history away on upgrade. It must load, with no team claimed.
func TestLoadState_FileFromBeforeTeamsAndPerKindCursors(t *testing.T) {
	t.Parallel()
	ledger := t.TempDir()
	writeStateFile(t, ledger, []byte(`{"version":1,"repo":"acme/api","cursor":"2026-10-01T00:00:00Z","cold_start_done":true,`+
		`"items":{"github.com/acme/api/pull/1":{"change_hash":"sha256:abc","status":"accepted"}}}`))

	s, err := LoadState(ledger)
	if err != nil {
		t.Fatalf("an older state file must load: %v", err)
	}
	if s.Repo != "acme/api" || !s.ColdStartDone || len(s.Items) != 1 {
		t.Errorf("history lost: %+v", s)
	}
	if s.Team != "" || !s.PullRequestCursor.IsZero() || !s.IssueCursor.IsZero() {
		t.Errorf("an older file must claim no team and no cursors: team=%q pr=%v issue=%v", s.Team, s.PullRequestCursor, s.IssueCursor)
	}
}

// Failure prevented: a crash mid-save leaving a half-written state file, or
// temp files accumulating in the ledger cache.
func TestSaveState_AtomicAndTidy(t *testing.T) {
	t.Parallel()
	ledger := t.TempDir()

	for i, repo := range []string{"acme/first", "acme/second"} {
		if err := SaveState(ledger, &State{Repo: repo}); err != nil {
			t.Fatalf("SaveState #%d: %v", i, err)
		}
	}

	entries, err := os.ReadDir(filepath.Dir(StatePath(ledger)))
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "state.json" {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Errorf("state dir contains %v, want only state.json (no leftover temp files)", names)
	}

	raw, err := os.ReadFile(StatePath(ledger))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.HasSuffix(string(raw), "}\n") || !json.Valid(raw) {
		t.Errorf("state file is not a complete JSON document: %q", raw)
	}
	s, err := LoadState(ledger)
	if err != nil || s.Repo != "acme/second" {
		t.Errorf("second save must replace the first: repo=%q err=%v", s.Repo, err)
	}
}

func TestSaveState_NilItemsWritesEmptyObject(t *testing.T) {
	t.Parallel()
	ledger := t.TempDir()
	if err := SaveState(ledger, &State{Repo: "acme/api"}); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	raw, err := os.ReadFile(StatePath(ledger))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(raw), `"items": {}`) {
		t.Errorf("nil Items must be written as {}, got: %s", raw)
	}
}

// Failure prevented: a bad state file wedging the mirror forever, or the
// failure being swallowed so nobody knows the history was lost. Each case
// must hand back a usable fresh State AND an error to log.
func TestLoadState_UnusableFile(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		setup func(t *testing.T, ledger string)
	}{
		{"garbage bytes", func(t *testing.T, ledger string) { writeStateFile(t, ledger, []byte("\x00\x01 not json")) }},
		{"truncated json", func(t *testing.T, ledger string) {
			writeStateFile(t, ledger, []byte(`{"version":1,"repo":"acme/api","items":{"github.com/acme/api/pull/1":{"updated_at":`))
		}},
		{"empty file", func(t *testing.T, ledger string) { writeStateFile(t, ledger, nil) }},
		{"wrong shape", func(t *testing.T, ledger string) { writeStateFile(t, ledger, []byte(`{"version":1,"items":[1,2,3]}`)) }},
		{"newer format version", func(t *testing.T, ledger string) {
			writeStateFile(t, ledger, []byte(`{"version":99,"repo":"acme/api","cold_start_done":true,"items":{}}`))
		}},
		{"a directory where the file should be", func(t *testing.T, ledger string) {
			if err := os.MkdirAll(StatePath(ledger), 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ledger := t.TempDir()
			tt.setup(t, ledger)
			s, err := LoadState(ledger)
			if err == nil {
				t.Fatal("an unusable state file must be reported")
			}
			assertFreshState(t, s)
			if !strings.Contains(err.Error(), StatePath(ledger)) {
				t.Errorf("error should name the file: %v", err)
			}
		})
	}
}

func TestLoadState_CorruptErrorKeepsItsCause(t *testing.T) {
	t.Parallel()
	ledger := t.TempDir()
	writeStateFile(t, ledger, []byte("{nope"))
	_, err := LoadState(ledger)
	var syntax *json.SyntaxError
	if !errors.As(err, &syntax) {
		t.Errorf("error %v should wrap *json.SyntaxError", err)
	}
}

// Failure prevented: a partially parsed corrupt file leaking its fields (a
// cursor, cold_start_done) into the "fresh" state, so the daemon skips a cold
// start it never completed.
func TestLoadState_CorruptFileDoesNotLeakPartialFields(t *testing.T) {
	t.Parallel()
	ledger := t.TempDir()
	writeStateFile(t, ledger, []byte(`{"version":1,"repo":"acme/api","cold_start_done":true,"cursor":"2026-10-01T00:00:00Z","items":[`))
	s, err := LoadState(ledger)
	if err == nil {
		t.Fatal("expected an error")
	}
	assertFreshState(t, s)
}

func TestLoadState_TolerantOfSparseValidFiles(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		body string
	}{
		{"empty object", `{}`},
		{"null", `null`},
		{"items explicitly null", `{"version":1,"items":null}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ledger := t.TempDir()
			writeStateFile(t, ledger, []byte(tt.body))
			s, err := LoadState(ledger)
			if err != nil {
				t.Fatalf("a valid sparse file must load: %v", err)
			}
			assertFreshState(t, s)
		})
	}
}

// Failure prevented: an empty ledger path resolving against the working
// directory and reading or writing .sageox/cache/ inside whatever repo the
// daemon happened to be in.
func TestState_RejectsEmptyLedgerPath(t *testing.T) {
	t.Chdir(t.TempDir())

	s, err := LoadState("")
	if err == nil {
		t.Error("LoadState(\"\") must fail")
	}
	assertFreshState(t, s)

	if err := SaveState("", &State{}); err == nil {
		t.Error("SaveState(\"\") must fail")
	}
	if _, statErr := os.Stat(".sageox"); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("SaveState(\"\") wrote into the working directory (stat err: %v)", statErr)
	}
}

func TestSaveState_NilStateFails(t *testing.T) {
	t.Parallel()
	ledger := t.TempDir()
	if err := SaveState(ledger, nil); err == nil {
		t.Fatal("SaveState(nil) must fail")
	}
	if _, err := os.Stat(StatePath(ledger)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("SaveState(nil) must not create a file (stat err: %v)", err)
	}
}

// Failure prevented: a write failure reported as success, so the daemon
// believes progress was recorded when nothing was.
func TestSaveState_WriteFailuresAreReported(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		setup func(t *testing.T, ledger string)
	}{
		{"a regular file where the .sageox directory belongs", func(t *testing.T, ledger string) {
			if err := os.WriteFile(filepath.Join(ledger, ".sageox"), []byte("x"), 0o600); err != nil {
				t.Fatalf("write: %v", err)
			}
		}},
		{"a directory where state.json belongs", func(t *testing.T, ledger string) {
			if err := os.MkdirAll(StatePath(ledger), 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			if err := os.WriteFile(filepath.Join(StatePath(ledger), "keep"), []byte("x"), 0o600); err != nil {
				t.Fatalf("write: %v", err)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ledger := t.TempDir()
			tt.setup(t, ledger)
			if err := SaveState(ledger, &State{Repo: "acme/api"}); err == nil {
				t.Fatal("expected SaveState to report the failure")
			}
		})
	}
}

// Failure prevented: a state file that grows forever, or one that forgets an
// item still inside its 90-day window and re-relays it.
func TestState_Prune(t *testing.T) {
	t.Parallel()
	now := at(0).Add(1000 * 24 * time.Hour)
	expiry := func(d time.Duration) time.Time { return now.Add(-Window).Add(d) } // change time that expires at now+d

	items := map[string]ItemState{
		"expired by a day":                    {LastMaterialChangeAt: expiry(-24 * time.Hour)},
		"expired by a nanosecond":             {LastMaterialChangeAt: expiry(-time.Nanosecond)},
		"expires exactly now":                 {LastMaterialChangeAt: expiry(0)},
		"expires in a nanosecond":             {LastMaterialChangeAt: expiry(time.Nanosecond)},
		"expires in a day":                    {LastMaterialChangeAt: expiry(24 * time.Hour)},
		"changed just now":                    {LastMaterialChangeAt: now},
		"no change time, relayed long ago":    {RelayedAt: expiry(-48 * time.Hour)},
		"no change time, relayed recently":    {RelayedAt: expiry(48 * time.Hour)},
		"change time wins over relay time":    {LastMaterialChangeAt: expiry(-48 * time.Hour), RelayedAt: now},
		"old relay time ignored when changed": {LastMaterialChangeAt: expiry(48 * time.Hour), RelayedAt: expiry(-480 * time.Hour)},
		"no timestamps at all":                {},
	}
	wantKept := map[string]bool{
		"expires exactly now":                 true, // "before now" is strict; the next cycle drops it
		"expires in a nanosecond":             true,
		"expires in a day":                    true,
		"changed just now":                    true,
		"no change time, relayed recently":    true,
		"old relay time ignored when changed": true,
	}

	// Prune deletes from the map it is given, so record the keys first.
	allKeys := make([]string, 0, len(items))
	for key := range items {
		allKeys = append(allKeys, key)
	}

	s := &State{Items: items}
	s.Prune(now)

	for _, key := range allKeys {
		_, kept := s.Items[key]
		if kept != wantKept[key] {
			t.Errorf("%q: kept=%v, want %v", key, kept, wantKept[key])
		}
	}
}

func TestState_PruneEdgeCases(t *testing.T) {
	t.Parallel()
	t.Run("nil items does not panic", func(t *testing.T) {
		t.Parallel()
		(&State{}).Prune(at(0))
	})
	t.Run("keeps the other fields", func(t *testing.T) {
		t.Parallel()
		s := &State{Repo: "acme/api", Team: "team_acme", PullRequestCursor: at(5), IssueCursor: at(4), ColdStartDone: true, Items: map[string]ItemState{"old": {}}}
		s.Prune(at(0))
		if s.Repo != "acme/api" || s.Team != "team_acme" || !s.ColdStartDone || !s.PullRequestCursor.Equal(at(5)) || !s.IssueCursor.Equal(at(4)) {
			t.Errorf("Prune touched non-item fields: %+v", s)
		}
	})
	t.Run("agrees with Expired for items away from the boundary", func(t *testing.T) {
		t.Parallel()
		now := at(0).Add(500 * 24 * time.Hour)
		for _, offset := range []time.Duration{-100 * 24 * time.Hour, -time.Hour, time.Hour, 100 * 24 * time.Hour} {
			changed := now.Add(-Window).Add(offset)
			s := &State{Items: map[string]ItemState{"k": {LastMaterialChangeAt: changed}}}
			s.Prune(now)
			_, kept := s.Items["k"]
			if expired := Expired(Item{LastMaterialChangeAt: changed}, now); kept == expired {
				t.Errorf("offset %v: kept=%v but Expired=%v; an item must be kept exactly when it is not expired", offset, kept, expired)
			}
		}
	})
}
