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

	// mediaImportCacheSubdir holds one record per media import, keyed by
	// content hash, inside the team context's gitignored .sageox/cache/ — the
	// local-only store that survives the daemon's reclones.
	mediaImportCacheSubdir = ".sageox/cache/media-imports"
)

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

	storedToken, err := auth.GetTokenForEndpoint(m.ep)
	if err != nil {
		return fmt.Errorf("read auth store: %w", err)
	}
	if storedToken == nil || storedToken.AccessToken == "" {
		return errkind.Errorf(errkind.NotLoggedIn, "not authenticated — run 'ox login' first")
	}
	client := api.NewRepoClientWithEndpoint(m.ep).WithAuthToken(storedToken.AccessToken).WithTimeout(mediaAPITimeout)

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

	// a server that issued no upload_id predates the confirm body and needs none
	var confirmReq *api.ConfirmRecordingUploadRequest
	if upload.UploadID != "" {
		confirmReq = &api.ConfirmRecordingUploadRequest{UploadID: upload.UploadID, Parts: parts}
	}
	if _, err := client.ConfirmRecordingUpload(ctx, api.ContextTypeTeam, m.tc.TeamID, upload.RecordingID, confirmReq); err != nil {
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
