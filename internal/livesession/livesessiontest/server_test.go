package livesessiontest

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sageox/ox/internal/sessionstream"
)

// These tests pin the fake's own contract: it is the shared double behind the
// livesession, cmd and daemon suites, and package coverage counts only tests
// that live beside it.

const (
	testCnv   = "cnv_01900000-0000-7000-8000-000000000001"
	testLayer = "clyr_01900000-0000-7000-8000-000000000002"
	testSes   = "ses_01900000-0000-7000-8000-000000000001"
	lineA     = "{\"a\":1}\n"
	lineB     = "{\"b\":2}\n"
)

type fixture struct {
	t    *testing.T
	s    *Server
	c    *sessionstream.Client
	hash *sessionstream.StreamHash
	off  int64
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	s := New(t)
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	c := &sessionstream.Client{BaseURL: s.URL, Token: Token, Key: priv, HTTP: &http.Client{Timeout: 2 * time.Second}}
	return &fixture{t: t, s: s, c: c, hash: sessionstream.NewStreamHash()}
}

func (f *fixture) register() {
	f.t.Helper()
	if _, err := f.c.RegisterKey(context.Background(), "test"); err != nil {
		f.t.Fatalf("register: %v", err)
	}
}

func (f *fixture) openReq() sessionstream.OpenSessionRequest {
	return sessionstream.OpenSessionRequest{LayerID: testLayer, RepoID: "repo_x", SessionID: testSes, SessionName: "n", Agent: "claude", StartedAt: time.Now()}
}

func (f *fixture) open() (sessionstream.OpenSessionResponse, error) {
	return f.c.Open(context.Background(), testCnv, f.openReq())
}

func (f *fixture) ready() {
	f.t.Helper()
	f.register()
	if _, err := f.open(); err != nil {
		f.t.Fatalf("open: %v", err)
	}
}

func (f *fixture) append(body string) (sessionstream.AppendResponse, error) {
	resp, err := f.c.AppendRaw(context.Background(), testCnv, testLayer, f.off, f.hash.Sum(), []byte(body))
	if err == nil {
		f.hash.Write([]byte(body))
		f.off += int64(len(body))
	}
	return resp, err
}

func (f *fixture) closeReq() sessionstream.CloseRequest {
	return sessionstream.CloseRequest{LayerID: testLayer, StoredThrough: f.off, StreamSHA256: f.hash.Sum()}
}

func wantRefusal(t *testing.T, err error, status int, reason string, action sessionstream.Action) *sessionstream.APIError {
	t.Helper()
	var api *sessionstream.APIError
	if !errors.As(err, &api) {
		t.Fatalf("want refusal %d %s, got %v", status, reason, err)
	}
	if api.Status != status || api.Body.Reason != reason || api.Body.Action != action {
		t.Fatalf("want %d %s/%s, got %d %s/%s", status, reason, action, api.Status, api.Body.Reason, api.Body.Action)
	}
	return api
}

func wantNetworkError(t *testing.T, err error) {
	t.Helper()
	var api *sessionstream.APIError
	if err == nil || errors.As(err, &api) {
		t.Fatalf("want a dropped connection, got %v", err)
	}
}

func TestServerStoresVerifiedAppendsAndRecordsEveryCall(t *testing.T) {
	f := newFixture(t)
	cfg := f.s.Config(t)
	if tok, err := cfg.Token(context.Background(), ""); err != nil || tok != Token || cfg.Endpoint != f.s.URL {
		t.Fatalf("config token=%q err=%v endpoint=%q", tok, err, cfg.Endpoint)
	}
	cfg.Logger.Info("routed into the test log")

	f.ready()
	opened, err := f.open() // resume is idempotent
	if err != nil || opened.Status != sessionstream.StatusLive || opened.StoredThrough != 0 {
		t.Fatalf("resume open: %+v %v", opened, err)
	}
	resp, err := f.append(lineA + lineB)
	if err != nil || resp.StoredThrough != int64(len(lineA+lineB)) || !resp.TeammateCommentsListed {
		t.Fatalf("append: %+v %v", resp, err)
	}
	if got := string(f.s.Stored(t, testCnv, testLayer)); got != lineA+lineB {
		t.Fatalf("stored %q", got)
	}
	if layers := f.s.Layers(testCnv); len(layers) != 1 || layers[0] != testLayer {
		t.Fatalf("layers %v", layers)
	}
	if layers := f.s.Layers("cnv_unknown"); len(layers) != 0 {
		t.Fatalf("unknown conversation has layers %v", layers)
	}
	if err := f.c.Close(context.Background(), testCnv, f.closeReq()); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := f.c.Close(context.Background(), testCnv, f.closeReq()); err != nil {
		t.Fatalf("idempotent close: %v", err)
	}
	closed, ok := f.s.ClosedWith(testCnv)
	if !ok || closed.StoredAtClose != f.off || closed.StoredThrough != f.off {
		t.Fatalf("closed %+v ok=%v", closed, ok)
	}
	if _, ok := f.s.ClosedWith("cnv_other"); ok {
		t.Fatal("unknown conversation reported closed")
	}
	if f.s.CallsTo(http.MethodPost, "/lines") != 1 || f.s.CallsTo(http.MethodPut, testCnv) != 2 || f.s.CallsTo("GET", "x") != 0 {
		t.Fatalf("call counts wrong: %+v", f.s.Calls())
	}
	var appendCall Call
	for _, c := range f.s.Calls() {
		if strings.HasSuffix(c.Path, "/lines") {
			appendCall = c
		}
	}
	if appendCall.Offset != 0 || appendCall.Status != http.StatusOK || appendCall.BodyLen != len(lineA+lineB) {
		t.Fatalf("append call %+v", appendCall)
	}
	if f.s.InFlight() != 0 || f.s.MaxConcurrent() < 1 {
		t.Fatalf("inflight=%d max=%d", f.s.InFlight(), f.s.MaxConcurrent())
	}
	// a closed conversation refuses new writes and new opens
	_, err = f.append(lineA)
	wantRefusal(t, err, http.StatusConflict, sessionstream.ReasonSessionClosed, sessionstream.ActionStop)
	_, err = f.open()
	wantRefusal(t, err, http.StatusConflict, sessionstream.ReasonSessionClosed, sessionstream.ActionStop)
}

