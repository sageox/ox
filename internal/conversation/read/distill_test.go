package read

import (
	"strings"
	"testing"
)

const (
	hiringTopic  = "tp_01a012cb-9764-7555-a3f3-ce3377e47d98"
	indexTopic   = "tp_01a012cb-9764-7555-a3f3-ce3377e47d99"
	unknownTopic = "tp_01a012cb-ffff-7fff-8fff-ffffffffffff"
)

func TestTopics(t *testing.T) {
	env := testReader(t).Topics(fullCnv)
	if !env.Success {
		t.Fatalf("Topics failed: %+v", env.Error)
	}
	data := env.Data.(*TopicsData)
	if data.Episode.Status != "draft" || data.Episode.ExtractedAt != "2026-08-18T02:55:27Z" {
		t.Errorf("episode = %+v", data.Episode)
	}
	// TTL recomputed from extracted_at + 1h with no extends; the stale
	// committed header value (03:55:27) happens to equal it here.
	if data.Episode.TTLExpiresAt != "2026-08-18T03:55:27Z" {
		t.Errorf("TTLExpiresAt = %q", data.Episode.TTLExpiresAt)
	}
	if len(data.Topics) != 2 {
		t.Fatalf("topics = %+v", data.Topics)
	}
	byID := map[string]TopicRow{}
	for _, tp := range data.Topics {
		byID[tp.ID] = tp
	}
	// Hiring has one current atom and one tombstone: count is current-only.
	if got := byID[hiringTopic]; got.AtomCount != 1 || got.Title != "Hiring" || len(got.CueURIs) != 1 {
		t.Errorf("hiring row = %+v", got)
	}
	if got := byID[indexTopic]; got.AtomCount != 1 {
		t.Errorf("index-trust row = %+v", got)
	}
	if data.AtomsTotal != 2 || data.AtomsSuperseded != 1 {
		t.Errorf("counts = %d current / %d superseded, want 2/1", data.AtomsTotal, data.AtomsSuperseded)
	}
	// The unusable "mystery" line surfaces as an advisory warning.
	if !hasWarningContaining(env.Warnings, "skipped") {
		t.Errorf("warnings = %v", env.Warnings)
	}
	if !strings.Contains(env.Guidance, "ox conversation topic ") {
		t.Errorf("guidance = %q", env.Guidance)
	}
}

func TestTopicsSkippedEpisode(t *testing.T) {
	env := testReader(t).Topics(skippedCnv)
	if !env.Success {
		t.Fatalf("Topics(skipped) failed: %+v", env.Error)
	}
	data := env.Data.(*TopicsData)
	if data.Episode.Status != "skipped" || data.Episode.SkippedReason != "cluster_exhausted_v2" {
		t.Errorf("episode = %+v", data.Episode)
	}
	if len(data.Topics) != 0 || data.AtomsTotal != 0 {
		t.Errorf("skipped episode payload = %+v", data)
	}
}

func TestTopicCurrentAndSuperseded(t *testing.T) {
	r := testReader(t)

	t.Run("default view is projected-current only", func(t *testing.T) {
		env := r.Topic(fullCnv, hiringTopic, false)
		if !env.Success {
			t.Fatalf("Topic failed: %+v", env.Error)
		}
		data := env.Data.(*TopicData)
		if data.Topic.ID != hiringTopic || data.Topic.Title != "Hiring" {
			t.Errorf("topic header = %+v", data.Topic)
		}
		if len(data.Atoms) != 1 || data.Atoms[0].ID != "at_01a012cb-9763-73bd-88a1-f299816da945" {
			t.Fatalf("atoms = %+v", data.Atoms)
		}
		a := data.Atoms[0]
		if a.Kind != "decision" || a.Signal != "high" || a.Confidence != 0.95 {
			t.Errorf("atom = %+v", a)
		}
		if a.Quote == nil || a.Quote.CueRef != 5 {
			t.Errorf("quote = %+v", a.Quote)
		}
		if a.Source == nil || len(a.Source.URIs) != 1 || !strings.HasPrefix(a.Source.URIs[0], "sageox://") {
			t.Errorf("source = %+v", a.Source)
		}
		if a.ValidTo != "" || a.SupersededBy != "" {
			t.Errorf("current atom leaked tombstone fields: %+v", a)
		}
		if data.AtomsTotal != 1 || data.AtomsSuperseded != 1 {
			t.Errorf("counts = %d/%d, want 1/1", data.AtomsTotal, data.AtomsSuperseded)
		}
	})

	t.Run("include-superseded adds auditable tombstones", func(t *testing.T) {
		env := r.Topic(fullCnv, hiringTopic, true)
		data := env.Data.(*TopicData)
		if len(data.Atoms) != 2 {
			t.Fatalf("atoms = %+v", data.Atoms)
		}
		var tomb *AtomView
		for i := range data.Atoms {
			if data.Atoms[i].ID == "at_01a012cb-9763-73bd-88a1-f299816da947" {
				tomb = &data.Atoms[i]
			}
		}
		if tomb == nil {
			t.Fatal("tombstone atom missing")
		}
		if tomb.ValidFrom == "" || tomb.ValidTo != "2026-08-18T02:56:00Z" || tomb.SupersededBy != "at_01a012cb-9763-73bd-88a1-f299816da945" {
			t.Errorf("tombstone = %+v", tomb)
		}
	})

	t.Run("legacy singular source uri folds into uris", func(t *testing.T) {
		env := r.Topic(fullCnv, indexTopic, false)
		data := env.Data.(*TopicData)
		if len(data.Atoms) != 1 {
			t.Fatalf("atoms = %+v", data.Atoms)
		}
		if uris := data.Atoms[0].Source.URIs; len(uris) != 1 || !strings.Contains(uris[0], "dsc_") {
			t.Errorf("legacy uri fold = %v", uris)
		}
	})
}

func TestDistillationTypedErrors(t *testing.T) {
	r := testReader(t)
	tests := []struct {
		name     string
		run      func() *Envelope
		wantCode string
	}{
		{name: "topics without distillation", run: func() *Envelope { return r.Topics(legacyCnv) }, wantCode: ErrCodeNoDistillation},
		{name: "topic without distillation", run: func() *Envelope { return r.Topic(legacyCnv, hiringTopic, false) }, wantCode: ErrCodeNoDistillation},
		{name: "topics invalid id", run: func() *Envelope { return r.Topics("nope") }, wantCode: ErrCodeInvalidID},
		{name: "topics unindexed", run: func() *Envelope { return r.Topics(unknownCnv) }, wantCode: ErrCodeNotIndexed},
		{name: "topic by title rejected", run: func() *Envelope { return r.Topic(fullCnv, "Hiring", false) }, wantCode: ErrCodeInvalidID},
		{name: "topic not found", run: func() *Envelope { return r.Topic(fullCnv, unknownTopic, false) }, wantCode: ErrCodeTopicNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := tt.run()
			if env.Success || env.Error == nil {
				t.Fatalf("succeeded, want %s", tt.wantCode)
			}
			if env.Error.Code != tt.wantCode {
				t.Errorf("code = %s, want %s", env.Error.Code, tt.wantCode)
			}
		})
	}
}
