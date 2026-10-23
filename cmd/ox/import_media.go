package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/errkind"
	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/internal/lfs"
	"github.com/spf13/cobra"
)

// mediaImportExtensions are the audio and video formats the recording upload
// route accepts (api-go mediaformats, ContextDirectUpload). The server checks
// the filename extension first, so any other file stays on the document path.
var mediaImportExtensions = map[string]bool{
	".m4a": true, ".mp3": true, ".wav": true, ".webm": true, ".ogg": true,
	".opus": true, ".flac": true, ".aac": true, ".wma": true,
	".mp4": true, ".mov": true, ".m4v": true,
}

const (
	// mediaAPITimeout covers the SageOx API calls around the upload. Confirm
	// completes a multipart upload and checks the stored object's size and
	// checksum before answering, which outlasts the client's 10s default on
	// big files.
	mediaAPITimeout = 2 * time.Minute

	// mediaTokenRefreshBuffer renews a sign-in with less than this many
	// seconds left, the buffer internal/auth recommends for API callers: it
	// covers the upload request and the first part URLs with room to spare.
	mediaTokenRefreshBuffer = 300

	// mediaImportCacheSubdir holds one record per media import, keyed by
	// content hash, inside the team context's gitignored .sageox/cache/ — the
	// local-only store that survives the daemon's reclones.
	mediaImportCacheSubdir = ".sageox/cache/media-imports"
)

// The wait after confirm answers 202 (received, still checking).
const (
	// mediaConfirmMinWait keeps a Retry-After of 0 from turning the wait
	// into a stream of back-to-back confirms.
	mediaConfirmMinWait = time.Second
	// mediaConfirmMaxWait caps a Retry-After so a wrong header cannot leave a
	// person watching a silent terminal for minutes between confirms.
	mediaConfirmMaxWait = 30 * time.Second
	// mediaConfirmDefaultWait is used when the 202 carries no Retry-After.
	mediaConfirmDefaultWait = 2 * time.Second
)

// mediaConfirmBudget bounds the whole wait, from the first 202: long enough
// for the check on a multi-gigabyte file (one confirm alone may take
// mediaAPITimeout), short enough that nobody waits without an answer. Past it
// the person is pointed at ox import --status. A var so tests can shorten it.
var mediaConfirmBudget = 5 * time.Minute

