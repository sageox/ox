//go:build slow

// conversation_e2e_harness_test.go — hermetic E2E harness for the
// ox conversation read family (list/show/transcript/topics/topic).
//
// Reuses the distill-history harness primitives (setupDistillHistoryE2E,
// testguard.BuildOxBinary/RunOx, full HOME/XDG reroute under t.TempDir(),
// staged team contexts via .sageox/config.json + config.local.toml
// [[team_contexts]]) and adds: the conversation envelope decoder, typed
// payload mirrors, and the discussions/ fixture stager that reproduces every
// observed on-disk state the plan's step-6 scenarios need.
//
// Scenario tests live in conversation_e2e_list_show_test.go,
// conversation_e2e_transcript_test.go, and conversation_e2e_topics_test.go.

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Envelope decoding
// ---------------------------------------------------------------------------

// conversationE2EError mirrors read.Error on the wire.
type conversationE2EError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

// conversationE2EEnvelope mirrors read.Envelope on the wire. Data stays raw
// so each scenario decodes it into the payload mirror it cares about.
type conversationE2EEnvelope struct {
	Success       bool                  `json:"success"`
	Data          json.RawMessage       `json:"data"`
	Error         *conversationE2EError `json:"error"`
	Guidance      string                `json:"guidance"`
	Warnings      []string              `json:"warnings"`
	TokenEstimate int                   `json:"token_estimate"`
	LastSync      string                `json:"last_sync"`
	ElapsedMS     int64                 `json:"elapsed_ms"`
}

// decodeConversationEnvelope finds and parses the JSON envelope line in a
// combined stdout+stderr blob. Mirrors decodeJournalEnvelope: RunOx merges
// the two fds with no ordering guarantee, so the envelope is located by
// scanning, and every other line is returned as the tail for diagnostics.
func decodeConversationEnvelope(t *testing.T, out string) (*conversationE2EEnvelope, string) {
	t.Helper()
	lines := strings.Split(out, "\n")
	envIdx := -1
	var env conversationE2EEnvelope
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || !strings.HasPrefix(trimmed, "{") {
			continue
		}
		if err := json.Unmarshal([]byte(trimmed), &env); err == nil {
			envIdx = i
			break
		}
	}
	if envIdx < 0 {
		t.Fatalf("no JSON envelope found in output:\n%s", out)
		return nil, ""
	}
	tail := make([]string, 0, len(lines)-1)
	for i, line := range lines {
		if i != envIdx {
			tail = append(tail, line)
		}
	}
	return &env, strings.Join(tail, "\n")
}

// decodeConversationData unmarshals an envelope's data payload into dst,
// failing the test with the raw payload on any mismatch.
func decodeConversationData(t *testing.T, env *conversationE2EEnvelope, dst any) {
	t.Helper()
	require.NotNil(t, env.Data, "envelope has no data payload")
	require.NoError(t, json.Unmarshal(env.Data, dst), "decode data payload: %s", string(env.Data))
}

// ---------------------------------------------------------------------------
// Payload mirrors (wire shapes of internal/conversation/read data structs)
// ---------------------------------------------------------------------------

type convE2EListRow struct {
	ConversationID  string   `json:"conversation_id"`
	RecordingID     string   `json:"recording_id"`
	Title           string   `json:"title"`
	RecordedAt      string   `json:"recorded_at"`
	Participants    []string `json:"participants"`
	DecisionCount   int      `json:"decision_count"`
	ActionItemCount int      `json:"action_item_count"`
	Topics          []string `json:"topics"`
	HasDistillation bool     `json:"has_distillation"`
}

type convE2EListData struct {
	Conversations []convE2EListRow `json:"conversations"`
	TotalIndexed  int              `json:"total_indexed"`
	Truncated     bool             `json:"truncated"`
}

type convE2EShowData struct {
	ConversationID string   `json:"conversation_id"`
	RecordingID    string   `json:"recording_id"`
	Title          string   `json:"title"`
	RecordedAt     string   `json:"recorded_at"`
	Participants   []string `json:"participants"`
	Summary        struct {
		Available    bool   `json:"available"`
		HumanSummary string `json:"human_summary"`
		Reason       string `json:"reason"`
	} `json:"summary"`
}

