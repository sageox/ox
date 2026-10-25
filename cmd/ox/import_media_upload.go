package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mattn/go-isatty"
	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/logger"
	"golang.org/x/sync/errgroup"
)

const (
	// mediaUploadParallelism bounds concurrent part PUTs: enough to keep a fast
	// uplink busy, few enough that each part's share of a slow one stays above
	// uploadFloorBytesPerSecond.
	mediaUploadParallelism = 4

	// mediaUploadRetries is how many times one PUT is retried after a transient
	// failure (network error, 408, 429, 5xx). One dropped connection must not
	// throw away the other parts of a multi-gigabyte upload.
	mediaUploadRetries = 3

	// mediaUploadMaxRepresigns bounds re-presigns per import. Every URL of one
	// upload expires together, so one re-presign normally renews them all;
	// three covers a second expiry on a very slow link without looping forever
	// on a 403 that has nothing to do with expiry.
	mediaUploadMaxRepresigns = 3

	// uploadRequestBaseTimeout plus size/uploadFloorBytesPerSecond is each PUT's
	// deadline. 32 KiB/s is a 1 Mbit/s uplink shared by four parallel parts:
	// the deadline exists to end a dead connection, not to police a slow one.
	// A fixed cap does both badly — the LFS path's 5 minutes failed every large
	// file on an ordinary link. Ctrl-C (context cancellation) remains the
	// person's lever for anything in between.
	uploadRequestBaseTimeout  = time.Minute
	uploadFloorBytesPerSecond = 32 << 10

	// progressThresholdBytes: smaller uploads finish before a progress line
	// would tell anyone anything, so they stay quiet.
	progressThresholdBytes = 50 << 20
)

// mediaUploadRetryBackoff is the first retry delay; each later retry doubles
// it. A variable so tests can retry without sleeping.
var mediaUploadRetryBackoff = time.Second

