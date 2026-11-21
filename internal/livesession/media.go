package livesession

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sageox/ox/internal/mediapool"
	"github.com/sageox/ox/internal/paths"
	"github.com/sageox/ox/internal/sessionstream"
	"github.com/sageox/ox/pkg/adapterprotocol"
)

// A pasted image reaches the server through the media lane, out of band: the
// spool holds only a media line naming it by sha256. Before a batch that
// carries media lines is appended, each image is declared, uploaded and
// completed, so a viewer that reads the line can always read the image. An
// upload that cannot finish now (offline, 5xx, an expired URL) leaves the
// batch spooled and retries with the next drain, exactly as text does. An
// image that can never upload becomes a declared gap line in the batch's
// place; image bytes are content and appear in no log.

const (
	// maxMediaPutFailures bounds how many times the storage endpoint may answer
	// one image with an error status before it is given up. A 4xx is often only
	// an expired presigned URL, and every drain declares again for a fresh one,
	// so a few tries separate "expired" from "never going to work" without
	// letting one bad image hold every text turn behind it in the spool.
	maxMediaPutFailures = 5
	// maxMediaPutWindow caps the same thing in time, for a link that drains
	// rarely: once the failing attempts add up to it the image is a gap whatever
	// the count. Transport errors (offline) never count toward either bound:
	// text waits out an outage, and so does the image.
	maxMediaPutWindow = 10 * time.Minute
	// maxMediaPutGap is the most one failure adds to that sum. The time between
	// two failures can hold an outage, a closed laptop or no drain at all, none
	// of it storage refusing the image; capping each step keeps that time from
	// ending a retry (a lone 4xx after hours offline must not read as ten
	// minutes of failures). Drains run seconds apart, well under this cap.
	maxMediaPutGap = 3 * time.Minute

	mediaAttemptsFile = "media-attempts.json"
)

// mediaAttempt is one image's failed storage PUTs, kept across drains in the
// session dir (local only; never uploaded).
type mediaAttempt struct {
	Failures int `json:"failures"`
	// Active is the capped time between failing attempts (maxMediaPutGap).
	Active time.Duration `json:"active"`
	Last   time.Time     `json:"last"`
}

func (s *session) loadAttempts() map[string]mediaAttempt {
	out := map[string]mediaAttempt{}
	if b, err := os.ReadFile(filepath.Join(s.dir, mediaAttemptsFile)); err == nil {
		_ = json.Unmarshal(b, &out) // unreadable means no history: one more round of tries
	}
	return out
}

func (s *session) saveAttempts(a map[string]mediaAttempt) {
	path := filepath.Join(s.dir, mediaAttemptsFile)
	if len(a) == 0 {
		_ = os.Remove(path)
		return
	}
	if b, err := json.Marshal(a); err == nil {
		_ = os.WriteFile(path, b, 0o600) // losing the count only grants extra tries
	}
}

// putStatusError is a storage answer that was not 2xx (as opposed to no answer).
type putStatusError struct{ status int }

func (e *putStatusError) Error() string { return fmt.Sprintf("upload status %d", e.status) }

// MediaNotice is the one-time warning a person gets the first time a pasted
// screenshot streams from this machine. The text stream is secret-scanned
// before it leaves; image bytes cannot be, so they upload as pasted (ADR-247,
// v2), and the person is told once so they can keep a credential out of frame.
const MediaNotice = "Screenshots pasted into this session are shared to its live view on SageOx as they are. Unlike text, images are not scanned for credentials, so keep secrets out of what you paste."

const mediaNoticeFile = "live-media-notice"

// oweMediaNotice records, once per machine, that the notice is owed. It never
// overwrites: a notice already given stays given.
func oweMediaNotice(dataDir string) {
	if dataDir == "" {
		dataDir = paths.DataDir()
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(dataDir, mediaNoticeFile), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return // exists (owed or given), or unwritable: the next image tries again
	}
	werr := writeNoticeMarker(f, "owed\n")
	cerr := f.Close()
	if werr != nil || cerr != nil {
		// an empty marker would block every later attempt (O_EXCL) and TakeMediaNotice
		// rejects it: remove what this call created so a later upload retries
		_ = os.Remove(filepath.Join(dataDir, mediaNoticeFile))
	}
}

// writeNoticeMarker is a variable so a test can fail the write.
var writeNoticeMarker = func(f *os.File, text string) error {
	_, err := f.WriteString(text)
	return err
}