// mediaConfirmSleep waits between confirms and returns early with ctx's error
// on Ctrl-C. A var so tests do not sleep.
var mediaConfirmSleep = func(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// isMediaImportFile reports whether path is an audio or video file that
// imports through the recording upload route.
func isMediaImportFile(path string) bool {
	return mediaImportExtensions[strings.ToLower(filepath.Ext(path))]
}

// mediaImport is what the recording upload path needs from runImport.
type mediaImport struct {
	srcPath     string
	size        int64
	tc          *config.TeamContext
	ep          string
	docsBaseDir string     // legacy document imports, checked for duplicates too
	recordedAt  *time.Time // set only from --date
	jsonOutput  bool
}

// mediaImportRecord is the local record of one media import. Recording
// uploads leave no metadata.json in the team context, so this is what lets a
// second `ox import` of the same bytes answer "already imported" — the same
// source-OID dedup the document path gets from data/docs.
type mediaImportRecord struct {
	SourceOID      string `json:"source_oid"`
	RecordingID    string `json:"recording_id"`
	Title          string `json:"title"`
	SourceFilename string `json:"source_filename"`
	ContentType    string `json:"content_type"`
	SourceSize     int64  `json:"source_size"`
	ImportedAt     string `json:"imported_at"`
}

// runImportMedia uploads an audio or video file through
// POST …/recordings/upload → presigned PUT(s) → POST …/recordings/{id}/confirm.
// It returns api.ErrRecordingUploadUnsupported, having uploaded nothing, when
// the server has no upload route; the caller then uses the document path.
func runImportMedia(ctx context.Context, cmd *cobra.Command, m mediaImport) error {
	if m.size <= 0 {
		return fmt.Errorf("media file is empty: %s", m.srcPath)
	}
	if lfs.IsPointerFile(m.srcPath) {
		return fmt.Errorf("%s: %w", m.srcPath, lfs.ErrPointerContent)
	}

	digest, err := fileSHA256(m.srcPath, m.size)
	if err != nil {
		return err
	}
	bareOID := hex.EncodeToString(digest)
	sourceOID := "sha256:" + bareOID

	if !importFlags.force {
		if rec, found := findMediaImportByOID(m.tc.Path, bareOID); found {
			return printAlreadyImported(cmd, m.jsonOutput, rec.RecordingID, rec.RecordingID)
		}
		if existing, found := findExistingDocByOID(m.docsBaseDir, sourceOID); found {
			return printAlreadyImported(cmd, m.jsonOutput, existing, "")
		}
	}

	accessToken, err := mediaImportAccessToken(ctx, m.ep)
	if err != nil {
		return err
	}
	client := api.NewRepoClientWithEndpoint(m.ep).WithAuthToken(accessToken).WithTimeout(mediaAPITimeout)

	filename := filepath.Base(m.srcPath)
	title := resolveImportTitle(m.srcPath)
	contentType := detectContentType(filename, nil)

	// One key for this import: every re-presign repeats the same request under
	// it, which the server answers with the same upload and fresh URLs.
	idempotencyKey, err := uuid.NewV7()
	if err != nil {
		return fmt.Errorf("mint idempotency key: %w", err)
	}
	uploadReq := &api.RecordingUploadRequest{
		Filename:       filename,
		ContentType:    contentType,
		Size:           m.size,
		Title:          title,
		RecordedAt:     m.recordedAt,
		ContentHash:    bareOID,
		ChecksumSHA256: base64.StdEncoding.EncodeToString(digest),
		Multipart:      true,
	}
	requestUpload := func(ctx context.Context) (*api.RecordingUploadResponse, error) {
		return client.RequestRecordingUpload(ctx, api.ContextTypeTeam, m.tc.TeamID, idempotencyKey.String(), uploadReq)
	}

	upload, err := requestUpload(ctx)
	if errors.Is(err, api.ErrRecordingUploadUnsupported) {
		return err
	}
	if err != nil {
		return fmt.Errorf("start recording upload: %w", err)
	}

	presigner, err := newUploadPresigner(m.size, contentType, upload, requestUpload)
	if err != nil {
		return fmt.Errorf("recording %s: %w", upload.RecordingID, err)
	}

	slog.InfoContext(ctx, "media import upload starting",
		"recording_id", upload.RecordingID, "team_id", m.tc.TeamID, "upload_id", upload.UploadID,
		"size", m.size, "requests", presigner.spanCount(), "multipart", upload.Multipart != nil)

	progress := newUploadProgress(cmd.ErrOrStderr(), filename, m.size)
	parts, err := uploadRecordingSpans(ctx, m.srcPath, presigner, progress)
	progress.finish()
	if err != nil {
		return fmt.Errorf("upload recording %s: %w", upload.RecordingID, err)
	}

	// A big upload can outlast the sign-in it started with. WithAuthToken
	// changes the client in place and the part workers' re-presign shares it,
	// so the token is swapped only here, after every part has finished. A
	// refresh that fails now must not throw away the uploaded bytes while the
	// stored sign-in still has time left: confirm goes out with it.
	if fresh, err := mediaImportAccessToken(ctx, m.ep); err == nil {
		client.WithAuthToken(fresh)
	} else if current, _ := auth.GetTokenForEndpoint(m.ep); ctx.Err() == nil && current != nil && current.AccessToken != "" && !current.IsExpired(0) {
		slog.WarnContext(ctx, "sign-in refresh before confirm failed; confirming with the current token",
			"recording_id", upload.RecordingID, "error", err)
		client.WithAuthToken(current.AccessToken)
	} else {
		return err
	}

	// a server that issued no upload_id predates the confirm body and needs none
	var confirmReq *api.ConfirmRecordingUploadRequest
	if upload.UploadID != "" {
		confirmReq = &api.ConfirmRecordingUploadRequest{UploadID: upload.UploadID, Parts: parts}
	}
	confirm := func(ctx context.Context) (*api.ConfirmRecordingUploadResponse, error) {
		return client.ConfirmRecordingUpload(ctx, api.ContextTypeTeam, m.tc.TeamID, upload.RecordingID, confirmReq)
	}
	if err := confirmUntilAccepted(ctx, cmd.ErrOrStderr(), upload.RecordingID, confirm); err != nil {
		return fmt.Errorf("confirm recording %s: %w", upload.RecordingID, err)
	}

	slog.InfoContext(ctx, "media import confirmed",
		"recording_id", upload.RecordingID, "team_id", m.tc.TeamID, "upload_id", upload.UploadID, "size", m.size)

	// the recording exists on the server now; a record that fails to write only
	// costs dedup on a later import, so it must not fail this one
	rec := mediaImportRecord{
		SourceOID:      sourceOID,
		RecordingID:    upload.RecordingID,
		Title:          title,
		SourceFilename: filename,
		ContentType:    contentType,
		SourceSize:     m.size,
		ImportedAt:     time.Now().UTC().Format(time.RFC3339),
	}
	if err := writeMediaImportRecord(m.tc.Path, rec); err != nil {
		slog.WarnContext(ctx, "media import record not written; a reimport will not be detected",
			"recording_id", upload.RecordingID, "error", err)
	}

	if m.jsonOutput {
		return emitImportJSON(cmd, importResult{
			Status:      "imported",
			Title:       title,
			TeamID:      m.tc.TeamID,
			SourceOID:   sourceOID,
			ContentType: contentType,
			RecordingID: upload.RecordingID,
		})
	}
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Imported: %s\nRecording: %s\n", title, upload.RecordingID)
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "\n  Track progress: ox import --status %s --watch%s\n", upload.RecordingID, importContextFlagHint())
	return nil
}