// mediaUploadClient PUTs to presigned storage URLs. It has no client-wide
// Timeout because each request gets a deadline sized to its bytes. Redirects
// are refused, as on the LFS client: a presigned URL never redirects, and
// following one would send the file to a host the server did not name.
var mediaUploadClient = &http.Client{
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// errRepresignUnsupported: the server issued no upload_id, so repeating the
// upload request would create a second recording rather than renew the URLs.
var errRepresignUnsupported = errors.New("server cannot re-presign this upload")

// uploadSpan is one PUT: a byte range of the file, its presigned URL, and the
// headers signed into that URL. partNumber is 0 for a single whole-file PUT.
type uploadSpan struct {
	partNumber int
	url        string
	headers    map[string]string
	offset     int64
	length     int64
}

// planRecordingUpload turns the server's answer into byte ranges, refusing a
// plan that does not cover the file exactly — uploading against one would
// leave a recording the server can only reject at confirm.
//
// Headers come from the response's upload_headers. A server that predates
// them gets today's behavior: Content-Type on a single PUT, nothing on parts.
func planRecordingUpload(size int64, contentType string, resp *api.RecordingUploadResponse) ([]uploadSpan, error) {
	mp := resp.Multipart
	if mp == nil {
		if err := checkUploadURL(resp.UploadURL); err != nil {
			return nil, err
		}
		headers := resp.UploadHeaders
		if headers == nil {
			headers = map[string]string{"Content-Type": contentType}
		}
		if err := checkSignedLength(headers, size); err != nil {
			return nil, err
		}
		return []uploadSpan{{url: resp.UploadURL, headers: headers, length: size}}, nil
	}

	if resp.UploadID == "" {
		return nil, fmt.Errorf("multipart upload has no upload_id to confirm it with")
	}
	if mp.PartSize <= 0 {
		return nil, fmt.Errorf("multipart upload has invalid part_size %d", mp.PartSize)
	}
	want := int((size + mp.PartSize - 1) / mp.PartSize)
	if len(mp.Parts) != want {
		return nil, fmt.Errorf("multipart upload has %d parts, a %d-byte file in %d-byte parts needs %d", len(mp.Parts), size, mp.PartSize, want)
	}

	parts := append([]api.MultipartPart(nil), mp.Parts...)
	sort.Slice(parts, func(i, j int) bool { return parts[i].PartNumber < parts[j].PartNumber })

	spans := make([]uploadSpan, 0, len(parts))
	for i, p := range parts {
		if p.PartNumber != i+1 {
			return nil, fmt.Errorf("multipart upload parts are not numbered 1..%d", want)
		}
		if err := checkUploadURL(p.UploadURL); err != nil {
			return nil, fmt.Errorf("part %d: %w", p.PartNumber, err)
		}
		offset := int64(i) * mp.PartSize
		length := mp.PartSize
		if rest := size - offset; rest < length {
			length = rest // the last part carries the remainder
		}
		if err := checkSignedLength(p.UploadHeaders, length); err != nil {
			return nil, fmt.Errorf("part %d: %w", p.PartNumber, err)
		}
		spans = append(spans, uploadSpan{
			partNumber: p.PartNumber,
			url:        p.UploadURL,
			headers:    p.UploadHeaders,
			offset:     offset,
			length:     length,
		})
	}
	return spans, nil
}

// checkSignedLength fails when the server signed a Content-Length other than
// the bytes ox will send. Storage would refuse that PUT anyway; failing here
// names the mismatch instead of a signature error. net/http writes the
// header from the request's length, so it is checked rather than copied.
func checkSignedLength(headers map[string]string, length int64) error {
	for k, v := range headers {
		if !strings.EqualFold(k, "Content-Length") {
			continue
		}
		signed, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		if err != nil || signed != length {
			return fmt.Errorf("server signed Content-Length %q for %d bytes", v, length)
		}
	}
	return nil
}

// checkUploadURL requires https outside loopback, the same rule the LFS client
// applies to upload hrefs: the file content and the URL's signature must not
// cross the network in the clear.
func checkUploadURL(raw string) error {
	if raw == "" {
		return fmt.Errorf("server returned no upload URL")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid upload URL")
	}
	if strings.EqualFold(u.Scheme, "https") {
		return nil
	}
	host := u.Hostname()
	if strings.EqualFold(u.Scheme, "http") && (strings.EqualFold(host, "localhost") || isLoopbackIP(host)) {
		return nil
	}
	return fmt.Errorf("upload URL must use https, got %q", u.Scheme)
}

func isLoopbackIP(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// uploadPresigner holds the current URLs for every span and renews them when
// storage refuses one as expired. Renewal repeats POST /upload with the same
// body and Idempotency-Key, which the server answers with the same upload_id
// and fresh URLs for every span.
type uploadPresigner struct {
	size        int64
	contentType string
	uploadID    string
	request     func(context.Context) (*api.RecordingUploadResponse, error)

	mu         sync.Mutex
	generation int // bumped on every renewal
	spans      []uploadSpan
	represigns int
}

func newUploadPresigner(size int64, contentType string, resp *api.RecordingUploadResponse, request func(context.Context) (*api.RecordingUploadResponse, error)) (*uploadPresigner, error) {
	spans, err := planRecordingUpload(size, contentType, resp)
	if err != nil {
		return nil, err
	}
	return &uploadPresigner{size: size, contentType: contentType, uploadID: resp.UploadID, request: request, spans: spans}, nil
}

func (p *uploadPresigner) spanCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.spans)
}

// span returns span i as currently presigned, and the generation it came from.
func (p *uploadPresigner) span(i int) (uploadSpan, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.spans[i], p.generation
}

// represign renews every URL once per expiry. The lock is held across the
// request on purpose: parts that hit the same expiry wait, then find the
// generation moved and reuse the renewal instead of each asking for one.
func (p *uploadPresigner) represign(ctx context.Context, seenGeneration int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.generation != seenGeneration {
		return nil
	}
	if p.uploadID == "" {
		return errRepresignUnsupported
	}
	if p.represigns == mediaUploadMaxRepresigns {
		return fmt.Errorf("storage still refused the upload URLs after %d re-presigns", mediaUploadMaxRepresigns)
	}
	p.represigns++

	resp, err := p.request(ctx)
	if err != nil {
		return fmt.Errorf("re-presign: %w", err)
	}
	if resp.UploadID != p.uploadID {
		return fmt.Errorf("re-presign returned upload %q, not %q", resp.UploadID, p.uploadID)
	}
	spans, err := planRecordingUpload(p.size, p.contentType, resp)
	if err != nil {
		return fmt.Errorf("re-presign: %w", err)
	}
	if len(spans) != len(p.spans) {
		return fmt.Errorf("re-presign changed the upload from %d to %d requests", len(p.spans), len(spans))
	}
	p.spans = spans
	p.generation++
	slog.InfoContext(ctx, "media upload re-presigned", "upload_id", p.uploadID, "represign", p.represigns)
	return nil
}