// TakeMediaNotice reports whether the screenshot notice is owed, and marks it
// given so no later call returns true. dataDir empty means the ox data dir.
func TakeMediaNotice(dataDir string) bool {
	if dataDir == "" {
		dataDir = paths.DataDir()
	}
	path := filepath.Join(dataDir, mediaNoticeFile)
	b, err := os.ReadFile(path)
	if err != nil || string(b) != "owed\n" {
		return false
	}
	return os.WriteFile(path, []byte("given\n"), 0o600) == nil
}

// mediaLine is the part of a spooled media line the uploader reads.
type mediaLine struct {
	Type   string `json:"type"`
	SHA256 string `json:"sha256"`
	MIME   string `json:"mime"`
	Size   int64  `json:"size"`
}

var mediaTypeMarker = []byte(`"type":"media"`)

// mediaGap is the strict gap line that stands for one image that cannot upload.
func mediaGap(reason string) []byte {
	return fmt.Appendf(nil, `{"type":"gap","reason":%q,"lost_entries":1}`+"\n", reason)
}

// prepareMedia makes every media line in batch uploadable-or-declared before it
// is sent. It returns the batch to send. When any line became a gap the
// returned batch differs and respool is set: the spool must be rewritten with
// it, because the stream cannot skip bytes that stay in the spool.
func (s *session) prepareMedia(ctx context.Context, batch []byte) (out []byte, respool bool, err error) {
	if !bytes.Contains(batch, mediaTypeMarker) {
		return batch, false, nil
	}
	var next bytes.Buffer
	for line := range bytes.Lines(batch) {
		var m mediaLine
		if !bytes.Contains(line, mediaTypeMarker) || json.Unmarshal(line, &m) != nil || m.Type != sessionstream.LineTypeMedia {
			next.Write(line)
			continue
		}
		reason, err := s.uploadMedia(ctx, m)
		if err != nil {
			return nil, false, err
		}
		if reason == "" {
			next.Write(line)
			continue
		}
		respool = true
		next.Write(mediaGap(reason))
		s.log().WarnContext(ctx, "live_session_media_skipped", "sha256", m.SHA256, "size", m.Size, "reason", reason)
	}
	return next.Bytes(), respool, nil
}

// errStreamEnded means the server ended the stream while an image uploaded.
var errStreamEnded = errors.New("stream ended during media upload")

// uploadMedia stores one image: declare by digest, upload, complete. It returns
// a gap reason when the image can never upload, an error when trying again
// later may work, and "" once the server holds the bytes. Idempotent by digest:
// an image the server already holds is not sent again.
func (s *session) uploadMedia(ctx context.Context, m mediaLine) (string, error) {
	if s.mediaStored[m.SHA256] {
		return "", nil
	}
	if !mediapool.ValidDigest(m.SHA256) || m.Size <= 0 {
		return "media_invalid", nil
	}
	if !adapterprotocol.AllowedMediaMIME(m.MIME) {
		return "media_unsupported_type", nil
	}
	if m.Size > sessionstream.MaxMediaBytes {
		return "media_too_large", nil
	}
	attempts := s.loadAttempts()
	if a := attempts[m.SHA256]; a.Failures >= maxMediaPutFailures || a.Active >= maxMediaPutWindow {
		return "media_upload_failed", nil
	}
	cnv := s.st.ConversationID
	var decl sessionstream.MediaResponse
	req := sessionstream.MediaRequest{SHA256: m.SHA256, Size: m.Size, MIME: m.MIME}
	if err := s.c.Do(ctx, http.MethodPost, sessionstream.MediaPath(cnv), mustJSON(req), "application/json", nil, &decl); err != nil {
		return s.mediaRefused(ctx, "declare", err)
	}
	if !decl.AlreadyStored {
		// local bytes are needed only to upload: a replayed layer restates
		// images the server already holds long after the pool swept them
		data, err := mediapool.Read(s.cfg.DataDir, m.SHA256)
		switch {
		case errors.Is(err, mediapool.ErrMissing), errors.Is(err, mediapool.ErrCorrupt):
			return "media_unavailable", nil
		case err != nil:
			return "", err
		}
		if int64(len(data)) != m.Size {
			return "media_invalid", nil
		}
		if err := s.putMedia(ctx, decl, m.MIME, data); err != nil {
			var status *putStatusError
			if errors.As(err, &status) {
				a := attempts[m.SHA256]
				if a.Failures > 0 {
					a.Active += min(time.Since(a.Last), maxMediaPutGap)
				}
				a.Last = time.Now()
				a.Failures++
				attempts[m.SHA256] = a
				s.saveAttempts(attempts)
				s.log().WarnContext(ctx, "live_session_media_put_failed", "sha256", m.SHA256, "status", status.status, "failures", a.Failures)
			}
			return "", fmt.Errorf("upload media %s: %w", m.SHA256, err)
		}
	}
	// complete also covers AlreadyStored: it re-verifies the stored digest
	if err := s.c.Do(ctx, http.MethodPost, sessionstream.MediaCompletePath(cnv, m.SHA256), nil, "application/json", nil, nil); err != nil {
		return s.mediaRefused(ctx, "complete", err)
	}
	if _, tried := attempts[m.SHA256]; tried {
		delete(attempts, m.SHA256)
		s.saveAttempts(attempts)
	}
	if s.mediaStored == nil {
		s.mediaStored = map[string]bool{}
	}
	s.mediaStored[m.SHA256] = true
	oweMediaNotice(s.cfg.DataDir)
	s.log().InfoContext(ctx, "live_session_media_stored", "sha256", m.SHA256, "size", m.Size, "already_stored", decl.AlreadyStored)
	return "", nil
}

