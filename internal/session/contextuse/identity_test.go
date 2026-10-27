package contextuse

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	ct "github.com/sageox/ox/internal/session/contexttrace"
)

const (
	tUUID  = "019f8c03-6f90-7ef0-8176-4d42225f5415"
	tCnv   = "cnv_" + tUUID
	tRec   = "rec_" + tUUID
	tLayer = "clyr_019f8c03-6f91-7ef0-8176-4d42225f5416"
)

func TestCanonicalRef(t *testing.T) {
	tests := []struct {
		name, refType, ref, want string
	}{
		{"cnv id", "discussion", tCnv, tCnv},
		{"rec id folds onto cnv twin", "discussion", tRec, tCnv},
		{"rec id without ref type", "", tRec, tCnv},
		{"uppercase hex lowercased", "", "rec_" + strings.ToUpper(tUUID), tCnv},
		{"surrounding whitespace", "", "  " + tRec + "\n", tCnv},
		{"legacy dsc id kept distinct", "", "dsc_" + tUUID, "dsc_" + tUUID},
		{"sageox uri bare", "", "sageox://" + tCnv, tCnv},
		{"sageox uri with layer", "", "sageox://" + tCnv + "/" + tLayer, tCnv},
		{"sageox uri with pinned layer and selectors", "", "sageox://" + tCnv + "/" + tLayer + "@2#t=1700000000000--1700000005000&cue=3-5", tCnv},
		{"sageox uri selectors only", "", "sageox://" + tCnv + "#cue=4", tCnv},
		{"sageox uri with rec", "", "sageox://" + tRec, tCnv},
		{"legacy team-scoped sageox uri", "", "sageox://team_fixture01/dsc_" + tUUID + "@2", "dsc_" + tUUID},
		{"sageox uri garbage", "", "sageox://nope", "sageox://nope"},
		{"sageox uri team without id", "", "sageox://team_x/notanid", "sageox://team_x/notanid"},
		{"short link sageox.ai cnv", "", "https://sageox.ai/c/" + tCnv, tCnv},
		{"short link rec", "", "https://sageox.ai/c/" + tRec, tCnv},
		{"short link subdomain", "", "https://test.sageox.ai/c/" + tRec, tCnv},
		{"short link non-sageox host", "", "http://localhost:3000/c/" + tRec, tCnv},
		{"short link uppercase scheme", "", "HTTPS://sageox.ai/c/" + tRec, tCnv},
		{"short link trailing path query fragment", "", "https://sageox.ai/c/" + tRec + "/topics?x=1#frag", tCnv},
		{"short link trailing slash", "", "https://sageox.ai/c/" + tCnv + "/", tCnv},
		{"recording page link", "", "https://sageox.ai/team/acme/media/recordings/" + tRec + "/summary", tCnv},
		{"kb recording link", "", "https://sageox.ai/kb/eng/recordings/" + tRec, tCnv},
		{"link to dsc not a recording", "", "https://sageox.ai/c/dsc_" + tUUID, "https://sageox.ai/c/dsc_" + tUUID},
		{"link with bad id unchanged", "", "https://sageox.ai/c/rec_123", "https://sageox.ai/c/rec_123"},
		{"link to other path unchanged", "", "https://sageox.ai/docs/x/", "https://sageox.ai/docs/x"},
		{"unparseable link unchanged", "", "https://%zz/c/" + tRec, "https://%zz/c/" + tRec},
		{"hostless link unchanged", "", "https:///c/" + tRec, "https:///c/" + tRec},
		{"id embedded in prose is not an id", "", "see " + tRec, "see " + tRec},
		{"truncated uuid unchanged", "", "cnv_019f8c03", "cnv_019f8c03"},
		{"plan id untouched", "plan", "pln_01J9ABC", "pln_01J9ABC"},
		{"plan slug untouched, case kept", "plan", "Auth-Plan", "Auth-Plan"},
		{"trailing slash dropped", "doc", "docs/adr/", "docs/adr"},
		{"empty", "", "", ""},
		{"whitespace only", "", "  \t", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, CanonicalRef(tt.refType, tt.ref))
		})
	}
}

func TestFindConversationID(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"embedded rec in link", "open https://sageox.ai/c/" + tRec + " now", tCnv},
		{"embedded cnv in uri", "sageox://" + tCnv + "#cue=1", tCnv},
		{"uppercase hex", "x " + "cnv_" + strings.ToUpper(tUUID), tCnv},
		{"legacy dsc", "sageox://team_x/dsc_" + tUUID + "@2", "dsc_" + tUUID},
		{"none", "auth-plan", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, FindConversationID(tt.in))
		})
	}
}

func TestSameItem(t *testing.T) {
	tests := []struct {
		name    string
		refType string
		a, b    string
		aliases []string
		want    bool
	}{
		{"rec vs cnv", "discussion", tRec, tCnv, nil, true},
		{"link vs rec", "", "https://sageox.ai/c/" + tCnv, tRec, nil, true},
		{"uri vs link", "", "sageox://" + tCnv + "#cue=2", "https://test.sageox.ai/c/" + tRec, nil, true},
		{"case-insensitive plain refs", "doc", "memory.md", "MEMORY.md", nil, true},
		{"trailing slash", "doc", "docs/adr/", "docs/adr", nil, true},
		{"slug matches via alias", "plan", "auth-plan", "pln_X", []string{"2026-10-01-auth-plan", "Auth-Plan"}, true},
		{"dated dir matches via alias", "plan", "2026-10-01-auth-plan/", "pln_X", []string{"2026-10-01-auth-plan"}, true},
		{"no match", "plan", "other-plan", "pln_X", []string{"auth-plan"}, false},
		{"empty a never matches", "", "", "", nil, false},
		{"empty a vs empty alias", "", " ", "x", []string{""}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, SameItem(tt.refType, tt.a, tt.b, tt.aliases...))
		})
	}
}

