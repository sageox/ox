package contextuse

import (
	"testing"

	"github.com/stretchr/testify/assert"

	ct "github.com/sageox/ox/internal/session/contexttrace"
)

func TestSurfaced(t *testing.T) {
	tests := []struct {
		name   string
		events []ct.Event
		want   []SurfacedRef
	}{
		{name: "empty", events: nil, want: []SurfacedRef{}},
		{
			name: "whisper uses ref, falls back to whisper id, keeps metadata",
			events: []ct.Event{
				{Type: ct.EventProvided, Source: ct.SourceTeamWhisper, Ref: "Ox7f2a", Title: "auth", FromName: "Dana", Seq: 3},
				{Type: ct.EventProvided, Source: ct.SourceProjectWhisper, WhisperID: "Ox9b1c", From: "riley", URL: "u"},
			},
			want: []SurfacedRef{
				{Ref: "Ox7f2a", RefType: "murmur", Via: ViaWhisper, Title: "auth", FromName: "Dana", Seq: 3},
				{Ref: "Ox9b1c", RefType: "murmur", Via: ViaWhisper, FromName: "riley", URL: "u"},
			},
		},
		{
			name: "prime docs from doc and docs",
			events: []ct.Event{
				{Type: ct.EventProvided, Source: ct.SourceTeamMemory, Doc: "MEMORY.md"},
				{Type: ct.EventProvided, Source: ct.SourceTeamDocs, Docs: []string{"SOUL.md", "MEMORY.md"}},
			},
			want: []SurfacedRef{
				{Ref: "MEMORY.md", RefType: "doc", Via: ViaPrime},
				{Ref: "SOUL.md", RefType: "doc", Via: ViaPrime},
			},
		},
		{
			name: "local recall with ref and with docs",
			events: []ct.Event{
				{Type: ct.EventProvided, Source: ct.SourceLocalRecall, Mechanism: ct.MechanismInjection, Ref: "2026-10-01-dana-auth", RefType: "session", Title: "auth retry", Seq: 5},
				{Type: ct.EventProvided, Source: ct.SourceLocalRecall, Ref: "Ox1111"},
				{Type: ct.EventProvided, Source: ct.SourceLocalRecall, Doc: "plan.md"},
			},
			want: []SurfacedRef{
				{Ref: "2026-10-01-dana-auth", RefType: "session", Via: ViaRecall, Title: "auth retry", Seq: 5},
				{Ref: "Ox1111", RefType: "session", Via: ViaRecall},
				{Ref: "plan.md", RefType: "doc", Via: ViaRecall},
			},
		},
		{
			name: "query hits via retrieval",
			events: []ct.Event{
				{Type: ct.EventProvided, Source: ct.SourceOnDemand, Mechanism: ct.MechanismRetrieval, Ref: "cnv_1", RefType: "discussion", Title: "t", URL: "https://sageox.ai/c/cnv_1", Seq: 8},
				{Type: ct.EventProvided, Source: ct.SourceOnDemand, Mechanism: ct.MechanismRetrieval, Ref: "x.md"},
			},
			want: []SurfacedRef{
				{Ref: "cnv_1", RefType: "discussion", Via: ViaRetrieval, Title: "t", URL: "https://sageox.ai/c/cnv_1", Seq: 8},
				{Ref: "x.md", RefType: "doc", Via: ViaRetrieval},
			},
		},
		{
			name: "invoked with query, ref preferred over query",
			events: []ct.Event{
				{Type: ct.EventInvoked, Source: ct.SourceOxCLI, Command: "query", Query: "retry budget", Seq: 2},
				{Type: ct.EventInvoked, Source: ct.SourceOxCLI, Command: "plan show", Query: "auth-plan", Ref: "auth-plan", RefType: "plan"},
				{Type: ct.EventInvoked, Source: ct.SourceOxCLI, Command: "murmur list"},
			},
			want: []SurfacedRef{
				{Ref: "retry budget", RefType: "query", Via: ViaRetrieval, Seq: 2},
				{Ref: "auth-plan", RefType: "plan", Via: ViaRetrieval},
			},
		},
		{
			name: "dedup keeps first by ref type and ref; skips empty and non-surfacing events",
			events: []ct.Event{
				{Type: ct.EventProvided, Source: ct.SourceTeamWhisper, Ref: "Ox7f2a", Seq: 1},
				{Type: ct.EventProvided, Source: ct.SourceTeamWhisper, Ref: "Ox7f2a", Seq: 9},
				{Type: ct.EventProvided, Source: ct.SourceTeamWhisper},
				{Type: ct.EventProvided, Source: ct.SourceTeamDocs, Doc: "Ox7f2a"},
				{Type: ct.EventUsed, Ref: "Ox7f2a"},
				{Type: ct.EventConsulted, Ref: "zzz"},
			},
			want: []SurfacedRef{
				{Ref: "Ox7f2a", RefType: "murmur", Via: ViaWhisper, Seq: 1},
				{Ref: "Ox7f2a", RefType: "doc", Via: ViaPrime},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, Surfaced(tt.events))
		})
	}
}

func TestSurfaced_Uncapped(t *testing.T) {
	var events []ct.Event
	for i := range 40 {
		events = append(events, ct.Event{Type: ct.EventProvided, Source: ct.SourceTeamWhisper, Ref: string(rune('A'+i%26)) + string(rune('a'+i/26))})
	}
	assert.Len(t, Surfaced(events), 40)
}

func TestSurfacedWith_IsRetrieval(t *testing.T) {
	events := []ct.Event{
		{Type: ct.EventInvoked, Command: "query", Query: "q1"},
		{Type: ct.EventInvoked, Command: "murmur", Query: "q2"},
	}
	got := SurfacedWith(events, SurfacedOptions{IsRetrieval: func(ev ct.Event) bool { return ev.Command == "query" }})
	assert.Equal(t, []SurfacedRef{{Ref: "q1", RefType: "query", Via: ViaRetrieval}}, got)
}