// mediaRefused maps a failed declare or complete: a final refusal of the image
// is a gap, the stream ending ends the drain, anything else is retried.
func (s *session) mediaRefused(ctx context.Context, step string, err error) (string, error) {
	var api *sessionstream.APIError
	if !errors.As(err, &api) {
		return "", fmt.Errorf("%s media: %w", step, err)
	}
	if done, terr := s.terminal(ctx, api); done || terr != nil {
		if terr != nil {
			return "", terr
		}
		return "", errStreamEnded
	}
	if classify(api) == refusalStop {
		if api.Status == http.StatusRequestEntityTooLarge {
			return "media_too_large", nil
		}
		return "media_rejected", nil
	}
	return "", fmt.Errorf("%s media: %w", step, api)
}

// putMedia sends the bytes to the presigned URL, with the headers the server
// bound into it. The URL is a bearer capability for storage, not the API: no
// login token or signature goes with it, and neither the URL nor the body is
// logged. It has no per-request timeout of its own, since an upload's length
// follows the image; the drain's budget bounds it.
func (s *session) putMedia(ctx context.Context, decl sessionstream.MediaResponse, mime string, data []byte) error {
	if decl.UploadURL == "" {
		return errors.New("server returned no upload url")
	}
	if err := checkUploadURL(decl.UploadURL); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, decl.UploadURL, bytes.NewReader(data))
	if err != nil {
		return errors.New("build upload request")
	}
	req.ContentLength = int64(len(data))
	req.Header.Set("Content-Type", mime)
	for k, v := range decl.UploadHeaders {
		if forbiddenUploadHeader(k) {
			continue
		}
		req.Header.Set(k, v)
	}
	hc := *s.cfg.HTTP
	hc.Timeout = 0
	// A redirect would replay the image and the signed headers wherever the
	// storage answer points; a presigned PUT never needs one, so a 3xx is a
	// failed upload like any other status.
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := hc.Do(req)
	if err != nil {
		// a *url.Error embeds the URL; keep it out of the error chain and logs
		var uerr *url.Error
		if errors.As(err, &uerr) {
			err = uerr.Err
		}
		return err
	}
	defer func() { _ = resp.Body.Close() }() // the status is all that is read
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode/100 != 2 {
		return &putStatusError{status: resp.StatusCode}
	}
	return nil
}

// checkUploadURL admits only what a presigned storage URL is: https with a host
// and no embedded credentials. Plain http is for a loopback endpoint (local
// dev and tests), never a remote host, where the image and its signed headers
// would cross the network in the clear. The host is the one the declare answer
// names and the PUT is never redirected from it.
func checkUploadURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil {
		return errors.New("server returned an unusable upload url")
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if ip := net.ParseIP(u.Hostname()); u.Hostname() == "localhost" || (ip != nil && ip.IsLoopback()) {
			return nil
		}
	}
	return errors.New("server returned an upload url that is not https")
}

// forbiddenUploadHeader reports headers the server's declare answer may not set
// on the storage PUT: ox's own credentials never go to storage.
func forbiddenUploadHeader(name string) bool {
	switch strings.ToLower(name) {
	case "authorization", "cookie", "proxy-authorization", strings.ToLower(sessionstream.HeaderSignature):
		return true
	}
	return false
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err) // v is a plain struct of strings and numbers
	}
	return b
}