func TestServerRefusesRequestsThatAreNotAuthenticAndSigned(t *testing.T) {
	f := newFixture(t)

	t.Run("not logged in", func(t *testing.T) {
		bad := *f.c
		bad.Token = "stale"
		_, err := bad.RegisterKey(context.Background(), "x")
		wantRefusal(t, err, http.StatusUnauthorized, sessionstream.ReasonUnauthenticated, sessionstream.ActionReauth)
	})
	t.Run("unsigned request", func(t *testing.T) {
		bad := *f.c
		bad.Key = nil
		_, err := bad.Open(context.Background(), testCnv, f.openReq())
		wantRefusal(t, err, http.StatusUnauthorized, sessionstream.ReasonBadSignature, sessionstream.ActionReauth)
	})
	t.Run("unregistered key", func(t *testing.T) {
		_, err := f.open()
		wantRefusal(t, err, http.StatusUnauthorized, sessionstream.ReasonUnknownKey, sessionstream.ActionReauth)
	})
	t.Run("expired signature", func(t *testing.T) {
		f.register()
		old := *f.c
		old.Now = func() time.Time { return time.Now().Add(-24 * time.Hour) }
		_, err := old.Open(context.Background(), testCnv, f.openReq())
		wantRefusal(t, err, http.StatusUnauthorized, sessionstream.ReasonSignatureExpired, sessionstream.ActionReauth)
	})
	t.Run("signature over other bytes", func(t *testing.T) {
		h := http.Header{}
		h.Set(sessionstream.HeaderSignature, sessionstream.Sign(f.c.Key, http.MethodPut, "/other", time.Now(), nil))
		noKey := *f.c
		noKey.Key = nil
		err := noKey.Do(context.Background(), http.MethodPut, sessionstream.SessionPath(testCnv), []byte("{}"), "application/json", h, nil)
		wantRefusal(t, err, http.StatusUnauthorized, sessionstream.ReasonBadSignature, sessionstream.ActionReauth)
	})
	t.Run("auth knob", func(t *testing.T) {
		f.s.RejectAuth(true)
		_, err := f.open()
		wantRefusal(t, err, http.StatusUnauthorized, sessionstream.ReasonUnauthenticated, sessionstream.ActionReauth)
		f.s.RejectAuth(false)
		if _, err := f.open(); err != nil {
			t.Fatalf("after clearing: %v", err)
		}
	})
	t.Run("down knob", func(t *testing.T) {
		f.s.SetDown(true)
		_, err := f.open()
		wantRefusal(t, err, http.StatusServiceUnavailable, sessionstream.ReasonUnavailable, sessionstream.ActionRetry)
		f.s.SetDown(false)
	})
}