// confirmUntilAccepted sends confirm and, while it answers 202 (received,
// still checking), waits and sends the same confirm again until it is
// accepted, refused, or mediaConfirmBudget has passed since the first 202.
// One line on errOut says the wait has begun; if the wait ends without an
// answer — budget or Ctrl-C — errOut also gets the --status command, since
// the upload itself was received.
func confirmUntilAccepted(ctx context.Context, errOut io.Writer, recordingID string, confirm func(context.Context) (*api.ConfirmRecordingUploadResponse, error)) error {
	resp, err := confirm(ctx)
	if err != nil {
		return err
	}
	if !resp.Pending {
		return nil
	}

	_, _ = fmt.Fprintln(errOut, "Upload received; the server is still checking it. Waiting for it to be accepted...")
	unanswered := func(err error) error {
		_, _ = fmt.Fprintf(errOut, "\n  Track progress: ox import --status %s --watch%s\n", recordingID, importContextFlagHint())
		return err
	}
	budgetErr := errkind.Errorf(errkind.Timeout, "upload received but not yet accepted after %s", mediaConfirmBudget)

	start := time.Now()
	// each confirm gets the normal call limit or the budget left, whichever
	// is shorter, so one slow confirm cannot stretch the wait past the budget
	budgetCtx, cancelBudget := context.WithDeadline(ctx, start.Add(mediaConfirmBudget))
	defer cancelBudget()
	var waited time.Duration
	for resp.Pending {
		wait := mediaConfirmWait(resp)
		// Two clocks, as in publishBulletinWithRetry: the waits taken (the
		// cap holds when tests replace the sleeper) and wall time (slow
		// confirms count too). Asking sooner than the server said only
		// spends a request, so a wait that does not fit ends the wait.
		if waited+wait > mediaConfirmBudget || time.Since(start)+wait > mediaConfirmBudget {
			return unanswered(budgetErr)
		}
		waited += wait
		if err := mediaConfirmSleep(ctx, wait); err != nil {
			return unanswered(err)
		}

		callCtx, cancel := context.WithTimeout(budgetCtx, mediaAPITimeout)
		resp, err = confirm(callCtx)
		cancel()
		switch {
		case err == nil:
		case ctx.Err() != nil:
			return unanswered(ctx.Err())
		case budgetCtx.Err() != nil:
			return unanswered(budgetErr)
		default:
			return err
		}
	}
	return nil
}