type convE2ECue struct {
	N       int    `json:"n"`
	Start   string `json:"start"`
	End     string `json:"end"`
	Speaker string `json:"speaker"`
	Text    string `json:"text"`
}

type convE2ETranscriptData struct {
	RevisionRequested int          `json:"revision_requested"`
	RevisionCurrent   int          `json:"revision_current"`
	Pinning           string       `json:"pinning"`
	Cues              []convE2ECue `json:"cues"`
	Window            struct {
		Cues      []int `json:"cues"`
		Truncated bool  `json:"truncated"`
		Clamped   bool  `json:"clamped"`
	} `json:"window"`
}

type convE2ETopicsData struct {
	Episode struct {
		Status        string `json:"status"`
		ExtractedAt   string `json:"extracted_at"`
		TTLExpiresAt  string `json:"ttl_expires_at"`
		SkippedReason string `json:"skipped_reason"`
	} `json:"episode"`
	Topics []struct {
		ID        string   `json:"id"`
		Title     string   `json:"title"`
		Summary   string   `json:"summary"`
		AtomCount int      `json:"atom_count"`
		CueURIs   []string `json:"cue_uris"`
	} `json:"topics"`
	AtomsTotal      int `json:"atoms_total"`
	AtomsSuperseded int `json:"atoms_superseded"`
}

type convE2EAtom struct {
	ID     string  `json:"id"`
	Kind   string  `json:"kind"`
	Signal string  `json:"signal"`
	Text   string  `json:"text"`
	Conf   float64 `json:"confidence"`
	Quote  *struct {
		CueRef int    `json:"cue_ref"`
		Text   string `json:"text"`
	} `json:"quote"`
	Source *struct {
		URIs    []string `json:"uris"`
		Speaker string   `json:"speaker"`
	} `json:"source"`
	ValidFrom    string `json:"valid_from"`
	ValidTo      string `json:"valid_to"`
	SupersededBy string `json:"superseded_by"`
}

type convE2ETopicData struct {
	Topic struct {
		ID      string `json:"id"`
		Title   string `json:"title"`
		Summary string `json:"summary"`
	} `json:"topic"`
	Atoms           []convE2EAtom `json:"atoms"`
	AtomsTotal      int           `json:"atoms_total"`
	AtomsSuperseded int           `json:"atoms_superseded"`
}

// ---------------------------------------------------------------------------
// Fixture identities
// ---------------------------------------------------------------------------

// Recording / conversation ids staged by stageConversationDiscussions. The
// cnv_/rec_ prefixes share one UUID by literal swap, so both spellings are
// derivable from these.
const (
	convE2EFullUUID = "019ff2f5-2079-7be1-b05e-8caad2772e61"
	convE2EFullRec  = "rec_" + convE2EFullUUID
	convE2EFullCnv  = "cnv_" + convE2EFullUUID

	convE2ELegacyUUID = "019ff370-e195-7d1c-a727-39a1a85823f2"
	convE2ELegacyRec  = "rec_" + convE2ELegacyUUID
	convE2ELegacyCnv  = "cnv_" + convE2ELegacyUUID

	convE2EBothUUID = "019ffc00-0000-7000-8000-000000000004"
	convE2EBothRec  = "rec_" + convE2EBothUUID

	convE2ESkippedUUID = "019ffd00-0000-7000-8000-000000000005"
	convE2ESkippedRec  = "rec_" + convE2ESkippedUUID
	convE2ESkippedCnv  = "cnv_" + convE2ESkippedUUID

	convE2ENoVTTUUID = "019ffe00-0000-7000-8000-000000000006"
	convE2ENoVTTRec  = "rec_" + convE2ENoVTTUUID
	convE2ENoVTTCnv  = "cnv_" + convE2ENoVTTUUID

	convE2ELongUUID = "019fff00-0000-7000-8000-000000000007"
	convE2ELongRec  = "rec_" + convE2ELongUUID
	convE2ELongCnv  = "cnv_" + convE2ELongUUID

	// convE2EUnindexedRec is a strictly valid rec_ UUIDv7 that no INDEX.json
	// entry carries — the index-miss scenario (D3).
	convE2EUnindexedRec = "rec_019faaaa-0000-7000-8000-0000000000ad"

	// convE2ETranscriptLayer is the full folder's transcript layer id; its
	// manifest pins revision 2.
	convE2ETranscriptLayer = "clyr_019ff2f5-deb5-77d3-b84b-04db14c601ca"

	// Distillation ids inside the full folder.
	convE2ETopicHiring     = "tp_01a012cb-9764-7555-a3f3-ce3377e47d98"
	convE2ETopicIndexTrust = "tp_01a012cb-9764-7555-a3f3-ce3377e47d99"
	convE2EAtomHire        = "at_01a012cb-9763-73bd-88a1-f299816da945"
	convE2EAtomLegacyURI   = "at_01a012cb-9763-73bd-88a1-f299816da946"
	convE2EAtomSuperseded  = "at_01a012cb-9763-73bd-88a1-f299816da947"
)