func TestServerKeyRegistrationHonorsOneKeyPerLogin(t *testing.T) {
	f := newFixture(t)
	f.register()
	f.register() // the same key again is fine

	_, other, _ := ed25519.GenerateKey(rand.Reader)
	second := *f.c
	second.Key = other
	_, err := second.RegisterKey(context.Background(), "other device")
	wantRefusal(t, err, http.StatusConflict, sessionstream.ReasonForbidden, sessionstream.ActionStop)

	f.s.ForceKeyConflict(true)
	_, err = f.c.RegisterKey(context.Background(), "x")
	wantRefusal(t, err, http.StatusConflict, sessionstream.ReasonForbidden, sessionstream.ActionStop)
	f.s.ForceKeyConflict(false)

	for name, body := range map[string]string{"not json": "{", "bad key": `{"public_key":"!!"}`} {
		err := f.c.Do(context.Background(), http.MethodPost, sessionstream.PathKeys, []byte(body), "application/json", nil, nil)
		if api := wantRefusalAny(t, err); api.Status != http.StatusBadRequest || api.Body.Reason != sessionstream.ReasonInvalidRequest {
			t.Fatalf("%s: %+v", name, api)
		}
	}
	// a registration signed by a different key than the one it carries is refused
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	other2 := *f.c
	other2.Key = f.c.Key
	body := []byte(`{"public_key":"` + sessionstream.EncodePublicKey(pub) + `"}`)
	err = other2.Do(context.Background(), http.MethodPost, sessionstream.PathKeys, body, "application/json", nil, nil)
	wantRefusal(t, err, http.StatusUnauthorized, sessionstream.ReasonBadSignature, sessionstream.ActionReauth)
}

func wantRefusalAny(t *testing.T, err error) *sessionstream.APIError {
	t.Helper()
	var api *sessionstream.APIError
	if !errors.As(err, &api) {
		t.Fatalf("want a refusal, got %v", err)
	}
	return api
}

func TestServerOpenRefusalsAndLostReplies(t *testing.T) {
	f := newFixture(t)
	f.register()

	f.s.RefuseOpen(1)
	_, err := f.open()
	wantRefusal(t, err, http.StatusServiceUnavailable, sessionstream.ReasonUnavailable, sessionstream.ActionRetry)

	f.s.RefuseOpensAsNameTaken(true)
	_, err = f.open()
	wantRefusal(t, err, http.StatusConflict, sessionstream.ReasonSessionNameTaken, sessionstream.ActionStop)
	f.s.RefuseOpensAsNameTaken(false)

	bad := f.openReq()
	bad.SessionID = "ses_01900000-0000-7000-8000-00000000ffff"
	_, err = f.c.Open(context.Background(), testCnv, bad)
	wantRefusal(t, err, http.StatusBadRequest, sessionstream.ReasonInvalidRequest, sessionstream.ActionFix)
	_, err = f.c.Open(context.Background(), "cnv_nope", f.openReq())
	wantRefusal(t, err, http.StatusBadRequest, sessionstream.ReasonInvalidRequest, sessionstream.ActionFix)
	err = f.c.Do(context.Background(), http.MethodPut, sessionstream.SessionPath(testCnv), []byte("{"), "application/json", nil, nil)
	wantRefusal(t, err, http.StatusBadRequest, sessionstream.ReasonInvalidRequest, sessionstream.ActionFix)

	f.s.DropOpenReply(1)
	_, err = f.open()
	wantNetworkError(t, err)
	if got := f.s.Layers(testCnv); len(got) != 1 {
		t.Fatalf("the open that lost its reply must still have happened: %v", got)
	}
	if _, err := f.open(); err != nil {
		t.Fatalf("retry after lost reply: %v", err)
	}
}

// RefuseNextOpens answers n valid opens, reopen or not, creates nothing, and
// leaves an invalid open to the validation it would fail anyway.
func TestServerRefuseNextOpensAnswersNValidOpens(t *testing.T) {
	f := newFixture(t)
	f.register()

	f.s.RefuseNextOpens(2, http.StatusForbidden, sessionstream.ReasonFeatureOff, sessionstream.ActionStop)
	bad := f.openReq()
	bad.SessionID = "ses_01900000-0000-7000-8000-00000000ffff"
	_, err := f.c.Open(context.Background(), testCnv, bad)
	wantRefusal(t, err, http.StatusBadRequest, sessionstream.ReasonInvalidRequest, sessionstream.ActionFix)

	reopen := f.openReq()
	reopen.Reopen = true
	_, err = f.c.Open(context.Background(), testCnv, reopen)
	wantRefusal(t, err, http.StatusForbidden, sessionstream.ReasonFeatureOff, sessionstream.ActionStop)
	_, err = f.open()
	wantRefusal(t, err, http.StatusForbidden, sessionstream.ReasonFeatureOff, sessionstream.ActionStop)
	if got := f.s.Layers(testCnv); len(got) != 0 {
		t.Fatalf("a refused open created layers: %v", got)
	}

	if _, err := f.open(); err != nil {
		t.Fatalf("the refusal answers n opens only: %v", err)
	}
}

