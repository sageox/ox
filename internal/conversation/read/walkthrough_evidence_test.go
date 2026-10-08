package read

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func stageEvidence(t *testing.T, cueCount int) (string, string, string) {
	t.Helper()
	root := copyTree(t, walkthroughRoot)
	dir := "layers/keyframe.clyr_01a0f490-0000-7000-8000-000000000001"
	require.NoError(t, os.MkdirAll(filepath.Join(root, walkthroughFolder, dir), 0755))
	var transcript strings.Builder
	transcript.WriteString("WEBVTT\n\n")
	for i := 0; i < cueCount; i++ {
		fmt.Fprintf(&transcript, "%s --> %s\nsource cue %d\n\n", formatVTTTimestamp(time.Duration(i)*time.Second), formatVTTTimestamp(time.Duration(i+1)*time.Second), i+1)
	}
	var jpg bytes.Buffer
	require.NoError(t, jpeg.Encode(&jpg, image.NewRGBA(image.Rect(0, 0, 3, 2)), nil))
	hash := digest(jpg.Bytes())
	frames := []evidenceFrame{{ID: "frame-" + hash + "-0.000000", SHA256: hash, Width: 3, Height: 2, Path: "frame-" + hash + ".jpg", TimestampSeconds: 0, Reason: "source-cue"}}
	fb, err := json.Marshal(frames)
	require.NoError(t, err)
	obs := []byte(`{"layers":[],"hints":[]}`)
	index := evidenceIndex{Spec: "keyframe/2", SourceRevision: digest([]byte("video")), TranscriptRevision: digest([]byte(transcript.String())), Capabilities: EvidenceCapabilities{Video: true}, FramesFile: "frames.json", TranscriptFile: "transcript.vtt"}
	index.Revision = digest([]byte(index.SourceRevision + "\n" + index.TranscriptRevision + "\n" + digest(fb) + "\n" + digest(obs) + "\nwalkthrough-evidence-v2"))
	ib, err := json.Marshal(index)
	require.NoError(t, err)
	writeWalkthroughFile(t, root, dir+"/index.json", string(ib))
	writeWalkthroughFile(t, root, dir+"/frames.json", string(fb))
	writeWalkthroughFile(t, root, dir+"/observations.json", string(obs))
	writeWalkthroughFile(t, root, dir+"/transcript.vtt", transcript.String())
	writeWalkthroughFile(t, root, dir+"/"+frames[0].Path, jpg.String())
	writeWalkthroughFile(t, root, dir+"/layer.json", fmt.Sprintf(`{"$schema_version":1,"layer_id":"clyr_01a0f490-0000-7000-8000-000000000001","conversation_id":%q,"kind":"keyframe","modality":"image","spec":"sageox://layer-spec/keyframe/2","revision":1,"status":"active"}`, walkthroughCnv))
	return root, index.Revision, dir
}

// Missing frame coverage must not hide requests or redirect a pin to newer text.
func TestEvidencePinnedTranscriptAndEmptyImageWindow(t *testing.T) {
	root, rev, _ := stageEvidence(t, 105)
	writeWalkthroughFile(t, root, "transcript.vtt", "WEBVTT\n\n00:00.000 --> 00:01.000\nwrong current words\n")
	r := New(root, time.Time{})
	d := walkthroughData(t, r.Walkthrough(walkthroughCnv, WalkthroughOptions{Revision: rev, Transcript: true}))
	require.Equal(t, 105, d.Transcript.Total)
	require.Len(t, d.Transcript.Cues, 100)
	require.NotEmpty(t, d.Transcript.NextCursor)
	next := walkthroughData(t, r.Walkthrough(walkthroughCnv, WalkthroughOptions{Revision: rev, Transcript: true, Cursor: d.Transcript.NextCursor}))
	require.Len(t, next.Transcript.Cues, 5)
	require.Equal(t, 105, next.Transcript.Cues[4].N)
	require.Empty(t, next.Transcript.NextCursor)
	empty := walkthroughData(t, r.Walkthrough(walkthroughCnv, WalkthroughOptions{Revision: rev, CueFirst: 47, CueLast: 47}))
	require.Empty(t, empty.Moments)
	require.Equal(t, "source cue 47", empty.Transcript.Cues[0].Text)
	env := r.Walkthrough(walkthroughCnv, WalkthroughOptions{Revision: digest([]byte("absent"))})
	require.False(t, env.Success)
	require.Equal(t, "revision_unavailable", env.Error.Code)
	env = r.Walkthrough(walkthroughCnv, WalkthroughOptions{Revision: rev, Cursor: d.Transcript.NextCursor, CueFirst: 47, CueLast: 47})
	require.False(t, env.Success)
	require.Equal(t, ErrCodeInvalidSelector, env.Error.Code)
}

