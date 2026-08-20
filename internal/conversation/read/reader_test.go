package read

import (
	"errors"
	"testing"
	"time"
)

// testReader opens the committed fixture corpus with a fixed last-sync
// instant.
func testReader(t *testing.T) *Reader {
	t.Helper()
	return New("testdata/discussions", time.Date(2026, 8, 20, 17, 41, 0, 0, time.UTC))
}

const (
	legacyCnv       = "cnv_019ff370-e195-7d1c-a727-39a1a85823f2"
	bothCnv         = "cnv_019ffc00-0000-7000-8000-000000000004"
	skippedCnv      = "cnv_019ffd00-0000-7000-8000-000000000005"
	noTranscriptCnv = "cnv_019ffe00-0000-7000-8000-000000000006"
	// unknownCnv is strictly valid but absent from the fixture index.
	unknownCnv = "cnv_019fffff-ffff-7fff-8fff-ffffffffffff"
)

func TestOpenNoTeamContext(t *testing.T) {
	r, err := Open(t.TempDir())
	if err == nil {
		t.Fatalf("Open on an uninitialized repo returned a reader: %+v", r)
	}
	if err.Code != ErrCodeNoTeamContext {
		t.Fatalf("Open error code = %s, want %s", err.Code, ErrCodeNoTeamContext)
	}
}

func TestListDropsPhantomsAndSortsNewestFirst(t *testing.T) {
	env := testReader(t).List(ListOptions{})
	if !env.Success {
		t.Fatalf("List failed: %+v", env.Error)
	}
	data := env.Data.(*ListData)
	// 7 parseable object entries (2 malformed rows are skipped by the
	// format loader before the count).
	if data.TotalIndexed != 7 {
		t.Errorf("TotalIndexed = %d, want 7", data.TotalIndexed)
	}
	// Phantom entry and hostile ../escape entry are dropped in the same pass.
	var ids []string
	for _, c := range data.Conversations {
		ids = append(ids, c.ConversationID)
	}
	want := []string{noTranscriptCnv, skippedCnv, bothCnv, legacyCnv, fullCnv}
	if len(ids) != len(want) {
		t.Fatalf("rows = %v, want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("newest-first order: rows = %v, want %v", ids, want)
		}
	}
}

func TestListRowDerivedFields(t *testing.T) {
	env := testReader(t).List(ListOptions{})
	data := env.Data.(*ListData)
	byID := map[string]ConversationRow{}
	for _, c := range data.Conversations {
		byID[c.ConversationID] = c
	}

	full := byID[fullCnv]
	// recorded_at derives from the UUIDv7 timestamp embedded in the
	// recording id: 0x019ff2f52079 ms = 2026-08-11T22:32:58.745Z.
	if full.RecordedAt != "2026-08-11T22:32:58Z" {
		t.Errorf("full RecordedAt = %q, want 2026-08-11T22:32:58Z", full.RecordedAt)
	}
	if !full.HasDistillation {
		t.Error("full HasDistillation = false, want true")
	}
	if full.RecordingID != fullRec {
		t.Errorf("full RecordingID = %q, want %q", full.RecordingID, fullRec)
	}
	// Index and metadata titles are both empty: folder-name fallback (D13).
	if full.Title != "2026-08-11-22-32-full" {
		t.Errorf("full Title = %q, want folder-name fallback", full.Title)
	}
	if full.DecisionCount != 1 || len(full.Participants) != 1 || full.Participants[0] != "Galex Yen" {
		t.Errorf("full row index fields wrong: %+v", full)
	}

	legacy := byID[legacyCnv]
	if legacy.HasDistillation {
		t.Error("legacy HasDistillation = true, want false")
	}
	if legacy.Title != "Legacy Era Discussion" {
		t.Errorf("legacy Title = %q, want index title", legacy.Title)
	}

	// Empty index title falls through to metadata.json before folder name.
	noTr := byID[noTranscriptCnv]
	if noTr.Title != "Metadata Title Fallback" {
		t.Errorf("no-transcript Title = %q, want metadata fallback", noTr.Title)
	}
}

func TestListLimitAndSince(t *testing.T) {
	r := testReader(t)

	env := r.List(ListOptions{Limit: 2})
	data := env.Data.(*ListData)
	if len(data.Conversations) != 2 || !data.Truncated {
		t.Fatalf("Limit 2: rows = %d truncated = %v, want 2 true", len(data.Conversations), data.Truncated)
	}
	if data.Conversations[0].ConversationID != noTranscriptCnv {
		t.Errorf("Limit keeps newest first, got %s", data.Conversations[0].ConversationID)
	}

	// The derived recorded_at instants come from the UUIDv7 timestamps, which
	// for the fabricated fixture ids land on 2026-08-13/14; this bound keeps
	// the three newest rows and drops legacy + full.
	since := time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)
	env = r.List(ListOptions{Since: since})
	data = env.Data.(*ListData)
	if len(data.Conversations) != 3 {
		t.Fatalf("Since %s: rows = %d, want 3", since, len(data.Conversations))
	}
	for _, c := range data.Conversations {
		if c.ConversationID == fullCnv || c.ConversationID == legacyCnv {
			t.Errorf("Since filter kept %s", c.ConversationID)
		}
	}
}