func TestServerAppendKnobsRefuseThenRecover(t *testing.T) {
	f := newFixture(t)
	f.ready()

	f.s.ForceNewLayer(1)
	_, err := f.append(lineA)
	api := wantRefusal(t, err, http.StatusConflict, sessionstream.ReasonBytesConflict, sessionstream.ActionNewLayer)
	if api.Body.StoredThrough == nil {
		t.Fatal("new_layer refusal must carry stored_through")
	}

	f.s.ForceResync(0)
	_, err = f.append(lineA)
	wantRefusal(t, err, http.StatusConflict, sessionstream.ReasonOffsetGap, sessionstream.ActionResync)

	f.s.RateLimit(1, 1500)
	_, err = f.append(lineA)
	api = wantRefusal(t, err, http.StatusTooManyRequests, sessionstream.ReasonRateLimited, sessionstream.ActionRetry)
	if api.Body.RetryAfterMS == nil || *api.Body.RetryAfterMS != 1500 {
		t.Fatalf("retry_after_ms %v", api.Body.RetryAfterMS)
	}

	f.s.RefuseNextAppend(http.StatusTeapot, "odd", "")
	_, err = f.append(lineA)
	wantRefusal(t, err, http.StatusTeapot, "odd", "")

	f.s.DropAfterWrite(1)
	_, err = f.append(lineA)
	wantNetworkError(t, err)
	// the bytes landed: resending the same batch from offset 0 is accepted as already stored
	resp, err := f.append(lineA)
	if err != nil || resp.StoredThrough != int64(len(lineA)) {
		t.Fatalf("resend after lost reply: %+v %v", resp, err)
	}

	_, err = f.c.AppendRaw(context.Background(), testCnv, "clyr_01900000-0000-7000-8000-0000000000ff", 0, "", []byte(lineA))
	wantRefusal(t, err, http.StatusNotFound, sessionstream.ReasonNotFound, sessionstream.ActionStop)
	err = f.c.Do(context.Background(), http.MethodPost, sessionstream.LinesPath(testCnv, testLayer)+"?offset=-x", []byte(lineA), sessionstream.ContentTypeNDJSON, nil, nil)
	wantRefusal(t, err, http.StatusBadRequest, sessionstream.ReasonInvalidRequest, sessionstream.ActionFix)
	_, err = f.c.AppendRaw(context.Background(), testCnv, testLayer, f.off, "wrong", []byte(lineB))
	wantRefusalAny(t, err)
}

func TestServerRebuildsALostProgressRowFromStoredObjects(t *testing.T) {
	f := newFixture(t)
	f.ready()
	if _, err := f.append(lineA); err != nil {
		t.Fatal(err)
	}

	f.s.LoseProgressRow(testCnv)
	f.s.LoseProgressRow("cnv_unknown") // unknown conversation is a no-op

	f.s.DeferRecovery(true)
	_, err := f.append(lineB)
	api := wantRefusal(t, err, http.StatusServiceUnavailable, sessionstream.ReasonUnavailable, sessionstream.ActionRetry)
	if api.Body.RetryAfterMS == nil || *api.Body.RetryAfterMS != storageRetryAfterMS {
		t.Fatalf("deferred recovery must carry retry_after_ms: %+v", api.Body)
	}
	err = f.c.Close(context.Background(), testCnv, f.closeReq())
	wantRefusal(t, err, http.StatusServiceUnavailable, sessionstream.ReasonUnavailable, sessionstream.ActionRetry)
	f.s.DeferRecovery(false)

	resp, err := f.append(lineB)
	if err != nil || resp.StoredThrough != int64(len(lineA+lineB)) {
		t.Fatalf("append after row loss must rebuild, not resync: %+v %v", resp, err)
	}

	f.s.LoseProgressRow(testCnv)
	if err := f.c.Close(context.Background(), testCnv, f.closeReq()); err != nil {
		t.Fatalf("close after row loss must rebuild: %v", err)
	}
}

func TestServerLostStorageAnswersResyncFromZero(t *testing.T) {
	f := newFixture(t)
	f.ready()
	if _, err := f.append(lineA); err != nil {
		t.Fatal(err)
	}
	f.s.LoseStoredBytes(testCnv)
	f.s.LoseStoredBytes("cnv_unknown")
	if got := f.s.Stored(t, testCnv, testLayer); len(got) != 0 {
		t.Fatalf("bytes survived: %q", got)
	}
	err := f.c.Close(context.Background(), testCnv, f.closeReq())
	api := wantRefusal(t, err, http.StatusConflict, sessionstream.ReasonStreamMismatch, sessionstream.ActionResync)
	if api.Body.StoredThrough == nil || *api.Body.StoredThrough != 0 {
		t.Fatalf("resync must point at the empty layer: %+v", api.Body)
	}
}

