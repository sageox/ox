package livesession_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/livesession"
	"github.com/sageox/ox/internal/mediapool"
	oxsession "github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/sessionstream"
)

// pngBytes is a fake image body: the drain never decodes it, only hashes it.
func pngBytes(seed string) []byte {
	return append([]byte("\x89PNG\r\n\x1a\n"), []byte(strings.Repeat(seed, 64))...)
}

func digest(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// mediaLine is a media line as the capture path writes it into raw.jsonl.
func mediaLine(t *testing.T, sha, mime string, size int64, position int) []byte {
	t.Helper()
	pos := position
	b, err := json.Marshal(oxsession.Entry{
		Timestamp: time.Unix(1_700_000_000, 0).UTC(), Type: oxsession.EntryTypeMedia,
		SHA256: sha, MIME: mime, Size: size, Position: &pos,
	})
	if err != nil {
		t.Fatalf("marshal media line: %v", err)
	}
	return append(b, '\n')
}

func textLine(s string) []byte {
	return []byte(`{"type":"user","content":"` + s + `"}` + "\n")
}

// stage puts an image where the capture path would, and returns its media line.
func (e *env) stage(img []byte, mime string, position int) []byte {
	e.t.Helper()
	sha := digest(img)
	if err := mediapool.Put(e.cfg.DataDir, sha, img); err != nil {
		e.t.Fatalf("stage: %v", err)
	}
	return mediaLine(e.t, sha, mime, int64(len(img)), position)
}

func lineTypes(t *testing.T, stream []byte) []string {
	t.Helper()
	var out []string
	for _, l := range bytes.Split(bytes.TrimSuffix(stream, []byte("\n")), []byte("\n")) {
		var v struct{ Type, Reason string }
		if err := json.Unmarshal(l, &v); err != nil {
			t.Fatalf("stream line is not JSON: %v", err)
		}
		out = append(out, strings.TrimSuffix(v.Type+":"+v.Reason, ":"))
	}
	return out
}

func TestMediaUploadsThenStreamsLineAtThePastedTurn(t *testing.T) {
	e := newEnv(t)
	img := pngBytes("a")
	batch := bytes.Join([][]byte{textLine("look at this"), e.stage(img, "image/png", 1), textLine("and then?")}, nil)
	e.spool(batch)
	e.mustDrain()

	st := e.state()
	data, mime, ok := e.srv.StoredMedia(t, st.ConversationID, digest(img))
	if !ok || !bytes.Equal(data, img) || mime != "image/png" {
		t.Fatalf("image not stored intact: ok=%v mime=%q", ok, mime)
	}
	stream := e.stored(st)
	if !bytes.Equal(stream, batch) {
		t.Fatalf("stream differs from spool")
	}
	if got, want := strings.Join(lineTypes(t, stream), ","), "user,media,user"; got != want {
		t.Fatalf("line order = %s, want %s", got, want)
	}
	if bytes.Contains(stream, img) || bytes.Contains(stream, []byte("PNG")) {
		t.Fatalf("image bytes leaked into the text stream")
	}
	// declare, storage PUT and complete all precede the append that cites it
	var order []string
	for _, c := range e.srv.Calls() {
		switch {
		case c.Method == http.MethodPost && strings.HasSuffix(c.Path, "/media"):
			order = append(order, "declare")
		case c.Method == http.MethodPut && strings.HasPrefix(c.Path, "/storage/"):
			order = append(order, "put")
		case strings.HasSuffix(c.Path, "/complete"):
			order = append(order, "complete")
		case strings.HasSuffix(c.Path, "/lines"):
			order = append(order, "append")
		}
	}
	if got, want := strings.Join(order, ","), "declare,put,complete,append"; got != want {
		t.Fatalf("call order = %s, want %s", got, want)
	}
}

func TestMediaIsIdempotentBySHA256(t *testing.T) {
	e := newEnv(t)
	img := pngBytes("b")
	e.spool(withPrompt(e.stage(img, "image/png", 0)))
	e.mustDrain()
	// the same screenshot pasted again, later: declared again, never re-uploaded
	e.spool(append(textLine("again"), e.stage(img, "image/png", 0)...))
	e.mustDrain()

	if got := e.srv.MediaPuts(); got != 1 {
		t.Fatalf("storage PUTs = %d, want 1", got)
	}
	if got := e.srv.CallsTo(http.MethodPost, "/media"); got != 2 {
		t.Fatalf("declares = %d, want 2 (the second answers already_stored)", got)
	}
	if got := strings.Join(lineTypes(t, e.stored(e.state())), ","); got != "user,media,user,media" {
		t.Fatalf("stream = %s", got)
	}
}

func TestMediaRetriesAfterNetworkFailureWithoutLosingTheTurn(t *testing.T) {
	e := newEnv(t)
	img := pngBytes("c")
	e.spool(withPrompt(e.stage(img, "image/png", 0)))
	e.srv.FailMediaUploads(1)

	if _, err := e.drain(); err == nil {
		t.Fatalf("drain succeeded while the upload failed")
	}
	st := e.state()
	if bytes.Contains(e.stored(st), []byte(`"type":"media"`)) {
		t.Fatalf("media line streamed before its image uploaded")
	}
	if !bytes.Contains(e.spoolBytes(), []byte(`"type":"media"`)) {
		t.Fatalf("media line left the spool before it was sent")
	}

	e.mustDrain()
	if _, _, ok := e.srv.StoredMedia(t, e.state().ConversationID, digest(img)); !ok {
		t.Fatalf("image not stored after retry")
	}
	if got := strings.Join(lineTypes(t, e.stored(e.state())), ","); got != "user,media" {
		t.Fatalf("stream after retry = %s", got)
	}
}

func TestMediaThatCanNeverUploadBecomesDeclaredGap(t *testing.T) {
	big := int64(sessionstream.MaxMediaBytes) + 1
	tests := []struct {
		name   string
		line   func(e *env) []byte
		server func(e *env)
		reason string
	}{
		{"over the size cap", func(e *env) []byte { return mediaLine(t, digest([]byte("x")), "image/png", big, 0) }, nil, "media_too_large"},
		{"disallowed mime", func(e *env) []byte { return e.stage(pngBytes("d"), "image/svg+xml", 0) }, nil, "media_unsupported_type"},
		{"bytes no longer staged", func(e *env) []byte { return mediaLine(t, digest([]byte("gone")), "image/png", 4, 0) }, nil, "media_unavailable"},
		{"server refuses the size", func(e *env) []byte { return e.stage(pngBytes("e"), "image/png", 0) }, func(e *env) { e.srv.RefuseMediaDeclares() }, "media_too_large"},
		{"malformed digest", func(e *env) []byte { return mediaLine(t, "not-a-digest", "image/png", 4, 0) }, nil, "media_invalid"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			if tt.server != nil {
				tt.server(e)
			}
			e.spool(bytes.Join([][]byte{prompt(), tt.line(e), textLine("after")}, nil))
			e.mustDrain()

			st := e.state()
			stream := e.stored(st)
			if got, want := strings.Join(lineTypes(t, stream), ","), "user,gap:"+tt.reason+",user"; got != want {
				t.Fatalf("stream = %s, want %s", got, want)
			}
			// the spool was rewritten to what streamed: the loss is declared in both
			if !bytes.Equal(stream, e.spoolBytes()) {
				t.Fatalf("spool and stream differ after the gap")
			}
			if st.Status == "disabled" {
				t.Fatalf("a refused image stopped the stream")
			}
		})
	}
}