// convE2ECitation builds a full-folder citation URI with the given revision
// suffix ("@2") and fragment ("#cue=5-6"); either part may be empty.
func convE2ECitation(rev, fragment string) string {
	return "sageox://" + convE2EFullCnv + "/" + convE2ETranscriptLayer + rev + fragment
}

// ---------------------------------------------------------------------------
// Fixture stager
// ---------------------------------------------------------------------------

// convE2EFullTranscript is the six-cue transcript of the full folder.
// Media-clock cue intervals (seconds): 1:[0,4) 2:[4,9) 3:[9,15.5) 4:[15.5,21)
// 5:[21,27.25) 6:[27.25,33).
const convE2EFullTranscript = `WEBVTT

00:00:00.000 --> 00:00:04.000
<v usr_a1b2c3d4e5f6a7b8c9d0e1f2>Hello team, thanks for joining the planning discussion this morning.

00:00:04.000 --> 00:00:09.000
<v usr_a1b2c3d4e5f6a7b8c9d0e1f2>Today we need to decide whether the reader trusts the index file completely.

00:00:09.000 --> 00:00:15.500
<v usr_9f8e7d6c5b4a39281706f5e4>I think we should trust it and treat every gap as a server problem to repair.

00:00:15.500 --> 00:00:21.000
<v usr_a1b2c3d4e5f6a7b8c9d0e1f2>Agreed, so the command line never walks the folder tree looking for strays.

00:00:21.000 --> 00:00:27.250
<v usr_9f8e7d6c5b4a39281706f5e4>Then we should hire a forward deployed engineer to help teams adopt the workflow.

00:00:27.250 --> 00:00:33.000
<v usr_a1b2c3d4e5f6a7b8c9d0e1f2>Good, let us write that decision down and move on to the citation format.
`

// convE2ELongCueCount sizes the long-transcript folder past the 100-cue
// default window so truncation and --full are observable.
const convE2ELongCueCount = 120

// writeConversationFixtureFile writes one fixture file, creating parents.
func writeConversationFixtureFile(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755), "mkdir %s", filepath.Dir(path))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644), "write %s", path)
}