func TestServerCloseValidatesAndHonorsRefusals(t *testing.T) {
	f := newFixture(t)
	f.ready()
	if _, err := f.append(lineA); err != nil {
		t.Fatal(err)
	}

	err := f.c.Close(context.Background(), "cnv_01900000-0000-7000-8000-0000000000aa", f.closeReq())
	wantRefusal(t, err, http.StatusNotFound, sessionstream.ReasonNotFound, sessionstream.ActionStop)
	unknownLayer := f.closeReq()
	unknownLayer.LayerID = "clyr_01900000-0000-7000-8000-0000000000ff"
	err = f.c.Close(context.Background(), testCnv, unknownLayer)
	wantRefusal(t, err, http.StatusNotFound, sessionstream.ReasonNotFound, sessionstream.ActionStop)

	stored := int64(7)
	f.s.RefuseNextClose(http.StatusServiceUnavailable, sessionstream.ReasonUnavailable, sessionstream.ActionRetry, &stored)
	err = f.c.Close(context.Background(), testCnv, f.closeReq())
	api := wantRefusal(t, err, http.StatusServiceUnavailable, sessionstream.ReasonUnavailable, sessionstream.ActionRetry)
	if api.Body.StoredThrough == nil || *api.Body.StoredThrough != 7 {
		t.Fatalf("refusal stored_through %+v", api.Body)
	}

	short := f.closeReq()
	short.StoredThrough = 1
	err = f.c.Close(context.Background(), testCnv, short)
	wantRefusal(t, err, http.StatusConflict, sessionstream.ReasonStreamMismatch, sessionstream.ActionResync)

	if err := f.c.Close(context.Background(), testCnv, f.closeReq()); err != nil {
		t.Fatalf("close: %v", err)
	}
	// a different layer of a closed conversation cannot be closed afterwards
	f.s.Preload(t, testCnv, "clyr_01900000-0000-7000-8000-0000000000bb", []byte(lineA))
	second := sessionstream.CloseRequest{LayerID: "clyr_01900000-0000-7000-8000-0000000000bb", StoredThrough: int64(len(lineA)), StreamSHA256: sessionstream.BytesSHA256([]byte(lineA))}
	err = f.c.Close(context.Background(), testCnv, second)
	wantRefusal(t, err, http.StatusConflict, sessionstream.ReasonSessionClosed, sessionstream.ActionStop)
}

func TestServerDeleteErasesObjectsAndAnswersGone(t *testing.T) {
	f := newFixture(t)
	f.ready()
	if _, err := f.append(lineA); err != nil {
		t.Fatal(err)
	}

	err := f.c.Delete(context.Background(), "cnv_01900000-0000-7000-8000-0000000000aa")
	wantRefusal(t, err, http.StatusNotFound, sessionstream.ReasonNotFound, sessionstream.ActionStop)

	f.s.RefuseDelete(true)
	err = f.c.Delete(context.Background(), testCnv)
	wantRefusal(t, err, http.StatusForbidden, sessionstream.ReasonForbidden, sessionstream.ActionStop)
	f.s.RefuseDelete(false)

	if err := f.c.Delete(context.Background(), testCnv); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if !f.s.IsDeleted(testCnv) || f.s.IsDeleted("cnv_other") {
		t.Fatal("IsDeleted wrong")
	}
	if got := f.s.Stored(t, testCnv, testLayer); len(got) != 0 {
		t.Fatalf("deleted bytes survived: %q", got)
	}
	err = f.c.Delete(context.Background(), testCnv)
	wantRefusal(t, err, http.StatusGone, sessionstream.ReasonSessionDeleted, sessionstream.ActionStopAndWipe)
	_, err = f.append(lineB)
	wantRefusal(t, err, http.StatusGone, sessionstream.ReasonSessionDeleted, sessionstream.ActionStopAndWipe)
	err = f.c.Close(context.Background(), testCnv, f.closeReq())
	wantRefusal(t, err, http.StatusGone, sessionstream.ReasonSessionDeleted, sessionstream.ActionStopAndWipe)
	_, err = f.open()
	wantRefusal(t, err, http.StatusGone, sessionstream.ReasonSessionDeleted, sessionstream.ActionStopAndWipe)
}

func TestServerOwnerDeletionFromTheWebAnswersGone(t *testing.T) {
	f := newFixture(t)
	f.ready()
	f.s.DeleteSession(testCnv)
	f.s.DeleteSession("cnv_unknown")
	_, err := f.append(lineA)
	wantRefusal(t, err, http.StatusGone, sessionstream.ReasonSessionDeleted, sessionstream.ActionStopAndWipe)
}

func TestServerPreloadStoresBatchesAsAnEarlierRunWould(t *testing.T) {
	f := newFixture(t)
	f.register()
	f.s.Preload(t, testCnv, testLayer, []byte(lineA), []byte(lineB))
	if got := string(f.s.Stored(t, testCnv, testLayer)); got != lineA+lineB {
		t.Fatalf("stored %q", got)
	}
	opened, err := f.open()
	if err != nil || opened.StoredThrough != int64(len(lineA+lineB)) {
		t.Fatalf("open must report the preloaded end: %+v %v", opened, err)
	}
}