// mediaConfirmWait is how long to wait before confirming again: the 202's
// Retry-After within [mediaConfirmMinWait, mediaConfirmMaxWait], or
// mediaConfirmDefaultWait when it gave none.
func mediaConfirmWait(resp *api.ConfirmRecordingUploadResponse) time.Duration {
	switch {
	case !resp.HasRetryAfter:
		return mediaConfirmDefaultWait
	case resp.RetryAfter < mediaConfirmMinWait:
		return mediaConfirmMinWait
	case resp.RetryAfter > mediaConfirmMaxWait:
		return mediaConfirmMaxWait
	default:
		return resp.RetryAfter
	}
}

// mediaImportAccessToken returns the sign-in for ep, refreshed when it has run
// out or runs out within mediaTokenRefreshBuffer.
func mediaImportAccessToken(ctx context.Context, ep string) (string, error) {
	token, err := auth.EnsureValidTokenForEndpointContext(ctx, ep, mediaTokenRefreshBuffer)
	if err != nil {
		// a refresh cut short by Ctrl-C is a cancellation, not a lost sign-in
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		var refreshErr *auth.TokenRefreshError
		if errors.As(err, &refreshErr) {
			return "", errkind.Errorf(errkind.NotLoggedIn, "sign-in expired and could not be renewed (%v) — run 'ox login'", err)
		}
		return "", fmt.Errorf("load credentials: %w", err)
	}
	if token == nil || token.AccessToken == "" {
		return "", errkind.Errorf(errkind.NotLoggedIn, "not authenticated — run 'ox login' first")
	}
	return token.AccessToken, nil
}

// printAlreadyImported reports a duplicate import, matching the document path's output.
func printAlreadyImported(cmd *cobra.Command, jsonOutput bool, id, recordingID string) error {
	if jsonOutput {
		return emitImportJSON(cmd, importResult{Status: "already_imported", ID: id, RecordingID: recordingID})
	}
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Already imported (id: %s). Use --force to reimport.\n", id)
	return nil
}

// fileSHA256 streams path through SHA-256 and checks it read exactly size
// bytes: a file still being written would otherwise be hashed as one thing
// and uploaded as another.
func fileSHA256(path string, size int64) ([]byte, error) {
	f, err := os.Open(path) //nolint:gosec // G304: the person's own import argument
	if err != nil {
		return nil, fmt.Errorf("open media file: %w", err)
	}
	defer f.Close()

	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return nil, fmt.Errorf("hash media file: %w", err)
	}
	if n != size {
		return nil, fmt.Errorf("media file changed while reading (expected %d bytes, read %d): %s", size, n, path)
	}
	return h.Sum(nil), nil
}

// mediaImportRecordPath is the record for one content hash.
func mediaImportRecordPath(tcPath, bareOID string) string {
	return filepath.Join(tcPath, filepath.FromSlash(mediaImportCacheSubdir), bareOID+".json")
}

// findMediaImportByOID returns the local record of an earlier media import of
// the same bytes. An unreadable record reads as "not imported": the worst case
// is a duplicate recording, never a refused import.
func findMediaImportByOID(tcPath, bareOID string) (mediaImportRecord, bool) {
	var rec mediaImportRecord
	data, err := os.ReadFile(mediaImportRecordPath(tcPath, bareOID))
	if err != nil {
		return rec, false
	}
	if json.Unmarshal(data, &rec) != nil || rec.RecordingID == "" {
		return rec, false
	}
	return rec, true
}

// writeMediaImportRecord stores rec atomically under the team context cache.
func writeMediaImportRecord(tcPath string, rec mediaImportRecord) error {
	path := mediaImportRecordPath(tcPath, strings.TrimPrefix(rec.SourceOID, "sha256:"))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create media import cache: %w", err)
	}
	return fileutil.AtomicWriteJSON(path, rec, 0o644)
}