// uploadRecordingSpans PUTs every span, at most mediaUploadParallelism at a
// time, each from its own section of one open file, so memory stays flat
// whatever the file size. It returns the completed parts for a multipart
// upload, or nil for a single PUT.
func uploadRecordingSpans(ctx context.Context, path string, presigner *uploadPresigner, progress *uploadProgress) ([]api.CompletedPart, error) {
	f, err := os.Open(path) //nolint:gosec // G304: the person's own import argument
	if err != nil {
		return nil, fmt.Errorf("open media file: %w", err)
	}
	defer f.Close()

	n := presigner.spanCount()
	etags := make([]string, n)
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(mediaUploadParallelism)
	for i := range n {
		g.Go(func() error {
			etag, err := putSpanWithRetry(gctx, f, presigner, i, progress)
			if err != nil {
				if span, _ := presigner.span(i); span.partNumber > 0 {
					return fmt.Errorf("part %d: %w", span.partNumber, err)
				}
				return err
			}
			etags[i] = etag
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}

	if first, _ := presigner.span(0); n == 1 && first.partNumber == 0 {
		return nil, nil
	}
	parts := make([]api.CompletedPart, n)
	for i := range n {
		span, _ := presigner.span(i)
		parts[i] = api.CompletedPart{PartNumber: span.partNumber, ETag: etags[i]}
	}
	return parts, nil
}

// retryableUploadError marks a PUT failure another attempt could fix.
type retryableUploadError struct{ err error }

func (e retryableUploadError) Error() string { return e.err.Error() }
func (e retryableUploadError) Unwrap() error { return e.err }

// urlRefusedError marks a PUT storage refused because of the URL itself — a
// 403 (expired or otherwise unacceptable signature) or a 400 ExpiredToken (the
// signing credentials expired). Fresh URLs can fix it; the same URL cannot.
type urlRefusedError struct{ err error }

func (e urlRefusedError) Error() string { return e.err.Error() }
func (e urlRefusedError) Unwrap() error { return e.err }

// putSpanWithRetry PUTs one span. A transient failure is retried with doubling
// backoff; a refused URL is re-presigned and retried without spending a retry.
// Cancellation of ctx (Ctrl-C, or a sibling part failing for good) stops it.
func putSpanWithRetry(ctx context.Context, f *os.File, presigner *uploadPresigner, i int, progress *uploadProgress) (string, error) {
	for attempt := 0; ; {
		span, generation := presigner.span(i)
		etag, err := putSpan(ctx, f, span, progress)
		if err == nil {
			return etag, nil
		}
		if ctx.Err() != nil {
			return "", err
		}

		var refused urlRefusedError
		if errors.As(err, &refused) {
			rerr := presigner.represign(ctx, generation)
			if errors.Is(rerr, errRepresignUnsupported) {
				return "", err
			}
			if rerr != nil {
				return "", fmt.Errorf("%w; %w", err, rerr)
			}
			continue
		}

		var retryable retryableUploadError
		if !errors.As(err, &retryable) {
			return "", err
		}
		if attempt == mediaUploadRetries {
			return "", fmt.Errorf("failed after %d attempts: %w", attempt+1, err)
		}
		delay := mediaUploadRetryBackoff << attempt
		attempt++
		slog.WarnContext(ctx, "media upload attempt failed, retrying",
			"part", span.partNumber, "attempt", attempt, "retry_in", delay, "error", err)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return "", ctx.Err()
		case <-timer.C:
		}
	}
}

// putSpan makes one PUT attempt with the span's signed headers. Content-Length
// comes from the request length, which net/http writes itself and which
// planRecordingUpload already matched against the signed value.
func putSpan(ctx context.Context, f *os.File, span uploadSpan, progress *uploadProgress) (string, error) {
	reqCtx, cancel := context.WithTimeout(ctx, uploadRequestTimeout(span.length))
	defer cancel()

	body := &countingReader{r: io.NewSectionReader(f, span.offset, span.length), progress: progress}
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPut, span.url, body)
	if err != nil {
		return "", fmt.Errorf("create upload request: %w", err)
	}
	req.ContentLength = span.length
	for k, v := range span.headers {
		if !strings.EqualFold(k, "Content-Length") {
			req.Header.Set(k, v)
		}
	}

	// logged without the query string: a presigned URL's signature is a credential
	logURL := redactedUploadURL(span.url)
	logger.LogHTTPRequest(http.MethodPut, logURL, "part", span.partNumber, "bytes", span.length)
	start := time.Now()

	resp, err := mediaUploadClient.Do(req)
	if err != nil {
		progress.add(-body.n.Load())
		logger.LogHTTPError(http.MethodPut, logURL, err, time.Since(start))
		return "", retryableUploadError{fmt.Errorf("network error: %w", err)}
	}
	defer resp.Body.Close()
	logger.LogHTTPResponse(http.MethodPut, logURL, resp.StatusCode, time.Since(start), "part", span.partNumber)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		progress.add(-body.n.Load())
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		text := strings.TrimSpace(string(snippet))
		statusErr := fmt.Errorf("storage answered HTTP %d: %s", resp.StatusCode, text)
		switch {
		case resp.StatusCode == http.StatusForbidden,
			resp.StatusCode == http.StatusBadRequest && strings.Contains(text, "ExpiredToken"):
			return "", urlRefusedError{statusErr}
		case resp.StatusCode >= 500, resp.StatusCode == http.StatusRequestTimeout, resp.StatusCode == http.StatusTooManyRequests:
			return "", retryableUploadError{statusErr}
		}
		return "", statusErr
	}
	_, _ = io.Copy(io.Discard, resp.Body)

	etag := resp.Header.Get("ETag")
	if span.partNumber > 0 && etag == "" {
		return "", fmt.Errorf("storage accepted the part but returned no ETag")
	}
	return etag, nil
}