// Hash checks protect against a mixed checkout and same-length poisoned cache.
func TestEvidenceRejectsChangedPayloadsAndDoesNotCallStubsImages(t *testing.T) {
	for _, file := range []string{"transcript.vtt", "frames.json", "observations.json"} {
		t.Run(file, func(t *testing.T) {
			root, rev, dir := stageEvidence(t, 2)
			writeWalkthroughFile(t, root, dir+"/"+file, `{}`)
			env := New(root, time.Time{}).Walkthrough(walkthroughCnv, WalkthroughOptions{Revision: rev})
			require.False(t, env.Success)
		})
	}
	root, rev, dir := stageEvidence(t, 2)
	d := walkthroughData(t, New(root, time.Time{}).Walkthrough(walkthroughCnv, WalkthroughOptions{Revision: rev}))
	f := d.Moments[0].Frame
	require.NotEmpty(t, f.LocalImage)
	b, err := os.ReadFile(f.LocalImage)
	require.NoError(t, err)
	b[len(b)-1] ^= 1
	require.NoError(t, os.WriteFile(f.LocalImage, b, 0644))
	d = walkthroughData(t, New(root, time.Time{}).Walkthrough(walkthroughCnv, WalkthroughOptions{Revision: rev}))
	require.Empty(t, d.Moments[0].Frame.LocalImage)
	outside := filepath.Join(t.TempDir(), "secret.jpg")
	require.NoError(t, os.WriteFile(outside, b, 0644))
	imagePath := filepath.Join(root, walkthroughFolder, dir, "frame-"+f.SHA256+".jpg")
	require.NoError(t, os.Remove(imagePath))
	require.NoError(t, os.Symlink(outside, imagePath))
	d = walkthroughData(t, New(root, time.Time{}).Walkthrough(walkthroughCnv, WalkthroughOptions{Revision: rev}))
	require.Empty(t, d.Moments[0].Frame.LocalImage)
	require.Empty(t, d.Moments[0].Frame.FetchCommand)
}

func TestLegacyWalkthroughPagesWordsWithoutFrames(t *testing.T) {
	root := copyTree(t, walkthroughRoot)
	require.NoError(t, os.Remove(filepath.Join(root, walkthroughFolder, "keyframes.json")))
	env := New(root, time.Time{}).Walkthrough(walkthroughCnv, WalkthroughOptions{CueFirst: 2, CueLast: 2})
	d := walkthroughData(t, env)
	require.Len(t, d.Transcript.Cues, 1)
	require.Equal(t, 2, d.Transcript.Cues[0].N)
}

// A new published revision must not change old source-backed citations.
func TestEvidenceOldPinSurvivesNewPublication(t *testing.T) {
	root, oldRevision, oldDir := stageEvidence(t, 2)
	newDir := strings.Replace(oldDir, "000000000001", "000000000002", 1)
	require.NoError(t, os.MkdirAll(filepath.Join(root, walkthroughFolder, newDir), 0755))
	entries, err := os.ReadDir(filepath.Join(root, walkthroughFolder, oldDir))
	require.NoError(t, err)
	payload := map[string][]byte{}
	for _, entry := range entries {
		b, e := os.ReadFile(filepath.Join(root, walkthroughFolder, oldDir, entry.Name()))
		require.NoError(t, e)
		payload[entry.Name()] = b
	}
	payload["transcript.vtt"] = bytes.ReplaceAll(payload["transcript.vtt"], []byte("source cue"), []byte("new words"))
	payload["layer.json"] = bytes.ReplaceAll(payload["layer.json"], []byte("000000000001"), []byte("000000000002"))
	var index evidenceIndex
	require.NoError(t, json.Unmarshal(payload["index.json"], &index))
	index.TranscriptRevision = digest(payload["transcript.vtt"])
	index.Revision = digest([]byte(index.SourceRevision + "\n" + index.TranscriptRevision + "\n" + digest(payload["frames.json"]) + "\n" + digest(payload["observations.json"]) + "\nwalkthrough-evidence-v2"))
	payload["index.json"], err = json.Marshal(index)
	require.NoError(t, err)
	for name, b := range payload {
		writeWalkthroughFile(t, root, newDir+"/"+name, string(b))
	}
	r := New(root, time.Time{})
	latest := walkthroughData(t, r.Walkthrough(walkthroughCnv, WalkthroughOptions{Transcript: true}))
	require.Equal(t, index.Revision, latest.Revision)
	require.Equal(t, "new words 1", latest.Transcript.Cues[0].Text)
	old := walkthroughData(t, r.Walkthrough(walkthroughCnv, WalkthroughOptions{Revision: oldRevision, Transcript: true}))
	require.Equal(t, oldRevision, old.Revision)
	require.Equal(t, "source cue 1", old.Transcript.Cues[0].Text)
}

// The very first suggested read must retain the pin, not send a consumer back
// to today's mutable transcript before later guidance mentions immutable reads.
func TestEvidenceGuidanceKeepsSourceRevision(t *testing.T) {
	root, revision, _ := stageEvidence(t, 2)
	env := New(root, time.Time{}).Walkthrough(walkthroughCnv, WalkthroughOptions{Revision: revision})
	require.True(t, env.Success)
	require.Contains(t, env.Guidance, "--transcript --revision "+revision)
	require.NotContains(t, env.Guidance, "ox conversation transcript")
}