func TestListEnvelopeContract(t *testing.T) {
	env := testReader(t).List(ListOptions{})
	if env.LastSync != "2026-08-20T17:41:00Z" {
		t.Errorf("LastSync = %q, want 2026-08-20T17:41:00Z", env.LastSync)
	}
	if env.TokenEstimate <= 0 {
		t.Errorf("TokenEstimate = %d, want > 0", env.TokenEstimate)
	}
	if env.Guidance == "" || env.Error != nil {
		t.Errorf("envelope guidance/error wrong: %+v", env)
	}
	if env.ElapsedMS < 0 {
		t.Errorf("ElapsedMS = %d", env.ElapsedMS)
	}
}

func TestLookupMissIsNotIndexed(t *testing.T) {
	env := testReader(t).Show(unknownCnv)
	if env.Success || env.Error == nil || env.Error.Code != ErrCodeNotIndexed {
		t.Fatalf("unknown id envelope = %+v, want not_indexed", env)
	}
	// A phantom entry's id resolves to no live folder either.
	env = testReader(t).Show("cnv_019f0000-0000-7000-8000-000000000009")
	if env.Error == nil || env.Error.Code != ErrCodeNotIndexed {
		t.Fatalf("phantom id envelope error = %+v, want not_indexed", env.Error)
	}
	// The hostile ../escape entry is dropped, never resolved.
	env = testReader(t).Show("cnv_019f1111-0000-7000-8000-000000000010")
	if env.Error == nil || env.Error.Code != ErrCodeNotIndexed {
		t.Fatalf("hostile-folder id envelope error = %+v, want not_indexed", env.Error)
	}
}

// fakeResolver exercises the D3 fallback seam.
type fakeResolver struct {
	folder string
	err    error
}

func (f fakeResolver) ResolveFolder(string) (string, error) { return f.folder, f.err }

func TestFallbackSeam(t *testing.T) {
	t.Run("resolves an unindexed id to a live folder", func(t *testing.T) {
		r := testReader(t)
		r.SetFallback(fakeResolver{folder: "2026-08-11-22-32-full"})
		env := r.Show(unknownCnv)
		if !env.Success {
			t.Fatalf("fallback Show failed: %+v", env.Error)
		}
	})
	t.Run("server-supplied path passes the same guard", func(t *testing.T) {
		r := testReader(t)
		r.SetFallback(fakeResolver{folder: "../escape"})
		env := r.Show(unknownCnv)
		if env.Success || env.Error.Code != ErrCodeReadError {
			t.Fatalf("hostile fallback folder envelope = %+v, want read_error", env)
		}
	})
	t.Run("failing fallback stays not_indexed", func(t *testing.T) {
		r := testReader(t)
		r.SetFallback(fakeResolver{err: errors.New("offline")})
		env := r.Show(unknownCnv)
		if env.Error == nil || env.Error.Code != ErrCodeNotIndexed {
			t.Fatalf("failing fallback envelope = %+v, want not_indexed", env)
		}
	})
}

func TestListMissingIndexIsEmpty(t *testing.T) {
	r := New(t.TempDir(), time.Time{})
	env := r.List(ListOptions{})
	if !env.Success {
		t.Fatalf("List over empty root failed: %+v", env.Error)
	}
	data := env.Data.(*ListData)
	if len(data.Conversations) != 0 || data.TotalIndexed != 0 {
		t.Errorf("empty root list = %+v", data)
	}
	if env.LastSync != "" {
		t.Errorf("zero last sync serialized as %q", env.LastSync)
	}
}

func TestDeriveRecordedAtFallbacks(t *testing.T) {
	if _, ok := uuidv7Time("rec_not-a-uuid"); ok {
		t.Error("uuidv7Time accepted a malformed id")
	}
	if ts, ok := folderNameDate("2026-08-11-22-32-full"); !ok || !ts.Equal(time.Date(2026, 8, 11, 22, 32, 0, 0, time.UTC)) {
		t.Errorf("folderNameDate = %v %v", ts, ok)
	}
	if _, ok := folderNameDate("short"); ok {
		t.Error("folderNameDate accepted a short name")
	}
}