func TestSurfacedRef_NamesAndMatches(t *testing.T) {
	s := SurfacedRef{Ref: tRec, RefType: "discussion", Aliases: []string{"https://sageox.ai/c/" + tCnv, "", "Kickoff", "kickoff/"}}
	assert.Equal(t, []string{tCnv, "Kickoff"}, s.Names())
	assert.True(t, s.Matches("sageox://"+tCnv))
	assert.True(t, s.Matches("KICKOFF"))
	assert.False(t, s.Matches("other"))

	plain := SurfacedRef{Ref: "MEMORY.md", RefType: "doc"}
	assert.Equal(t, []string{"MEMORY.md"}, plain.Names())
	assert.True(t, plain.Matches("memory.md"))
}

func TestSurfaced_Aliases(t *testing.T) {
	plan := func(ref string, seq int, aliases ...string) ct.Event {
		return ct.Event{Type: ct.EventProvided, Source: ct.SourceLocalRecall, Ref: ref, RefType: "plan", Seq: seq, Aliases: aliases}
	}
	tests := []struct {
		name   string
		events []ct.Event
		want   []SurfacedRef
	}{
		{
			name:   "aliases copied from the event",
			events: []ct.Event{plan("pln_X", 1, "auth-plan")},
			want:   []SurfacedRef{{Ref: "pln_X", RefType: "plan", Via: ViaRecall, Seq: 1, Aliases: []string{"auth-plan"}}},
		},
		{
			name: "repeated deliveries union aliases into the first",
			events: []ct.Event{
				plan("pln_X", 1, "auth-plan"),
				plan("pln_X", 4, "2026-10-01-auth-plan", "AUTH-PLAN"),
				plan("auth-plan", 7), // delivered by slug: same item
			},
			want: []SurfacedRef{{Ref: "pln_X", RefType: "plan", Via: ViaRecall, Seq: 1, Aliases: []string{"auth-plan", "2026-10-01-auth-plan"}}},
		},
		{
			name: "a later delivery under a new name adds that name",
			events: []ct.Event{
				plan("auth-plan", 1),
				plan("pln_X", 2, "auth-plan"),
			},
			want: []SurfacedRef{{Ref: "auth-plan", RefType: "plan", Via: ViaRecall, Seq: 1, Aliases: []string{"pln_X"}}},
		},
		{
			name: "conversation surfaced as rec then cnv link is one item",
			events: []ct.Event{
				{Type: ct.EventProvided, Source: ct.SourceOnDemand, Mechanism: ct.MechanismRetrieval, Ref: tRec, RefType: "discussion"},
				{Type: ct.EventProvided, Source: ct.SourceOnDemand, Mechanism: ct.MechanismRetrieval, Ref: "https://sageox.ai/c/" + tCnv, RefType: "discussion"},
			},
			want: []SurfacedRef{{Ref: tRec, RefType: "discussion", Via: ViaRetrieval}},
		},
		{
			name: "old events without aliases stay alias-free",
			events: []ct.Event{
				{Type: ct.EventProvided, Source: ct.SourceTeamMemory, Doc: "MEMORY.md"},
				{Type: ct.EventProvided, Source: ct.SourceTeamMemory, Doc: "MEMORY.md"},
			},
			want: []SurfacedRef{{Ref: "MEMORY.md", RefType: "doc", Via: ViaPrime}},
		},
		{
			name: "single-doc event carries aliases; multi-doc event does not",
			events: []ct.Event{
				{Type: ct.EventProvided, Source: ct.SourceTeamDocs, Doc: "SOUL.md", Aliases: []string{"team/SOUL.md"}},
				{Type: ct.EventProvided, Source: ct.SourceTeamDocs, Docs: []string{"A.md", "B.md"}, Aliases: []string{"ambiguous"}},
			},
			want: []SurfacedRef{
				{Ref: "SOUL.md", RefType: "doc", Via: ViaPrime, Aliases: []string{"team/SOUL.md"}},
				{Ref: "A.md", RefType: "doc", Via: ViaPrime},
				{Ref: "B.md", RefType: "doc", Via: ViaPrime},
			},
		},
		{
			name:   "invoked retrieval carries aliases",
			events: []ct.Event{{Type: ct.EventInvoked, Command: "plan show", Query: "auth-plan", Ref: "pln_X", RefType: "plan", Aliases: []string{"auth-plan"}}},
			want:   []SurfacedRef{{Ref: "pln_X", RefType: "plan", Via: ViaRetrieval, Aliases: []string{"auth-plan"}}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, Surfaced(tt.events))
		})
	}
}

func TestSurfaced_AliasesNotShared(t *testing.T) {
	ev := ct.Event{Type: ct.EventProvided, Source: ct.SourceLocalRecall, Ref: "pln_X", RefType: "plan", Aliases: []string{"auth-plan"}}
	got := Surfaced([]ct.Event{ev})
	got[0].Aliases[0] = "mutated"
	assert.Equal(t, "auth-plan", ev.Aliases[0])
}