// uploadRequestTimeout is a PUT's deadline: see uploadFloorBytesPerSecond.
func uploadRequestTimeout(length int64) time.Duration {
	return uploadRequestBaseTimeout + time.Duration(length/uploadFloorBytesPerSecond)*time.Second
}

// redactedUploadURL drops the query string, which carries the signature.
func redactedUploadURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "(unparseable upload URL)"
	}
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

// countingReader reports bytes as the transport reads them, so progress
// follows the network rather than the disk. n is atomic because the
// transport's write loop can still be reading when Do returns an error.
type countingReader struct {
	r        io.Reader
	progress *uploadProgress
	n        atomic.Int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n.Add(int64(n))
	c.progress.add(int64(n))
	return n, err
}

// uploadProgress prints bytes sent / total to stderr for large uploads. On a
// terminal it redraws one line; otherwise (an AI coworker, a log file) it
// prints a line per quarter, so a captured transcript stays short.
type uploadProgress struct {
	w     io.Writer
	name  string
	total int64
	tty   bool

	mu        sync.Mutex
	sent      int64
	quarter   int64 // highest quarter printed (non-TTY)
	lastDraw  time.Time
	lineWidth int
}

// newUploadProgress returns nil (a no-op reporter) for uploads at or under
// progressThresholdBytes.
func newUploadProgress(w io.Writer, name string, total int64) *uploadProgress {
	if total <= progressThresholdBytes {
		return nil
	}
	return &uploadProgress{w: w, name: truncateString(name, 40), total: total, tty: isTerminalWriter(w)}
}

func isTerminalWriter(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && (isatty.IsTerminal(f.Fd()) || isatty.IsCygwinTerminal(f.Fd()))
}

// add records n more bytes sent; a negative n un-counts a failed attempt
// so a retried part is not counted twice.
func (p *uploadProgress) add(n int64) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sent += n
	if p.sent < 0 {
		p.sent = 0
	} else if p.sent > p.total {
		p.sent = p.total
	}

	if p.tty {
		// redraw at most five times a second, and always at the end
		if p.sent < p.total && time.Since(p.lastDraw) < 200*time.Millisecond {
			return
		}
		p.lastDraw = time.Now()
		line := p.line()
		pad := 0
		if p.lineWidth > len(line) {
			pad = p.lineWidth - len(line)
		}
		p.lineWidth = len(line)
		_, _ = fmt.Fprintf(p.w, "\r%s%s", line, strings.Repeat(" ", pad))
		return
	}

	if q := p.sent * 4 / p.total; q > p.quarter {
		p.quarter = q
		_, _ = fmt.Fprintln(p.w, p.line())
	}
}

func (p *uploadProgress) line() string {
	return fmt.Sprintf("Uploading %s: %s / %s (%d%%)", p.name, formatSize(p.sent), formatSize(p.total), p.sent*100/p.total)
}

// finish ends the terminal line so the next output starts on its own.
func (p *uploadProgress) finish() {
	if p == nil || !p.tty {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.lineWidth > 0 {
		_, _ = fmt.Fprintln(p.w)
	}
}