func TestServerNetworkFaultsStallDelayAndBlockAtTheEdge(t *testing.T) {
	f := newFixture(t)
	f.ready()

	t.Run("edge answers before api-go", func(t *testing.T) {
		f.s.BlockAtEdge(http.MethodPost, 1, "text/html", "<html>blocked</html>")
		if _, err := f.open(); err != nil { // PUT is not the blocked method
			t.Fatalf("open: %v", err)
		}
		_, err := f.append(lineA)
		api := wantRefusalAny(t, err)
		if api.Status != http.StatusForbidden || !strings.Contains(api.Body.Error, "blocked") {
			t.Fatalf("edge refusal %+v", api)
		}
		if _, err := f.append(lineA); err != nil {
			t.Fatalf("the block spends one answer: %v", err)
		}
		f.s.BlockAtEdge("", 1, "application/json", `{"message":"waf"}`)
		_, err = f.append(lineB)
		wantRefusalAny(t, err)
	})
	t.Run("hang until the client gives up", func(t *testing.T) {
		f.s.Hang(true)
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		_, err := f.c.Open(ctx, testCnv, f.openReq())
		wantNetworkError(t, err)
		f.s.Hang(false)
		waitIdle(t, f.s)
	})
	t.Run("large appends hang, small go through", func(t *testing.T) {
		f.s.HangAppendsOver(len(lineB))
		if _, err := f.append(lineB); err != nil {
			t.Fatalf("a batch at the limit goes through: %v", err)
		}
		big := lineA + lineA + lineA
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		_, err := f.c.AppendRaw(ctx, testCnv, testLayer, f.off, f.hash.Sum(), []byte(big))
		wantNetworkError(t, err)
		f.s.HangAppendsOver(0)
		waitIdle(t, f.s)
		if _, err := f.append(big); err != nil {
			t.Fatalf("cleared: %v", err)
		}
	})
	t.Run("latency overlaps callers and open delay hits only opens", func(t *testing.T) {
		f.s.Latency(30 * time.Millisecond)
		f.s.DelayOpen(30 * time.Millisecond)
		var wg sync.WaitGroup
		start := time.Now()
		for range 3 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, _ = f.open()
			}()
		}
		wg.Wait()
		if time.Since(start) < 60*time.Millisecond {
			t.Fatalf("open delay not applied: %v", time.Since(start))
		}
		if f.s.MaxConcurrent() < 2 {
			t.Fatalf("callers did not overlap: %d", f.s.MaxConcurrent())
		}
		f.s.Latency(0)
		f.s.DelayOpen(0)
	})
}

func waitIdle(t *testing.T, s *Server) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for s.InFlight() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("%d requests still in flight", s.InFlight())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func comment(id string) sessionstream.TeammateComment {
	return sessionstream.TeammateComment{ID: id, AuthorName: "Teammate A", Body: "note " + id, CreatedAt: time.Now()}
}

func TestServerRedeliversTeammateCommentsUntilAckRecorded(t *testing.T) {
	f := newFixture(t)
	f.ready()
	for _, id := range []string{"c1", "c2", "c3", "c4", "c5", "c6", "c7"} {
		f.s.QueueTeammateComment(comment(id))
	}
	resp, err := f.append(lineA)
	if err != nil || len(resp.TeammateComments) != sessionstream.MaxTeammateCommentsPerAppend || resp.TeammateComments[0].ID != "c1" {
		t.Fatalf("first append lists %d (cap) oldest first: %+v %v", sessionstream.MaxTeammateCommentsPerAppend, resp.TeammateComments, err)
	}

	ack := func(ids ...string) (sessionstream.AckTeammateCommentsResponse, error) {
		req := sessionstream.AckTeammateCommentsRequest{LayerID: testLayer}
		for _, id := range ids {
			req.Acks = append(req.Acks, sessionstream.TeammateCommentAck{ID: id, DeliveredAtSeq: 4})
		}
		return f.c.AckTeammateComments(context.Background(), testCnv, req)
	}
	if got, err := ack("c1", "c2"); err != nil || got.Recorded != 2 {
		t.Fatalf("ack: %+v %v", got, err)
	}
	if got, err := ack("c1"); err != nil || got.Recorded != 0 {
		t.Fatalf("a repeat ack records nothing (first wins): %+v %v", got, err)
	}
	if seq, ok := f.s.DeliveredAtSeq("c1"); !ok || seq != 4 {
		t.Fatalf("delivered %d %v", seq, ok)
	}
	if _, ok := f.s.DeliveredAtSeq("c7"); ok {
		t.Fatal("unacked comment reported delivered")
	}
	zero := sessionstream.AckTeammateCommentsRequest{LayerID: testLayer, Acks: []sessionstream.TeammateCommentAck{{ID: "c3", DeliveredAtSeq: 0}}}
	if got, err := f.c.AckTeammateComments(context.Background(), testCnv, zero); err != nil || got.Recorded != 0 {
		t.Fatalf("a non-positive seq is not recorded: %+v %v", got, err)
	}
	if n := len(f.s.AckRequests()); n != 3 {
		t.Fatalf("accepted ack bodies %d", n)
	}

	f.s.RemoveTeammateComment("c3")
	resp, err = f.append(lineB)
	if err != nil || len(resp.TeammateComments) != 4 || resp.TeammateComments[0].ID != "c4" {
		t.Fatalf("second append lists the undelivered rest, minus the removed: %+v %v", resp.TeammateComments, err)
	}

	f.s.FailTeammateLookup(true)
	resp, err = f.append("{\"c\":3}\n")
	if err != nil || resp.TeammateCommentsListed || len(resp.TeammateComments) != 0 {
		t.Fatalf("a failed lookup answers without a list: %+v %v", resp, err)
	}
	f.s.FailTeammateLookup(false)
}