func TestPersistentStorage4xxEndsInGapAndTextKeepsFlowing(t *testing.T) {
	e := newEnv(t)
	img := pngBytes("f")
	e.spool(bytes.Join([][]byte{prompt(), e.stage(img, "image/png", 0), textLine("after the image")}, nil))
	e.srv.FailMediaUploadsWith(http.StatusForbidden)

	for i := 0; i < 5; i++ {
		if _, err := e.drain(); err == nil {
			t.Fatalf("drain %d succeeded while storage refused the image", i)
		}
	}
	// every retry declared again, for a fresh URL
	if got := e.srv.CallsTo(http.MethodPost, "/media"); got != 5 {
		t.Fatalf("declares = %d, want 5", got)
	}
	e.mustDrain() // the bound is reached: gap, and the text behind it goes out
	if got, want := strings.Join(lineTypes(t, e.stored(e.state())), ","), "user,gap:media_upload_failed,user"; got != want {
		t.Fatalf("stream = %s, want %s", got, want)
	}
	// text appended after the gap still flows
	e.spool(textLine("later"))
	e.mustDrain()
	if got := strings.Join(lineTypes(t, e.stored(e.state())), ","); got != "user,gap:media_upload_failed,user,user" {
		t.Fatalf("stream = %s", got)
	}
}

