package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/lfs"
	"github.com/spf13/cobra"
)

// transcriptImportFormats maps the transcript files that become a Discussion
// on their own, the way audio does, to the source_format api-go's
// POST …/recordings/transcript accepts. Plain .txt stays on the document path:
// it is as often notes as a transcript.
var transcriptImportFormats = map[string]string{
	".vtt": "vtt",
	".srt": "srt",
}

// transcriptImportFormat returns the server's source_format for path, if path
// is a transcript that imports as a Discussion.
func transcriptImportFormat(path string) (string, bool) {
	format, ok := transcriptImportFormats[strings.ToLower(filepath.Ext(path))]
	return format, ok
}

// runImportTranscript creates a transcript-only Discussion through
// POST …/recordings/transcript. The text rides in the request body, so there
// is no upload or confirm. It returns api.ErrRecordingUploadUnsupported, having
// created nothing, when the server has no such route.
func runImportTranscript(ctx context.Context, cmd *cobra.Command, m mediaImport, format string) error {
	if lfs.IsPointerFile(m.srcPath) {
		return fmt.Errorf("%s: %w", m.srcPath, lfs.ErrPointerContent)
	}
	content, err := os.ReadFile(m.srcPath) //nolint:gosec // G304: the person's own import argument
	if err != nil {
		return fmt.Errorf("read transcript: %w", err)
	}
	if len(content) == 0 {
		return fmt.Errorf("transcript file is empty: %s", m.srcPath)
	}
	// JSON would silently replace invalid bytes with U+FFFD, so a UTF-16 export
	// would arrive as a page of replacement characters instead of an error
	if !utf8.Valid(content) {
		return fmt.Errorf("transcript is not UTF-8 text: %s", m.srcPath)
	}

	digest := sha256.Sum256(content)
	bareOID := hex.EncodeToString(digest[:])
	sourceOID := "sha256:" + bareOID

	// only a Discussion this route created counts: a document import of the
	// same bytes may never have become one, or never been pushed, and the
	// document path dedups (and resumes) its own imports on fallback
	if !importFlags.force {
		if rec, found := findMediaImportByOID(m.tc.Path, bareOID); found {
			return printAlreadyImported(cmd, m.jsonOutput, rec.RecordingID, rec.RecordingID)
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

	idempotencyKey, err := uuid.NewV7()
	if err != nil {
		return fmt.Errorf("mint idempotency key: %w", err)
	}
	resp, err := client.CreateTranscriptRecording(ctx, api.ContextTypeTeam, m.tc.TeamID, idempotencyKey.String(), &api.TranscriptRecordingRequest{
		Title:        title,
		Content:      string(content),
		SourceFormat: format,
		RecordedAt:   m.recordedAt,
	})
	if errors.Is(err, api.ErrRecordingUploadUnsupported) {
		return err
	}
	if err != nil {
		return fmt.Errorf("create transcript discussion: %w", err)
	}

	slog.InfoContext(ctx, "transcript import created",
		"recording_id", resp.ID, "team_id", m.tc.TeamID, "source_format", format, "size", len(content))

	// the Discussion exists on the server now; a record that fails to write
	// only costs dedup on a later import, so it must not fail this one
	rec := mediaImportRecord{
		SourceOID:      sourceOID,
		RecordingID:    resp.ID,
		Title:          title,
		SourceFilename: filename,
		ContentType:    contentType,
		SourceSize:     int64(len(content)),
		ImportedAt:     time.Now().UTC().Format(time.RFC3339),
	}
	if err := writeMediaImportRecord(m.tc.Path, rec); err != nil {
		slog.WarnContext(ctx, "transcript import record not written; a reimport will not be detected",
			"recording_id", resp.ID, "error", err)
	}

	if m.jsonOutput {
		return emitImportJSON(cmd, importResult{
			Status:      "imported",
			Title:       title,
			TeamID:      m.tc.TeamID,
			SourceOID:   sourceOID,
			ContentType: contentType,
			RecordingID: resp.ID,
		})
	}
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Imported: %s\nRecording: %s\n", title, resp.ID)
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "\n  Track progress: ox import --status %s --watch%s\n", resp.ID, importContextFlagHint())
	return nil
}