func TestServerAckFaultsAreVisibleToTheCallerNotJustTheServer(t *testing.T) {
	f := newFixture(t)
	f.ready()
	f.s.QueueTeammateComment(comment("c1"))
	req := sessionstream.AckTeammateCommentsRequest{LayerID: testLayer, Acks: []sessionstream.TeammateCommentAck{{ID: "c1", DeliveredAtSeq: 2}}}
	ack := func() (sessionstream.AckTeammateCommentsResponse, error) {
		return f.c.AckTeammateComments(context.Background(), testCnv, req)
	}

	for name, bad := range map[string]sessionstream.AckTeammateCommentsRequest{
		"empty":    {LayerID: testLayer},
		"too many": {LayerID: testLayer, Acks: make([]sessionstream.TeammateCommentAck, sessionstream.MaxTeammateCommentAcks+1)},
	} {
		_, err := f.c.AckTeammateComments(context.Background(), testCnv, bad)
		if api := wantRefusalAny(t, err); api.Status != http.StatusBadRequest {
			t.Fatalf("%s: %+v", name, api)
		}
	}
	if err := f.c.Do(context.Background(), http.MethodPost, sessionstream.TeammateCommentAcksPath(testCnv), []byte("{"), "application/json", nil, nil); err == nil {
		t.Fatal("malformed ack accepted")
	}

	f.s.DropAcksBeforeRecording(1)
	_, err := ack()
	wantNetworkError(t, err)
	if _, ok := f.s.DeliveredAtSeq("c1"); ok {
		t.Fatal("a drop before recording must record nothing")
	}

	f.s.RefuseNextAck(http.StatusServiceUnavailable, sessionstream.ReasonUnavailable, sessionstream.ActionRetry)
	_, err = ack()
	wantRefusal(t, err, http.StatusServiceUnavailable, sessionstream.ReasonUnavailable, sessionstream.ActionRetry)
	f.s.RefuseAcks(2, http.StatusBadRequest, sessionstream.ReasonInvalidRequest, sessionstream.ActionFix)
	for range 2 {
		_, err = ack()
		wantRefusal(t, err, http.StatusBadRequest, sessionstream.ReasonInvalidRequest, sessionstream.ActionFix)
	}

	f.s.IgnoreNextAcks(1)
	if got, err := ack(); err != nil || got.Recorded != 0 {
		t.Fatalf("ignored ack answers 200 and records nothing: %+v %v", got, err)
	}
	if _, ok := f.s.DeliveredAtSeq("c1"); ok {
		t.Fatal("ignored ack was recorded")
	}

	f.s.DropAcksAfterRecording(1)
	_, err = ack()
	wantNetworkError(t, err)
	if seq, ok := f.s.DeliveredAtSeq("c1"); !ok || seq != 2 {
		t.Fatalf("a drop after recording must have recorded: %d %v", seq, ok)
	}

	statuses := []int{}
	for _, a := range f.s.AckAttempts() {
		statuses = append(statuses, a.Status)
	}
	want := []int{0, 503, 400, 400, 200, 0}
	if len(statuses) < len(want)+1 { // the three malformed requests before are 400s too
		t.Fatalf("attempts %v", statuses)
	}
	if got := statuses[len(statuses)-len(want):]; !equalInts(got, want) {
		t.Fatalf("attempt statuses %v, want tail %v", statuses, want)
	}
	if ids := f.s.AckAttempts()[len(statuses)-1].IDs; len(ids) != 1 || ids[0] != "c1" {
		t.Fatalf("attempt ids %v", ids)
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestServerStoredSurvivesBinarySafeBytes(t *testing.T) {
	f := newFixture(t)
	f.ready()
	body := strings.Repeat("{\"k\":\"v\"}\n", 50)
	if _, err := f.append(body); err != nil {
		t.Fatal(err)
	}
	if got := f.s.Stored(t, testCnv, testLayer); !bytes.Equal(got, []byte(body)) {
		t.Fatalf("stored %d bytes, want %d", len(got), len(body))
	}
}

// fatalTB turns Fatalf into a recoverable panic so a test can assert that a
// helper refuses loudly instead of handing back a wrong answer.
type fatalTB struct {
	testing.TB
	msg string
}

func (f *fatalTB) Fatalf(format string, args ...any) {
	f.msg = fmt.Sprintf(format, args...)
	panic(f)
}

func expectFatal(t *testing.T, fn func(tb testing.TB)) string {
	t.Helper()
	tb := &fatalTB{TB: t}
	func() {
		defer func() {
			if r := recover(); r != nil && r != any(tb) {
				panic(r)
			}
		}()
		fn(tb)
	}()
	if tb.msg == "" {
		t.Fatal("helper did not fail")
	}
	return tb.msg
}

func TestServerHelpersFailLoudlyOnAnUnusableLayer(t *testing.T) {
	f := newFixture(t)
	if msg := expectFatal(t, func(tb testing.TB) { f.s.Stored(tb, "bad cnv", testLayer) }); !strings.Contains(msg, "bytes prefix") {
		t.Fatalf("stored: %q", msg)
	}
	if msg := expectFatal(t, func(tb testing.TB) { f.s.Preload(tb, testCnv, testLayer, []byte("not ndjson")) }); !strings.Contains(msg, "preload batch") {
		t.Fatalf("preload: %q", msg)
	}
}

// A layer the object keys cannot name proves nothing about the store, so the
// fake answers retry rather than inventing a stored end.
func TestServerAnswersRetryWhenStorageCannotNameALayer(t *testing.T) {
	f := newFixture(t)
	f.ready()
	f.s.mu.Lock()
	f.s.convs[testCnv].layers["bad"] = &layerState{cur: sessionstream.NewCursor()}
	f.s.mu.Unlock()

	_, err := f.c.AppendRaw(context.Background(), testCnv, "bad", 0, sessionstream.EmptyStreamSHA256, []byte(lineA))
	wantRefusal(t, err, http.StatusServiceUnavailable, sessionstream.ReasonUnavailable, sessionstream.ActionRetry)
	err = f.c.Close(context.Background(), testCnv, sessionstream.CloseRequest{LayerID: "bad", StoredThrough: 5, StreamSHA256: "x"})
	wantRefusal(t, err, http.StatusServiceUnavailable, sessionstream.ReasonUnavailable, sessionstream.ActionRetry)
}

func TestServerKeepsItsRowWhenStoredObjectsDisagreeOrCannotFormAStream(t *testing.T) {
	t.Run("diverged", func(t *testing.T) {
		f := newFixture(t)
		f.ready()
		if _, err := f.append(lineA + lineB); err != nil {
			t.Fatal(err)
		}
		// a row whose hash the objects do not reproduce: neither may move the other
		f.s.mu.Lock()
		f.s.convs[testCnv].layers[testLayer].cur = sessionstream.Cursor{StoredThrough: 4, PrefixSHA256: strings.Repeat("0", 64)}
		f.s.mu.Unlock()
		err := f.c.Close(context.Background(), testCnv, f.closeReq())
		api := wantRefusal(t, err, http.StatusConflict, sessionstream.ReasonStreamMismatch, sessionstream.ActionResync)
		if api.Body.StoredThrough == nil || *api.Body.StoredThrough != 4 {
			t.Fatalf("the row must stand: %+v", api.Body)
		}
	})
	t.Run("gap in the stored objects", func(t *testing.T) {
		f := newFixture(t)
		f.ready()
		if _, err := f.append(lineA); err != nil {
			t.Fatal(err)
		}
		if _, err := f.append(lineB); err != nil {
			t.Fatal(err)
		}
		prefix, err := sessionstream.BytesPrefix(TeamID, testCnv, testLayer)
		if err != nil {
			t.Fatal(err)
		}
		first, err := sessionstream.BytesKey(TeamID, testCnv, testLayer, 0)
		if err != nil {
			t.Fatal(err)
		}
		if n := f.s.Store.DeletePrefix(first); n != 1 || len(f.s.Store.Keys(prefix)) != 1 {
			t.Fatalf("setup: deleted %d", n)
		}
		f.s.LoseProgressRow(testCnv)
		err = f.c.Close(context.Background(), testCnv, f.closeReq())
		wantRefusal(t, err, http.StatusServiceUnavailable, sessionstream.ReasonUnavailable, sessionstream.ActionRetry)
	})
}