// stageConversationDiscussions stages the discussions/ tree under the
// harness's primary team-context root, reproducing every observed on-disk
// state the step-6 scenarios exercise:
//
//   - full: transcript + summary.json + metadata + layers.json manifest +
//     folder-form transcript layer (revision 2) + draft distillation with a
//     superseded atom tombstone. Empty INDEX title (fallback via metadata →
//     folder name is separately covered by no-transcript).
//   - legacy: transcript + summary.md only (pre-JSON era; no manifests).
//   - both-manifests: layers.json AND conversation.json coexisting (D6).
//   - skipped: distillation episode with status=skipped + skipped_reason.
//   - no-transcript: metadata.json only — no transcript, no summary.
//   - long: generated transcript past the 100-cue default window.
//   - a stray `<folder>transcript.vtt` sibling FILE at the discussions root
//     (pre-2026-01-29 writer bug — must never be globbed).
//   - INDEX.json carrying all six live folders plus a phantom entry (folder
//     deleted), a hostile `../escape` folder name, a non-object entry, and
//     an entry with neither folder nor recording_id.
//
// Returns the discussions root path.
func stageConversationDiscussions(t *testing.T, teamContextRoot string) string {
	t.Helper()
	disc := filepath.Join(teamContextRoot, "discussions")

	// --- full folder -------------------------------------------------------
	full := filepath.Join(disc, "2026-08-11-22-32-full")
	writeConversationFixtureFile(t, filepath.Join(full, "transcript.vtt"), convE2EFullTranscript)
	writeConversationFixtureFile(t, filepath.Join(full, "metadata.json"), `{
  "recording_id": "`+convE2EFullRec+`",
  "title": "",
  "created_at": "2026-08-11T22:32:58Z",
  "user_id": "usr_fixture",
  "context_type": "team",
  "context_id": "team_conv_e2e"
}
`)
	writeConversationFixtureFile(t, filepath.Join(full, "summary.json"), `{
  "schema_version": 2,
  "recording_id": "`+convE2EFullRec+`",
  "title": "Full Fixture Discussion",
  "human_summary": "The team agreed to trust the index file as the only lookup source and to hire a forward deployed engineer.",
  "participants": [
    {"name": "Galex Yen", "role": "Speaker"},
    {"name": "Ryan", "role": "Speaker"},
    {"name": "", "role": "Ghost"}
  ],
  "chapters": [{"id": "ch-1", "title": "ignored by show"}]
}
`)
	writeConversationFixtureFile(t, filepath.Join(full, "layers.json"), `{
  "$schema_version": 1,
  "id": "`+convE2EFullCnv+`",
  "type": "recording",
  "context_type": "team",
  "context_id": "team_conv_e2e",
  "title": "",
  "started_at": "2026-08-11T22:32:58Z",
  "clock": {"t0": "2026-08-11T22:32:58Z", "clock_class": "recording-local", "pauses": []}
}
`)
	writeConversationFixtureFile(t,
		filepath.Join(full, "layers", "transcript."+convE2ETranscriptLayer, "layer.json"), `{
  "$schema_version": 1,
  "layer_id": "`+convE2ETranscriptLayer+`",
  "conversation_id": "`+convE2EFullCnv+`",
  "kind": "transcript",
  "modality": "text",
  "mime": "text/vtt",
  "label": "Transcript",
  "origin": "derived",
  "spec": "sageox://layer-spec/transcript/1",
  "revision": 2,
  "status": "active",
  "supersedes": null,
  "lineage": [],
  "clock": {"t0": ""},
  "content": {"kind": "transcript", "refs": [{"ref": "git-file", "path": "transcript.vtt", "mime": "text/vtt"}]}
}
`)
	writeConversationFixtureFile(t, filepath.Join(full, "distillation", "distillation.jsonl"),
		`{"type": "episode", "schema_version": 2, "id": "ep_01a012cb-9763-72fc-930a-b74ca843c611", "status": "draft", "recording_uri": "sageox://`+convE2EFullCnv+`", "ttl_expires_at": "2026-08-18T03:55:27Z", "provenance": {"extracted_at": "2026-08-18T02:55:27Z", "extracted_by_run_id": "run1", "llm_model": "m", "prompt_version": "engine-v2", "backstop_version": 1}}
{"type": "topic", "id": "`+convE2ETopicHiring+`", "title": "Hiring", "summary": "The hiring decision.", "cue_uris": ["`+convE2ECitation("@2", "#cue=5-6")+`"]}
{"type": "topic", "id": "`+convE2ETopicIndexTrust+`", "title": "Index Trust", "summary": "Trusting the index file.", "cue_uris": ["`+convE2ECitation("@2", "#cue=2-4")+`"]}
{"type": "atom", "id": "`+convE2EAtomHire+`", "kind": "decision", "signal": "high", "text": "Hire a forward deployed engineer.", "topic_id": "`+convE2ETopicHiring+`", "source": {"uris": ["`+convE2ECitation("@2", "#cue=5")+`"], "speaker": "usr_9f8e7d6c5b4a39281706f5e4"}, "quote": {"cue_ref": 5, "text": "Then we should hire a forward deployed engineer."}, "confidence": 0.95, "valid_from": "2026-08-18T02:55:27Z", "valid_to": null}
{"type": "atom", "id": "`+convE2EAtomLegacyURI+`", "kind": "learning", "signal": "medium", "text": "Legacy citation form still appears in old atoms.", "topic_id": "`+convE2ETopicIndexTrust+`", "source": {"uri": "sageox://team_fixture01/dsc_019f8c03-6f90-7ef0-8176-4d42225f5415@2", "speaker": "usr_a1b2c3d4e5f6a7b8c9d0e1f2"}, "confidence": 0.8, "valid_from": "2026-08-18T02:55:27Z", "valid_to": null}
{"type": "atom", "id": "`+convE2EAtomSuperseded+`", "kind": "action_item", "signal": "low", "text": "Old wording superseded later.", "topic_id": "`+convE2ETopicHiring+`", "source": {"uris": []}, "confidence": 0.5, "valid_from": "2026-08-18T02:55:27Z", "valid_to": "2026-08-18T02:56:00Z", "superseded_by": "`+convE2EAtomHire+`"}
{"type":"mystery","id":"zz_1"}
`)

	// --- legacy folder (summary.md era, no manifests) ----------------------
	legacy := filepath.Join(disc, "2026-08-12-01-00-legacy")
	writeConversationFixtureFile(t, filepath.Join(legacy, "transcript.vtt"), `WEBVTT

00:00:00.000 --> 00:00:03.000
<v Speaker 1>Welcome to the legacy era recording.

00:00:03.000 --> 00:00:07.000
<v Speaker 2>Nothing here carries a layer manifest at all.
`)
	writeConversationFixtureFile(t, filepath.Join(legacy, "summary.md"), "# Legacy Summary\n\nHand-written era summary, predating summary.json.\n")

	// --- both-manifests folder (D6 anomaly) --------------------------------
	both := filepath.Join(disc, "2026-08-14-01-00-both-manifests")
	writeConversationFixtureFile(t, filepath.Join(both, "layers.json"), `{
  "$schema_version": 1,
  "id": "cnv_`+convE2EBothUUID+`",
  "title": "From layers.json",
  "started_at": "2026-08-14T01:00:00Z",
  "clock": {"t0": "2026-08-14T01:00:00Z", "clock_class": "recording-local", "pauses": []}
}
`)
	writeConversationFixtureFile(t, filepath.Join(both, "conversation.json"), `{
  "$schema_version": 1,
  "id": "cnv_`+convE2EBothUUID+`",
  "title": "From conversation.json",
  "clock": {"t0": "", "clock_class": "", "pauses": null}
}
`)
	writeConversationFixtureFile(t, filepath.Join(both, "transcript.vtt"), `WEBVTT

00:00:00.000 --> 00:00:05.000
<v usr_b1b2c3d4e5f6a7b8c9d0e1f2>Two manifest names live in this folder at once.
`)

	// --- skipped-distillation folder ---------------------------------------
	skipped := filepath.Join(disc, "2026-08-16-01-00-skipped")
	writeConversationFixtureFile(t, filepath.Join(skipped, "transcript.vtt"), `WEBVTT

00:00:00.000 --> 00:00:04.000
<v usr_c1b2c3d4e5f6a7b8c9d0e1f2>This one was skipped by the distiller.
`)
	writeConversationFixtureFile(t, filepath.Join(skipped, "distillation", "distillation.jsonl"),
		`{"type": "episode", "schema_version": 2, "id": "ep_019ff600-0000-7000-8000-000000000e02", "status": "skipped", "recording_uri": "sageox://`+convE2ESkippedCnv+`", "provenance": {"extracted_at": "2026-08-16T01:10:00Z", "extracted_by_run_id": "run3", "llm_model": "m", "prompt_version": "engine-v2", "backstop_version": 1}, "skipped_reason": "cluster_exhausted_v2"}
`)

	// --- no-transcript folder (staged server writes, D13) ------------------
	writeConversationFixtureFile(t, filepath.Join(disc, "2026-08-17-01-00-no-transcript", "metadata.json"), `{
  "recording_id": "`+convE2ENoVTTRec+`",
  "title": "Metadata Title Fallback",
  "created_at": "2026-08-17T01:00:00Z",
  "user_id": "usr_fixture",
  "context_type": "team",
  "context_id": "team_conv_e2e"
}
`)

	// --- long-transcript folder (past the 100-cue default window) ----------
	var vtt strings.Builder
	vtt.WriteString("WEBVTT\n")
	for i := 1; i <= convE2ELongCueCount; i++ {
		fmt.Fprintf(&vtt, "\n00:%02d:%02d.000 --> 00:%02d:%02d.000\n<v usr_a1b2c3d4e5f6a7b8c9d0e1f2>Cue number %d of the long recording.\n",
			(i-1)/60, (i-1)%60, i/60, i%60, i)
	}
	writeConversationFixtureFile(t, filepath.Join(disc, "2026-08-18-01-00-long", "transcript.vtt"), vtt.String())

	// --- stray sibling FILE at the discussions root ------------------------
	writeConversationFixtureFile(t, filepath.Join(disc, "2026-08-11-22-32-fulltranscript.vtt"), `WEBVTT

00:00:00.000 --> 00:00:02.000
Stray sibling file from the pre-2026-01-29 writer bug.
`)

	// --- INDEX.json --------------------------------------------------------
	writeConversationFixtureFile(t, filepath.Join(disc, "INDEX.json"), `[
  {"folder": "2026-08-11-22-32-full", "recording_id": "`+convE2EFullRec+`", "title": "", "participants": ["Galex Yen"], "decision_count": 1, "action_item_count": 0, "has_keyframes": false},
  {"folder": "2026-08-12-01-00-legacy", "recording_id": "`+convE2ELegacyRec+`", "title": "Legacy Era Discussion", "participants": ["Casey"], "decision_count": 0, "action_item_count": 0, "has_keyframes": false, "topics": ["legacy"]},
  {"folder": "2026-08-14-01-00-both-manifests", "recording_id": "`+convE2EBothRec+`", "title": "Both Manifests", "participants": [], "decision_count": 0, "action_item_count": 0, "has_keyframes": false},
  {"folder": "2026-08-16-01-00-skipped", "recording_id": "`+convE2ESkippedRec+`", "title": "Skipped Episode", "participants": [], "decision_count": 0, "action_item_count": 0, "has_keyframes": false},
  {"folder": "2026-08-17-01-00-no-transcript", "recording_id": "`+convE2ENoVTTRec+`", "title": "", "participants": [], "decision_count": 0, "action_item_count": 0, "has_keyframes": false},
  {"folder": "2026-08-18-01-00-long", "recording_id": "`+convE2ELongRec+`", "title": "Long Recording", "participants": [], "decision_count": 0, "action_item_count": 0, "has_keyframes": false},
  {"folder": "2026-01-01-00-00-phantom", "recording_id": "rec_019f0000-0000-7000-8000-000000000009", "title": "Deleted Folder", "participants": [], "decision_count": 0, "action_item_count": 0, "has_keyframes": false},
  {"folder": "../escape", "recording_id": "rec_019f1111-0000-7000-8000-000000000010", "title": "Hostile Folder Name", "decision_count": 0, "action_item_count": 0, "has_keyframes": false},
  "not an object",
  {"title": "no folder or recording id"}
]
`)

	return disc
}