// A replayed layer restates images the server already holds, long after the
// local pool swept their bytes: they are completed, not turned into gaps.
func TestReplayedMediaNeedsNoLocalBytesWhenServerHasThem(t *testing.T) {
	e := newEnv(t)
	img := pngBytes("g")
	line := e.stage(img, "image/png", 0)
	e.spool(withPrompt(line))
	e.mustDrain()
	if err := os.RemoveAll(filepath.Join(e.cfg.DataDir, "live-media")); err != nil {
		t.Fatal(err)
	}
	e.spool(append(textLine("again"), line...))
	e.mustDrain()
	if got := strings.Join(lineTypes(t, e.stored(e.state())), ","); got != "user,media,user,media" {
		t.Fatalf("stream = %s", got)
	}
}

// Retry history is machine-local state like the rest of the stream's files:
// Ledger staging skips it and cache pruning keeps it with the stream.
func TestMediaRetryHistoryIsLocalFile(t *testing.T) {
	e := newEnv(t)
	e.spool(withPrompt(e.stage(pngBytes("h"), "image/png", 0)))
	e.srv.FailMediaUploadsWith(http.StatusForbidden)
	if _, err := e.drain(); err == nil {
		t.Fatalf("drain succeeded")
	}
	entries, err := os.ReadDir(e.dir)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range entries {
		if f.Name() == "media-attempts.json" {
			found = true
		}
		if strings.HasPrefix(f.Name(), "media-") && !livesession.IsLocalFile(f.Name()) {
			t.Fatalf("%s is written by the stream but not a local file", f.Name())
		}
	}
	if !found {
		t.Fatalf("failed upload left no retry history to classify")
	}
}

// Images upload unscanned (ADR-247, v2), so the person is told once per
// machine, after the first screenshot streamed.
func TestMediaNoticeIsOwedOnceAfterFirstScreenshotStreams(t *testing.T) {
	e := newEnv(t)
	if livesession.TakeMediaNotice(e.cfg.DataDir) {
		t.Fatalf("notice owed before any screenshot streamed")
	}
	e.spool(withPrompt(e.stage(pngBytes("i"), "image/png", 0)))
	e.mustDrain()
	if !livesession.TakeMediaNotice(e.cfg.DataDir) {
		t.Fatalf("notice not owed after the first screenshot streamed")
	}
	e.spool(e.stage(pngBytes("j"), "image/png", 0))
	e.mustDrain()
	if livesession.TakeMediaNotice(e.cfg.DataDir) {
		t.Fatalf("notice owed a second time")
	}
}

// The storage answer's real status is what the fake records, so a test can see
// the injected failure.
func TestFakeStorageRecordsTheStatusItSent(t *testing.T) {
	e := newEnv(t)
	e.spool(withPrompt(e.stage(pngBytes("k"), "image/png", 0)))
	e.srv.FailMediaUploadsWith(http.StatusForbidden)
	_, _ = e.drain()
	for _, c := range e.srv.Calls() {
		if c.Method == http.MethodPut && strings.HasPrefix(c.Path, "/storage/") {
			if c.Status != http.StatusForbidden {
				t.Fatalf("recorded status = %d, want 403", c.Status)
			}
			return
		}
	}
	t.Fatalf("no storage PUT recorded")
}

// A 3xx from storage is never followed: the image and its signed headers go
// only where the declare answer named.
func TestStorageRedirectIsNotFollowed(t *testing.T) {
	var hits int
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++ }))
	t.Cleanup(elsewhere.Close)

	e := newEnv(t)
	e.spool(withPrompt(e.stage(pngBytes("l"), "image/png", 0)))
	e.srv.RedirectMediaUploads(elsewhere.URL + "/steal")
	if _, err := e.drain(); err == nil {
		t.Fatalf("drain succeeded through a redirect")
	}
	if hits != 0 {
		t.Fatalf("redirect target received %d requests", hits)
	}
}

// One 4xx after a long quiet stretch (an outage, a closed laptop) is not ten
// minutes of failures: the image still gets its remaining attempts.
func TestIdleTimeBetweenFailuresDoesNotEndTheRetries(t *testing.T) {
	e := newEnv(t)
	img := pngBytes("m")
	e.spool(withPrompt(e.stage(img, "image/png", 0)))
	old := time.Now().Add(-6 * time.Hour)
	history := map[string]map[string]any{digest(img): {"failures": 1, "active": 0, "last": old}}
	b, _ := json.Marshal(history)
	if err := os.WriteFile(filepath.Join(e.dir, "media-attempts.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	e.srv.FailMediaUploadsWith(http.StatusForbidden)
	if _, err := e.drain(); err == nil {
		t.Fatalf("drain succeeded")
	}
	e.srv.FailMediaUploadsWith(0)
	e.mustDrain()
	if got := strings.Join(lineTypes(t, e.stored(e.state())), ","); got != "user,media" {
		t.Fatalf("stream = %s: idle hours turned a lone 4xx into a gap", got)
	}
}