// convE2ETotalIndexed is the number of parseable INDEX.json entries staged
// above (six live folders + phantom + hostile; the non-object line and the
// id-less entry are invalid records, not entries).
const convE2ETotalIndexed = 8

// convE2ELiveFolders is the number of rows that survive the guard +
// existence pass (phantom and hostile dropped).
const convE2ELiveFolders = 6

// nowConversationE2E is the single reference instant a conversation E2E
// captures at entry (mirrors the distill-history harness clock discipline).
func nowConversationE2E() time.Time { return time.Now().UTC() }

// setupConversationE2E builds the shared hermetic harness (fresh binary,
// workspace, XDG reroute, fake auth) and stages the conversation fixture
// tree in the primary team context. Returns the harness; the discussions
// root lives at <primaryTeam.path>/discussions.
func setupConversationE2E(t *testing.T) *distillHistoryE2E {
	t.Helper()
	e2e := setupDistillHistoryE2E(t, nowConversationE2E())
	stageConversationDiscussions(t, e2e.primaryTeam.path)
	return e2e
}

// removeConversationAuth deletes the fake auth.json so the harness models a
// fully logged-out machine (D14: local reads never touch auth).
func removeConversationAuth(t *testing.T, e2e *distillHistoryE2E) {
	t.Helper()
	require.NoError(t, os.Remove(filepath.Join(e2e.configHome, "sageox", "auth.json")))
}
