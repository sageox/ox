package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/errkind"
	"github.com/sageox/ox/internal/gitserver"
	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/paths"
	"github.com/spf13/cobra"
)

type importFlagsT struct {
	text   string
	date   string
	force  bool
	team   string
	title  string
	status string
	watch  bool
	list   bool
}

var importFlags importFlagsT

var importCmd = &cobra.Command{
	Use:   "import <file|url>",
	Short: "Import a document, media file, or video URL into team context",
	Long: `Import a document, media file, or video URL for onboarding and knowledge sharing.

Imports are stored with LFS-backed content and git-tracked metadata in the
team context repo — including media files (mp4, mov, webm, m4a, mp3, wav, …),
which are transcribed and summarized server-side after import. Supports
Loom, Cap, and direct video URLs.

Team context is a conversation store; Knowledge Bubbles are Curator-authored
syntheses and are not an import target (ox ADR-028).

  ox import report.pdf --text extracted.md
  ox import report.pdf --title "Q2 Review"        # override the filename-derived title
  ox import notes.md --date 2026-01-15
  ox import ./standup.mp4                       # media file into the team (git-tracked, transcribed)
  ox import https://www.loom.com/share/abc123 --title "Architecture Review"
  ox import https://cap.link/abc123 --title "Sprint Retro"
  ox import --list                              # find import IDs
  ox import --status rec_01234567               # check processing once
  ox import --status rec_01234567 --watch       # wait until complete

The team is auto-discovered from the current repo. Use --team to override.

For behavioral rules (vs. documents), see 'ox guide team-rules' — rules
go in agents/rules/, not through ox import.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runImport,
}

func init() {
	importCmd.Flags().StringVar(&importFlags.text, "text", "", "path to pre-extracted text/markdown for indexing (optional)")
	importCmd.Flags().SetAnnotation("text", "cobra_annotation_flag_value_name", []string{"file"})
	importCmd.Flags().StringVar(&importFlags.date, "date", "", "date for filing (YYYY-MM-DD, default: auto-detect from metadata)")
	importCmd.Flags().BoolVar(&importFlags.force, "force", false, "re-import even if content hash already exists")
	importCmd.Flags().StringVar(&importFlags.team, "team", "", "team ID (or slug/name when inside a repo)")
	importCmd.Flags().StringVar(&importFlags.title, "title", "", "display title (defaults to filename for file imports)")
	importCmd.Flags().StringVar(&importFlags.status, "status", "", "check processing status of a URL import (use --list to find IDs)")
	importCmd.Flags().BoolVar(&importFlags.watch, "watch", false, "poll --status until processing completes or fails")
	importCmd.Flags().BoolVar(&importFlags.list, "list", false, "list imports and their processing status")
}

// docMeta is the metadata.json schema for imported documents.
//
// This manifest is a living document — the CLI creates it at import time with
// the source file and any client-provided sidecars (e.g., "text-extract").
// The SageOx server may later add or update server-generated sidecars such as
// "what-matters" (a cached summary of what's relevant to the team from this
// document). Server-side sidecars are versioned through normal git commits, so
// the history of how a document's relevance evolves over time is preserved.
type docMeta struct {
	Version        string             `json:"version"`
	Title          string             `json:"title"`
	SourceFilename string             `json:"source_filename"`
	ContentType    string             `json:"content_type"`
	SourceSize     int64              `json:"source_size"`
	SourceOID      string             `json:"source_oid"`
	CreatedAt      string             `json:"created_at"`
	ImportedAt     string             `json:"imported_at"`
	Path           string             `json:"path"`
	Sidecars       map[string]sidecar `json:"sidecars"`
}

// sidecar describes an additional derived file associated with an imported document.
// The map key in docMeta.Sidecars is the sidecar type.
//
// Client-created sidecars (at import time):
//   - "text-extract" — pre-extracted text/markdown for indexing
//
// Server-generated sidecars (added/updated post-import):
//   - "what-matters" — a short summary of what's relevant to the team from this
//     document. Periodically re-summarized as team context evolves, so it may be
//     recommitted over time. Git history preserves how the document's relevance
//     changes until it no longer matters at all.
type sidecar struct {
	Filename  string `json:"filename"`
	OID       string `json:"oid"`
	Size      int64  `json:"size"`
	CreatedAt string `json:"created_at"`
}

// importResult is the --json payload for a team document import. recording_id is
// populated only when the server routes the file to transcription and returns an
// ID (media files); it can be fed to `ox import --status <id> --watch`.
type importResult struct {
	Status      string `json:"status"` // "imported" | "already_imported"
	Title       string `json:"title,omitempty"`
	Path        string `json:"path,omitempty"`
	ID          string `json:"id,omitempty"` // existing doc id on already_imported
	TeamID      string `json:"team_id,omitempty"`
	SourceOID   string `json:"source_oid,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	RecordingID string `json:"recording_id,omitempty"`
}

// runImport uploads a document to the team context's LFS store and commits its pointer files.
func runImport(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	jsonOutput, _ := cmd.Flags().GetBool("json")

	// dispatch: --status flag takes priority
	if importFlags.status != "" {
		return runImportStatus(cmd, jsonOutput)
	}

	// dispatch: --list flag
	if importFlags.list {
		return runImportList(cmd, jsonOutput)
	}

	// require an argument for file/URL import
	if len(args) == 0 {
		return fmt.Errorf("requires a file path or URL argument")
	}

	srcPath := args[0]

	// dispatch: URL import
	if strings.HasPrefix(srcPath, "http://") || strings.HasPrefix(srcPath, "https://") {
		return runImportURL(cmd, srcPath, jsonOutput)
	}

	srcInfo, err := os.Stat(srcPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("file not found: %s", srcPath)
		}
		return fmt.Errorf("stat source file: %w", err)
	}
	if srcInfo.IsDir() {
		return fmt.Errorf("source must be a file, not a directory: %s", srcPath)
	}

	// resolve date: --date flag > file mtime > today
	var importDate time.Time
	if importFlags.date != "" {
		importDate, err = time.Parse("2006-01-02", importFlags.date)
		if err != nil {
			return fmt.Errorf("invalid --date format (expected YYYY-MM-DD): %s", importFlags.date)
		}
	} else {
		importDate = srcInfo.ModTime()
		if importDate.IsZero() {
			importDate = time.Now().UTC()
		}
	}

	// projectRoot is optional — import works outside a repo when --team is given
	projectRoot, _ := findProjectRoot()

	// resolve endpoint once, used consistently for team resolution, LFS, and push
	ep := resolveImportEndpoint(projectRoot)

	tc, err := resolveImportTeam(projectRoot, ep)
	if err != nil {
		return err
	}

	// data/ is excluded from the team context sparse checkout (deny list in
	// sync.manifest). We create the directory ourselves and git add stages
	// files outside the sparse cone. After commit+push, these local files are
	// ephemeral — the daemon's next reclone (gc_interval_days, default 7d)
	// produces a fresh sparse checkout that omits data/ entirely. The actual
	// document content lives on the LFS server; only pointer files and
	// metadata.json are in git history.
	docsBaseDir := filepath.Join(tc.Path, "data", "docs")
	if err := os.MkdirAll(docsBaseDir, 0o755); err != nil {
		return fmt.Errorf("create data/docs directory: %w", err)
	}

	srcContent, err := os.ReadFile(srcPath)
	if err != nil {
		return fmt.Errorf("read source file: %w", err)
	}

	srcRef := lfs.NewFileRef(srcContent)

	// dedup: skip if this exact content was already imported
	if !importFlags.force {
		if existing, found := findExistingDocByOID(docsBaseDir, srcRef.OID); found {
			meta, resumed, err := resumeDocImport(ctx, tc.Path, ep, existing, srcRef)
			if err != nil {
				return fmt.Errorf("resume import: %w", err)
			}
			if resumed {
				return finishDocImport(cmd, jsonOutput, tc.TeamID, ep, meta)
			}
			docID := filepath.Base(filepath.Dir(existing))
			if jsonOutput {
				return emitImportJSON(cmd, importResult{Status: "already_imported", ID: docID})
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Already imported (id: %s). Use --force to reimport.\n", docID)
			return nil
		}
	}

	// derive the per-document storage slug from --title, falling back to the
	// filename stem (see resolveImportSlug for the empty-slug guard)
	dirSlug := resolveImportSlug(srcPath)

	docDir := filepath.Join(docsBaseDir,
		importDate.Format("2006"),
		importDate.Format("01"),
		importDate.Format("02"),
		dirSlug,
	)
	if _, statErr := os.Stat(docDir); statErr == nil && !importFlags.force {
		return fmt.Errorf("document directory already exists for this date — use --force to reimport: %s", docDir)
	}

	// prepare LFS batch objects
	batchObjects := []lfs.BatchObject{
		{OID: srcRef.BareOID(), Size: srcRef.Size},
	}
	fileContents := map[string][]byte{
		srcRef.BareOID(): srcContent,
	}

	var textRef lfs.FileRef
	hasText := false
	if importFlags.text != "" {
		textContent, err := os.ReadFile(importFlags.text)
		if err != nil {
			if os.IsNotExist(err) {
				return fmt.Errorf("--text file not found: %s", importFlags.text)
			}
			return fmt.Errorf("read text file: %w", err)
		}
		textRef = lfs.NewFileRef(textContent)
		hasText = true

		batchObjects = append(batchObjects, lfs.BatchObject{OID: textRef.BareOID(), Size: textRef.Size})
		fileContents[textRef.BareOID()] = textContent
	}

	// upload content to LFS
	lfsClient, err := getTeamContextLFSClient(ctx, ep, tc)
	if err != nil {
		return fmt.Errorf("create LFS client: %w", err)
	}

	slog.Info("uploading doc to LFS", "doc", dirSlug, "files", len(batchObjects))

	resp, err := lfsClient.BatchUploadContext(ctx, batchObjects)
	if err != nil {
		return fmt.Errorf("LFS batch upload: %w", err)
	}

	results := lfs.UploadAll(resp, fileContents, 4)
	var uploadErrors []string
	for _, r := range results {
		if r.Error != nil {
			uploadErrors = append(uploadErrors, fmt.Sprintf("OID %s: %s", r.OID, r.Error))
		}
	}
	if len(uploadErrors) > 0 {
		return fmt.Errorf("LFS upload failed:\n  %s", strings.Join(uploadErrors, "\n  "))
	}

	// created only after the upload succeeds, so a failed upload leaves nothing that blocks a retry;
	// Mkdir, not MkdirAll, so an import that created docDir during our upload is not overwritten without --force
	err = os.MkdirAll(filepath.Dir(docDir), 0o755)
	if err == nil {
		err = os.Mkdir(docDir, 0o755)
	}
	if errors.Is(err, fs.ErrExist) && importFlags.force {
		err = nil
	}
	if errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("document directory was created by another import while this one was uploading, use --force to reimport: %s", docDir)
	}
	if err != nil {
		return fmt.Errorf("create doc directory: %w", err)
	}

	// write LFS pointer files (~200 bytes each, referencing content on LFS server).
	// these are committed to git and survive in history, but the local working-tree
	// copies are cleaned up on the next sparse-checkout reclone (data/ is denied).
	srcFilename := filepath.Base(srcPath)
	srcPointerPath := filepath.Join(docDir, srcFilename)
	textPointerPath := filepath.Join(docDir, "extracted.md")
	pointerFiles := map[string]lfs.FileRef{srcFilename: srcRef}
	if hasText {
		pointerFiles["extracted.md"] = textRef
	}
	// AssertUploaded: srcRef/textRef blobs were uploaded via BatchUpload/UploadAll above.
	if _, err := lfs.WritePointerFiles(docDir, lfs.AssertUploadedManifest(pointerFiles)); err != nil {
		return fmt.Errorf("write pointer files: %w", err)
	}

	title := resolveImportTitle(srcPath)

	// build and write metadata.json (plain git, not LFS — stays readable without
	// hydration). like pointer files, the local copy is ephemeral and cleaned up
	// on sparse-checkout reclone, but persists in git history.
	// sidecars only includes additional derived files (source is described by top-level fields)
	sidecars := map[string]sidecar{}
	if hasText {
		sidecars["text-extract"] = sidecar{
			Filename:  "extracted.md",
			OID:       textRef.OID,
			Size:      textRef.Size,
			CreatedAt: time.Now().UTC().Format(time.RFC3339),
		}
	}

	// relative path within team context (for cloud notification)
	relDocDir, _ := filepath.Rel(tc.Path, docDir)

	meta := docMeta{
		Version:        "1",
		Title:          title,
		SourceFilename: srcFilename,
		ContentType:    detectContentType(srcFilename, srcContent),
		SourceSize:     srcRef.Size,
		SourceOID:      srcRef.OID,
		CreatedAt:      importDate.Format(time.RFC3339),
		ImportedAt:     time.Now().UTC().Format(time.RFC3339),
		Path:           relDocDir,
		Sidecars:       sidecars,
	}

	metaData, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal metadata: %w", err)
	}
	metaPath := filepath.Join(docDir, "metadata.json")
	if err := os.WriteFile(metaPath, metaData, 0o644); err != nil {
		return fmt.Errorf("write metadata.json: %w", err)
	}

	if err := commitAndPushDocImport(tc.Path, ep, dirSlug, metaPath, srcPointerPath, textPointerPath, hasText); err != nil {
		return fmt.Errorf("commit and push: %w", err)
	}

	return finishDocImport(cmd, jsonOutput, tc.TeamID, ep, meta)
}

// finishDocImport reports a published import using its saved manifest.
func finishDocImport(cmd *cobra.Command, jsonOutput bool, teamID, ep string, meta docMeta) error {
	// cloud notification — uses team_id since imports target team contexts, not
	// project repos. Returns a recording ID when the server routes the file to
	// transcription; failures degrade silently and never fail the import.
	recordingID := notifyImport(teamID, ep, meta)

	if jsonOutput {
		return emitImportJSON(cmd, importResult{
			Status:      "imported",
			Title:       meta.Title,
			Path:        meta.Path,
			TeamID:      teamID,
			SourceOID:   meta.SourceOID,
			ContentType: meta.ContentType,
			RecordingID: recordingID,
		})
	}

	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Imported: %s\nPath: %s\n", meta.Title, meta.Path)
	if recordingID != "" {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "\n  Track progress: ox import --status %s --watch%s\n", recordingID, importContextFlagHint())
	}
	return nil
}

// emitImportJSON writes an importResult as indented JSON to stdout.
func emitImportJSON(cmd *cobra.Command, result importResult) error {
	return cli.PrintJSONTo(cmd.OutOrStdout(), result)
}

// resolveImportTitle returns the explicit --title when set, otherwise the
// title inferred from the filename. It backs both the metadata.json/server
// display title and (via resolveImportSlug) the per-document storage slug.
func resolveImportTitle(srcPath string) string {
	if t := strings.TrimSpace(importFlags.title); t != "" {
		return t
	}
	return inferTitle(srcPath)
}

// resolveImportSlug derives the per-document storage slug for the team import
// path. It prefers the slugified --title, but a punctuation-only title (e.g.
// "!!!") slugifies to "" — which would collapse docDir onto the date directory
// and silently scatter metadata.json and pointer files there. In that case it
// falls back to the filename-derived slug so storage identity always lives
// under a per-document directory. (A pathological filename like "!!!.pdf" can
// still produce an empty slug; that edge predates the --title feature and is
// out of scope here.)
func resolveImportSlug(srcPath string) string {
	if s := slugify(resolveImportTitle(srcPath)); s != "" {
		return s
	}
	return slugify(inferTitle(srcPath))
}

// inferTitle derives a human-readable title from a filename.
// Strips extension, replaces hyphens and underscores with spaces.
func inferTitle(path string) string {
	base := filepath.Base(path)
	title := strings.TrimSuffix(base, filepath.Ext(base))
	title = strings.ReplaceAll(title, "-", " ")
	title = strings.ReplaceAll(title, "_", " ")
	return title
}

// ensureMetadataGitattributes ensures metadata.json is excluded from LFS.
// The data/** LFS rule covers source files and extracted.md, but metadata.json
// must remain a plain-text git object so AI coworkers can read it without hydration.
func ensureMetadataGitattributes(tcPath string) error {
	gitattrsPath := filepath.Join(tcPath, ".gitattributes")

	content, err := os.ReadFile(gitattrsPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read .gitattributes: %w", err)
	}

	newContent := docImportAttributes(content)
	if bytes.Equal(content, newContent) {
		return nil
	}
	return os.WriteFile(gitattrsPath, newContent, 0o644)
}

// docImportAttributes adds only the metadata override owned by document imports.
func docImportAttributes(content []byte) []byte {
	const marker = "data/**/metadata.json"
	const override = "data/**/metadata.json !filter !diff !merge text"
	if strings.Contains(string(content), marker) {
		return content
	}
	existing := strings.TrimRight(string(content), "\n")
	if existing != "" {
		existing += "\n"
	}
	return []byte(existing + override + "\n")
}

// findExistingDocByOID scans data/docs/ metadata.json files for a matching source OID.
// Returns the metadata path if found, preserving the document's original location.
func findExistingDocByOID(docsBaseDir, oid string) (string, bool) {
	var metaPath string
	var found bool

	_ = filepath.WalkDir(docsBaseDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Name() != "metadata.json" {
			return nil
		}
		data, readErr := os.ReadFile(path) //nolint:gosec // G122 - path comes from controlled walkDir within validated import directory
		if readErr != nil {
			return nil
		}
		var meta struct {
			SourceOID string `json:"source_oid"`
		}
		if json.Unmarshal(data, &meta) != nil {
			return nil
		}
		if meta.SourceOID == oid {
			metaPath = path
			found = true
			return filepath.SkipAll
		}
		return nil
	})

	return metaPath, found
}

// resumeDocImport validates and publishes a complete import left behind by a failed commit or push.
func resumeDocImport(ctx context.Context, tcPath, ep, metaPath string, source lfs.FileRef) (meta docMeta, resumed bool, err error) {
	var files map[string][]byte
	err = gitutil.WithRepoLock(ctx, tcPath, func() error {
		var readErr error
		files, readErr = readDocImport(ctx, tcPath, metaPath, source, &meta)
		if readErr != nil {
			return readErr
		}
		tracked := 0
		for path, content := range files {
			head, headExists, err := docImportGitBlob(ctx, tcPath, "HEAD", path)
			if err != nil {
				return err
			}
			if headExists {
				matches, err := docImportMatchesBlob(ctx, tcPath, path, content, head)
				if err != nil {
					return err
				}
				if !matches {
					return fmt.Errorf("document differs from HEAD; leaving it unchanged: %s", path)
				}
				tracked++
			}
			index, found, err := docImportGitBlob(ctx, tcPath, "", path)
			if err != nil {
				return err
			}
			indexMatches := !headExists && !found
			if found {
				indexMatches, err = docImportMatchesBlob(ctx, tcPath, path, content, index)
				if err != nil {
					return err
				}
			}
			if !indexMatches {
				return fmt.Errorf("document has different staged content; leaving it unchanged: %s", path)
			}
		}
		if tracked != 0 && tracked != len(files) {
			return fmt.Errorf("document is only partially committed; leaving it unchanged: %s", metaPath)
		}
		published, err := docImportPublished(ctx, tcPath, files, tracked == 0)
		if err != nil {
			return err
		}
		resumed = !published
		if published {
			return nil
		}
		if tracked == 0 {
			// Snapshot preparation adds attributes; publication checks use only document files.
			if err := commitDocImportSnapshot(ctx, tcPath, filepath.Base(filepath.Dir(metaPath)), maps.Clone(files)); err != nil {
				return err
			}
		}
		// One attempt with no reconciliation callbacks cannot enter the helper's locking retry path.
		// Keep the ownership check and push under this lock so other ox writers cannot replace HEAD.
		if err := pushTeamContext(ctx, tcPath, ep, 1); err != nil {
			return fmt.Errorf("saved import commit remains local; could not confirm publication; synchronize the team context, then rerun ox import: %w", err)
		}
		for path, expected := range files {
			content, found, err := docImportGitBlob(ctx, tcPath, "HEAD", path)
			if err != nil {
				return err
			}
			matches := false
			if found {
				matches, err = docImportMatchesBlob(ctx, tcPath, path, expected, content)
				if err != nil {
					return err
				}
			}
			if !matches {
				return fmt.Errorf("document changed while publishing; cannot report a successful import: %s", path)
			}
		}
		return nil
	})
	return meta, resumed, err
}

// docImportPublished checks the native push tracking ref for a previously published document.
// It does not query the remote; an unknown or ambiguous tracking state cannot authorize a push.
func docImportPublished(ctx context.Context, tcPath string, files map[string][]byte, needsCommit bool) (bool, error) {
	branch, err := gitutil.RunGit(ctx, tcPath, "symbolic-ref", "--quiet", "HEAD")
	if err != nil {
		return false, fmt.Errorf("cannot determine import publication: %w", err)
	}
	remote, err := gitutil.RunGit(ctx, tcPath, "for-each-ref", "--format=%(push:remotename)", branch)
	if err != nil {
		return false, fmt.Errorf("cannot determine import publication remote: %w", err)
	}
	if remote == "" {
		return false, fmt.Errorf("cannot determine import publication: push remote is unknown")
	}
	if _, configured, err := docImportGitConfig(ctx, tcPath, "--get-all", "remote."+remote+".push"); err != nil {
		return false, err
	} else if configured {
		return false, fmt.Errorf("cannot determine import publication with an explicit push refspec")
	}
	if mirror, _, err := docImportGitConfig(ctx, tcPath, "--bool", "--get", "remote."+remote+".mirror"); err != nil {
		return false, err
	} else if mirror == "true" {
		return false, fmt.Errorf("cannot determine import publication for a mirror remote")
	}
	if mode, configured, err := docImportGitConfig(ctx, tcPath, "--get", "push.default"); err != nil {
		return false, err
	} else if configured && mode != "simple" && mode != "current" && mode != "upstream" && mode != "tracking" {
		return false, fmt.Errorf("cannot determine import publication for push.default=%s", mode)
	}
	pushRef, err := gitutil.RunGit(ctx, tcPath, "rev-parse", "--symbolic-full-name", "@{push}")
	if err != nil {
		return false, fmt.Errorf("cannot determine import publication ref: %w", err)
	}
	if !strings.HasPrefix(pushRef, "refs/remotes/"+remote+"/") {
		return false, fmt.Errorf("cannot determine import publication: push tracking ref is unknown")
	}

	var urls [2]string
	for i, flags := range [][]string{{"--all"}, {"--push", "--all"}} {
		args := append([]string{"-C", tcPath, "remote", "get-url"}, flags...)
		args = append(args, remote)
		cmd := gitutil.NewNetworkCmd(ctx, args...)
		cmd.Dir = tcPath
		out, err := cmd.Output()
		if err != nil {
			return false, fmt.Errorf("cannot determine import publication URL: %w", err)
		}
		urls[i] = strings.TrimSpace(string(out))
		if urls[i] == "" || strings.Contains(urls[i], "\n") {
			return false, fmt.Errorf("cannot determine import publication with multiple remote URLs")
		}
	}
	if urls[0] != urls[1] {
		return false, fmt.Errorf("cannot determine import publication when fetch and push URLs differ")
	}

	matched := 0
	for path, expected := range files {
		content, found, err := docImportGitBlob(ctx, tcPath, pushRef, path)
		if err != nil {
			return false, fmt.Errorf("cannot determine import publication: %w", err)
		}
		if found {
			matches, err := docImportMatchesBlob(ctx, tcPath, path, expected, content)
			if err != nil {
				return false, err
			}
			if !matches {
				return false, fmt.Errorf("published document differs from the saved import; leaving it unchanged: %s", path)
			}
			matched++
		}
	}
	if matched != 0 && matched != len(files) {
		return false, fmt.Errorf("published document is incomplete; leaving it unchanged")
	}
	if matched == len(files) {
		return true, nil
	}

	// A previously published import can be absent after an upstream rewrite.
	// Check the history Git still knows before a fast-forward push can revive it.
	forkPoint, err := gitutil.RunGit(ctx, tcPath, "merge-base", "--fork-point", pushRef, "HEAD")
	if err != nil {
		return false, fmt.Errorf("cannot determine safe import retry history: %w", err)
	}
	for path := range files {
		_, found, err := docImportGitBlob(ctx, tcPath, forkPoint, path)
		if err != nil {
			return false, fmt.Errorf("cannot determine safe import retry history: %w", err)
		}
		if found {
			return false, fmt.Errorf("document was previously published and removed; leaving it unchanged: %s", path)
		}
	}

	// A branch push sends every ancestor, even when private files were later deleted.
	// Resume only a single non-merge import commit based directly on the known fork point.
	// Count from the current push ref so an upstream rewind cannot hide removed history.
	outgoing, err := gitutil.RunGit(ctx, tcPath, "rev-list", "--parents", "--max-count=2", pushRef+"..HEAD")
	if err != nil {
		return false, fmt.Errorf("cannot determine safe import retry history: %w", err)
	}
	commits := strings.Fields(outgoing)
	if needsCommit && len(commits) == 0 {
		return false, nil
	}
	if needsCommit || len(commits) != 2 || commits[1] != forkPoint {
		return false, fmt.Errorf("saved import remains local; outgoing history includes other commits or a merge; review outgoing commits before synchronizing the team context, then rerun ox import")
	}
	if err := docImportCommitOwned(ctx, tcPath, forkPoint, commits[0], files); err != nil {
		return false, err
	}
	return false, nil
}

// docImportCommitOwned accepts only validated document files and the metadata attributes override.
func docImportCommitOwned(ctx context.Context, tcPath, parent, commit string, files map[string][]byte) error {
	// Paths are NUL-delimited data; logging helpers can trim or sanitize valid filename bytes.
	changed, err := exec.CommandContext(ctx, "git", "-C", tcPath, "diff", "--name-only", "--no-renames", "-z", parent, commit, "--").Output()
	if err != nil {
		return fmt.Errorf("inspect import commit: %w", err)
	}
	for _, name := range bytes.Split(bytes.TrimSuffix(changed, []byte{0}), []byte{0}) {
		rel := string(name)
		path := filepath.Join(tcPath, filepath.FromSlash(rel))
		if _, owned := files[path]; !owned && rel != ".gitattributes" {
			return fmt.Errorf("saved import remains local; commit includes changes outside this document; review outgoing commits before synchronizing the team context, then rerun ox import")
		}
		mode, err := gitutil.RunGit(ctx, tcPath, "ls-tree", "--format=%(objectmode)", commit, "--", ":(literal)"+rel)
		if err != nil {
			return fmt.Errorf("inspect import commit file mode: %w", err)
		}
		if mode != "100644" && mode != "100755" {
			return fmt.Errorf("saved import commit contains a non-regular file; leaving it unchanged")
		}
		if rel == ".gitattributes" {
			before, _, err := docImportGitBlob(ctx, tcPath, parent, path)
			if err != nil {
				return err
			}
			after, found, err := docImportGitBlob(ctx, tcPath, commit, path)
			if err != nil {
				return err
			}
			if !found || !bytes.Equal(after, docImportAttributes(before)) {
				return fmt.Errorf("saved import remains local; commit includes unrelated .gitattributes changes; review outgoing commits before synchronizing the team context, then rerun ox import")
			}
		}
	}
	return nil
}

// docImportGitConfig treats only Git's missing-value exit code as an absent setting.
func docImportGitConfig(ctx context.Context, tcPath string, args ...string) (string, bool, error) {
	out, err := gitutil.RunGit(ctx, tcPath, append([]string{"config"}, args...)...)
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("cannot determine import publication configuration: %w", err)
	}
	return out, true, nil
}

// readDocImport accepts only a complete manifest and the pointers it describes.
func readDocImport(ctx context.Context, tcPath, metaPath string, source lfs.FileRef, meta *docMeta) (map[string][]byte, error) {
	data, err := readDocImportFile(tcPath, metaPath)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, meta); err != nil {
		return nil, fmt.Errorf("read import manifest: %w", err)
	}
	docDir := filepath.Dir(metaPath)
	relDocDir, err := filepath.Rel(tcPath, docDir)
	if err != nil || filepath.Clean(meta.Path) != relDocDir || meta.Version != "1" || meta.SourceOID != source.OID || meta.SourceSize != source.Size {
		return nil, fmt.Errorf("import manifest does not match the source and document path: %s", metaPath)
	}
	refs := map[string]lfs.FileRef{meta.SourceFilename: source}
	for _, sidecar := range meta.Sidecars {
		if _, exists := refs[sidecar.Filename]; exists {
			return nil, fmt.Errorf("import manifest repeats a pointer filename: %s", sidecar.Filename)
		}
		refs[sidecar.Filename] = lfs.FileRef{OID: sidecar.OID, Size: sidecar.Size}
	}
	files := map[string][]byte{metaPath: data}
	for filename, ref := range refs {
		if filename == "" || filename == "." || filename == ".." || filename == "metadata.json" || strings.ContainsAny(filename, `/\`) {
			return nil, fmt.Errorf("unsafe import pointer filename: %q", filename)
		}
		path := filepath.Join(docDir, filename)
		content, err := readDocImportFile(tcPath, path)
		if err != nil {
			return nil, err
		}
		pointer := lfs.FormatPointer(ref.OID, ref.Size)
		oid, size, err := lfs.ParsePointer(pointer)
		if err != nil || oid != ref.OID || size != ref.Size {
			return nil, fmt.Errorf("import pointer does not match its manifest: %s", path)
		}
		matches, err := docImportMatchesBlob(ctx, tcPath, path, content, []byte(pointer))
		if err != nil {
			return nil, err
		}
		if !matches {
			return nil, fmt.Errorf("import pointer does not match its manifest: %s", path)
		}
		files[path] = content
	}
	return files, nil
}

// readDocImportFile refuses paths that traverse a symlink or leave the clone.
func readDocImportFile(tcPath, path string) ([]byte, error) {
	rel, err := filepath.Rel(tcPath, path)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return nil, fmt.Errorf("unsafe import file path: %s", path)
	}
	current := tcPath
	parts := strings.Split(rel, string(filepath.Separator))
	for i, part := range parts {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return nil, fmt.Errorf("read import file: %w", err)
		}
		if (i < len(parts)-1 && !info.IsDir()) || (i == len(parts)-1 && !info.Mode().IsRegular()) {
			return nil, fmt.Errorf("import path is not a regular file or directory: %s", current)
		}
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read import file: %w", err)
	}
	return content, nil
}

// docImportMatchesBlob compares Git-clean file content to a canonical blob without writing objects or checkout bytes.
func docImportMatchesBlob(ctx context.Context, tcPath, path string, content, blob []byte) (bool, error) {
	rel, err := filepath.Rel(tcPath, path)
	if err != nil {
		return false, err
	}
	var oids [2]string
	for i, input := range [][]byte{content, blob} {
		args := []string{"-C", tcPath, "hash-object", "--stdin"}
		if i == 0 {
			args = append(args, "--path="+filepath.ToSlash(rel))
		} else {
			args = append(args, "--no-filters")
		}
		cmd := gitutil.NewNetworkCmd(ctx, args...)
		cmd.Dir = tcPath
		cmd.Stdin = bytes.NewReader(input)
		out, err := cmd.Output()
		if err != nil {
			return false, fmt.Errorf("compare import content using Git: %w", err)
		}
		oids[i] = strings.TrimSpace(string(out))
	}
	return oids[0] == oids[1], nil
}

// docImportGitBlob distinguishes an absent path from an unreadable Git state.
func docImportGitBlob(ctx context.Context, tcPath, revision, path string) ([]byte, bool, error) {
	rel, err := filepath.Rel(tcPath, path)
	if err != nil {
		return nil, false, err
	}
	rel = filepath.ToSlash(rel)
	args := []string{"ls-tree", "-z", revision, "--", ":(literal)" + rel}
	if revision == "" {
		args = []string{"ls-files", "--stage", "-z", "--", ":(literal)" + rel}
	}
	out, err := gitutil.RunGit(ctx, tcPath, args...)
	if err != nil {
		return nil, false, fmt.Errorf("inspect import Git state: %w", err)
	}
	if out == "" {
		return nil, false, nil
	}
	cmd := exec.CommandContext(ctx, "git", "-C", tcPath, "show", revision+":"+rel)
	content, err := cmd.Output()
	if err != nil {
		return nil, false, fmt.Errorf("read import Git blob %s: %w", path, err)
	}
	return content, true, nil
}

// detectContentType returns the MIME type for a file.
// Uses extension mapping first, falls back to http.DetectContentType.
func detectContentType(filename string, content []byte) string {
	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	case ".pdf":
		return "application/pdf"
	case ".docx":
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	case ".md", ".markdown":
		return "text/markdown"
	case ".txt":
		return "text/plain"
	case ".html", ".htm":
		return "text/html"
	case ".json":
		return "application/json"
	case ".yaml", ".yml":
		return "application/x-yaml"
	case ".csv":
		return "text/csv"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	// audio formats
	case ".m4a":
		return "audio/mp4"
	case ".mp3":
		return "audio/mpeg"
	case ".wav":
		return "audio/wav"
	case ".ogg":
		return "audio/ogg"
	case ".opus":
		return "audio/opus"
	case ".flac":
		return "audio/flac"
	case ".aac":
		return "audio/aac"
	case ".wma":
		return "audio/x-ms-wma"
	case ".webm":
		return "audio/webm"
	// video formats
	case ".mp4":
		return "video/mp4"
	case ".mov":
		return "video/quicktime"
	case ".mkv":
		return "video/x-matroska"
	case ".avi":
		return "video/x-msvideo"
	}
	sniffLen := len(content)
	if sniffLen > 512 {
		sniffLen = 512
	}
	return http.DetectContentType(content[:sniffLen])
}

// resolveImportEndpoint returns the SageOx endpoint, preferring project config when available.
func resolveImportEndpoint(projectRoot string) string {
	if projectRoot != "" {
		if ep := endpoint.GetForProject(projectRoot); ep != "" {
			return ep
		}
	}
	return endpoint.Get()
}

// autoDiscoverSingleTeam returns the team context if exactly one team is synced
// locally. Returns nil if zero or multiple teams are found.
func autoDiscoverSingleTeam(ep string) *config.TeamContext {
	if ep == "" {
		return nil
	}

	teamsDir := paths.TeamsDataDir(ep)
	entries, err := os.ReadDir(teamsDir)
	if err != nil {
		return nil
	}

	var dirs []os.DirEntry
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, e)
		}
	}
	if len(dirs) != 1 {
		return nil
	}

	return &config.TeamContext{
		TeamID: dirs[0].Name(),
		Path:   filepath.Join(teamsDir, dirs[0].Name()),
	}
}

// resolveTeamContextByEndpoint finds a team context by team ID using only
// the endpoint (no project root required). Scans the teams data directory.
// Only matches by team ID since directory names are team IDs — slug/name
// metadata is not available from the filesystem scan alone.
func resolveTeamContextByEndpoint(query, ep string) *config.TeamContext {
	if ep == "" {
		return nil
	}

	teamsDir := paths.TeamsDataDir(ep)
	teamPath := filepath.Join(teamsDir, query)
	if info, err := os.Stat(teamPath); err == nil && info.IsDir() {
		return &config.TeamContext{
			TeamID: query,
			Path:   teamPath,
		}
	}

	return nil
}

// getTeamContextLFSClient creates an LFS client for the team context repo.
// Fallback chain: cloud API → cached marker → git remote URL.
func getTeamContextLFSClient(ctx context.Context, ep string, tc *config.TeamContext) (*lfs.Client, error) {

	repoURL := GetTeamURLWithFallback("", tc.TeamID, ep)
	if repoURL == "" {
		// last resort: read from local git remote
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		out, gitErr := gitutil.RunGit(ctx, tc.Path, "remote", "get-url", "origin")
		if gitErr != nil || strings.TrimSpace(out) == "" {
			return nil, fmt.Errorf("no team context repo URL found (API and git remote both failed)")
		}
		repoURL = strings.TrimSpace(out)
	}

	return lfs.NewClientForEndpoint(ctx, repoURL, ep)
}

// commitAndPushDocImport stages, commits, and pushes imported document files.
func commitAndPushDocImport(tcPath, ep, docID, metaPath, srcPointerPath, textPointerPath string, hasText bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	filesToAdd := []string{metaPath, srcPointerPath}
	if hasText {
		filesToAdd = append(filesToAdd, textPointerPath)
	}

	if err := gitutil.WithRepoLock(ctx, tcPath, func() error {
		files := make(map[string][]byte, len(filesToAdd))
		for _, path := range filesToAdd {
			content, err := readDocImportFile(tcPath, path)
			if err != nil {
				return err
			}
			files[path] = content
		}
		return commitDocImportSnapshot(ctx, tcPath, docID, files)
	}); err != nil {
		return err
	}

	return pushTeamContext(context.Background(), tcPath, ep, 0)
}

// commitDocImportSnapshot stages only validated files and leaves unrelated index entries alone.
// The caller holds WithRepoLock across validation, staging and the snapshot commit.
func commitDocImportSnapshot(ctx context.Context, tcPath, docID string, files map[string][]byte) error {
	if err := gitutil.IsSafeForGitOps(tcPath); err != nil {
		return fmt.Errorf("unsafe import commit: %w", err)
	}
	if err := prepareDocImportAttributes(ctx, tcPath, files); err != nil {
		return err
	}

	paths := make([]string, 0, len(files))
	for path := range files {
		rel, err := filepath.Rel(tcPath, path)
		if err != nil {
			return err
		}
		paths = append(paths, ":(literal)"+filepath.ToSlash(rel))
	}
	sort.Strings(paths)
	args := append([]string{"add", "--sparse", "--"}, paths...)
	if _, err := gitutil.RunGit(ctx, tcPath, args...); err != nil {
		return fmt.Errorf("stage import files: %w", err)
	}
	for path, expected := range files {
		content, found, err := docImportGitBlob(ctx, tcPath, "", path)
		if err != nil {
			return err
		}
		matches := false
		if found {
			matches, err = docImportMatchesBlob(ctx, tcPath, path, expected, content)
			if err != nil {
				return err
			}
		}
		if !matches {
			return fmt.Errorf("import file changed while staging; leaving it unchanged: %s", path)
		}
	}
	_, err := gitutil.CommitLedgerSnapshot(ctx, tcPath, fmt.Sprintf("import: doc %s", docID), paths...)
	return err
}

// prepareDocImportAttributes refuses unrelated edits before updating or staging attributes.
func prepareDocImportAttributes(ctx context.Context, tcPath string, files map[string][]byte) error {
	path := filepath.Join(tcPath, ".gitattributes")
	head, headExists, err := docImportGitBlob(ctx, tcPath, "HEAD", path)
	if err != nil {
		return err
	}
	expected := docImportAttributes(head)
	var current []byte
	currentExists := false
	if _, err := os.Lstat(path); err == nil {
		currentExists = true
		current, err = readDocImportFile(tcPath, path)
		if err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read import .gitattributes: %w", err)
	}
	unchanged := currentExists == headExists
	owned := false
	if currentExists {
		if headExists {
			unchanged, err = docImportMatchesBlob(ctx, tcPath, path, current, head)
			if err != nil {
				return err
			}
		}
		owned, err = docImportMatchesBlob(ctx, tcPath, path, current, expected)
		if err != nil {
			return err
		}
	}
	if !unchanged && !owned {
		return fmt.Errorf("unrelated .gitattributes changes; leaving them unchanged")
	}
	index, indexExists, err := docImportGitBlob(ctx, tcPath, "", path)
	if err != nil {
		return err
	}
	unchanged = indexExists == headExists && bytes.Equal(index, head)
	owned = false
	if indexExists {
		owned, err = docImportMatchesBlob(ctx, tcPath, path, expected, index)
		if err != nil {
			return err
		}
	}
	if !unchanged && !owned {
		return fmt.Errorf("unrelated staged .gitattributes changes; leaving them unchanged")
	}
	if err := ensureMetadataGitattributes(tcPath); err != nil {
		return fmt.Errorf("update import .gitattributes: %w", err)
	}
	content, err := readDocImportFile(tcPath, path)
	if err != nil {
		return err
	}
	matches, err := docImportMatchesBlob(ctx, tcPath, path, content, expected)
	if err != nil {
		return err
	}
	if !matches {
		return fmt.Errorf(".gitattributes changed while preparing the import; leaving it unchanged")
	}
	files[path] = content
	return nil
}

// pushTeamContext pushes team context changes to remote with conflict retry.
// Takes endpoint explicitly since team context path lacks .sageox/ for discovery.
// No auto-resolve — team context conflicts require manual resolution.
// A zero maxRetries preserves the default policy used by new imports.
func pushTeamContext(ctx context.Context, tcPath, ep string, maxRetries int) error {
	return gitutil.PushWithRetry(ctx, tcPath, gitutil.PushOpts{
		MaxRetries: maxRetries,
		PrePush: func(repoPath string) error {
			if ep != "" {
				if err := gitserver.RefreshRemoteCredentials(repoPath, ep); err != nil {
					return fmt.Errorf("credential refresh: %w", err)
				}
			}
			return nil
		},
	})
}

// slugify converts a string to a filesystem-safe directory name.
// Lowercase, spaces/underscores → hyphens, strip non-alphanumeric (keep hyphens).
func slugify(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(unicode.ToLower(r))
		case r == ' ' || r == '_' || r == '-':
			b.WriteRune('-')
		}
	}
	// collapse consecutive hyphens
	re := regexp.MustCompile(`-{2,}`)
	result := re.ReplaceAllString(b.String(), "-")
	return strings.Trim(result, "-")
}

// resolveImportTeam resolves the team context for an import: explicit --team
// first (project lookup, then endpoint scan), otherwise repo-bound team or
// single-team auto-discovery.
func resolveImportTeam(projectRoot, ep string) (*config.TeamContext, error) {
	var tc *config.TeamContext
	if importFlags.team != "" {
		if projectRoot != "" {
			tc = resolveTeamContext(projectRoot, importFlags.team)
		}
		if tc == nil {
			// no project root or team not found via project — scan by endpoint
			tc = resolveTeamContextByEndpoint(importFlags.team, ep)
		}
		if tc == nil {
			return nil, fmt.Errorf("team context not found: %q (use ox agent prime to see available teams)", importFlags.team)
		}
		return tc, nil
	}

	if projectRoot != "" {
		tc = config.FindRepoTeamContext(projectRoot)
	}
	if tc == nil {
		// no project or no team in project — try single-team auto-discovery
		tc = autoDiscoverSingleTeam(ep)
	}
	if tc == nil {
		if projectRoot == "" {
			return nil, fmt.Errorf("no team found — use --team to specify one, or run from inside a SageOx project")
		}
		return nil, fmt.Errorf("no team context configured — use --team to specify one, or run 'ox init' first")
	}
	return tc, nil
}

// resolveImportContext resolves the import target (a team context) and
// creates an authenticated API client. Shared by URL import, status, and
// list operations. Knowledge Bubbles are not an import target (ox ADR-028).
func resolveImportContext(ctx context.Context) (contextType, contextID string, client *api.RepoClient, ep string, err error) {
	_ = ctx // retained for signature stability across the call sites

	projectRoot, _ := findProjectRoot()
	ep = resolveImportEndpoint(projectRoot)

	tc, tcErr := resolveImportTeam(projectRoot, ep)
	if tcErr != nil {
		return "", "", nil, "", tcErr
	}
	contextType, contextID = api.ContextTypeTeam, tc.TeamID

	storedToken, err := auth.GetTokenForEndpoint(ep)
	if err != nil {
		return "", "", nil, "", fmt.Errorf("failed to read auth store: %w", err)
	}
	if storedToken == nil || storedToken.AccessToken == "" {
		return "", "", nil, "", errkind.Errorf(errkind.NotLoggedIn, "not authenticated — run 'ox login' first")
	}

	client = api.NewRepoClientWithEndpoint(ep).WithAuthToken(storedToken.AccessToken)
	return contextType, contextID, client, ep, nil
}

// importContextFlagHint reproduces the context flag the user passed so copy-
// pasted follow-up commands resolve the same context.
func importContextFlagHint() string {
	if importFlags.team != "" {
		return " --team " + importFlags.team
	}
	return ""
}

// runImportURL handles importing a video/audio by URL via the cloud API.
func runImportURL(cmd *cobra.Command, url string, jsonOutput bool) error {
	contextType, contextID, client, _, err := resolveImportContext(cmd.Context())
	if err != nil {
		return err
	}

	req := &api.ImportVideoURLRequest{
		URL:   url,
		Title: importFlags.title,
	}

	resp, err := client.ImportVideoURL(contextType, contextID, req)
	if err != nil {
		return fmt.Errorf("import failed: %w", err)
	}
	if resp == nil {
		return fmt.Errorf("import endpoint not available (server may not support URL imports yet)")
	}

	if jsonOutput {
		return cli.PrintJSONTo(cmd.OutOrStdout(), resp)
	}

	cli.PrintSuccess("Import started")
	fmt.Fprintf(cmd.OutOrStdout(), "  ID:        %s\n", resp.RecordingID)
	fmt.Fprintf(cmd.OutOrStdout(), "  Status:    %s\n", resp.Status)
	if resp.Title != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "  Title:     %s\n", resp.Title)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "\n  Track progress: ox import --status %s --watch%s\n", resp.RecordingID, importContextFlagHint())
	return nil
}

// runImportStatus handles checking the processing status of a recording.
func runImportStatus(cmd *cobra.Command, jsonOutput bool) error {
	contextType, contextID, client, _, err := resolveImportContext(cmd.Context())
	if err != nil {
		return err
	}

	recordingID := importFlags.status

	// The recording row may not exist yet: the API pre-generates the ID before
	// starting the Temporal workflow, and the DB insert happens inside the workflow
	// (CreateVideoRecording activity). Treat 404 as status="starting" rather than
	// an error, since the ID is known-valid.
	for {
		resp, err := client.GetVideoStatus(contextType, contextID, recordingID)
		if err != nil {
			return fmt.Errorf("status check failed: %w", err)
		}
		if resp == nil {
			// row not yet created — show as "starting"
			if jsonOutput {
				starting := struct {
					ID     string `json:"id"`
					Status string `json:"status"`
				}{ID: recordingID, Status: "starting"}
				if importFlags.watch {
					// JSONL: one compact JSON object per line for streaming
					out, _ := json.Marshal(starting)
					out = append(out, '\n')
					_ = cli.WriteJSONBytes(cmd.OutOrStdout(), out)
				} else {
					_ = cli.PrintJSONTo(cmd.OutOrStdout(), starting)
				}
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "Recording: %s\nStatus:    starting\n", recordingID)
			}
			if !importFlags.watch {
				return nil
			}
			time.Sleep(3 * time.Second)
			continue
		}

		if jsonOutput {
			if importFlags.watch {
				// JSONL: one compact JSON object per line for streaming
				out, _ := json.Marshal(resp)
				out = append(out, '\n')
				_ = cli.WriteJSONBytes(cmd.OutOrStdout(), out)
			} else {
				_ = cli.PrintJSONTo(cmd.OutOrStdout(), resp)
			}
		} else {
			printVideoStatus(cmd, resp)
		}

		if !importFlags.watch || resp.Status == "ready" || resp.Status == "failed" {
			return nil
		}

		time.Sleep(3 * time.Second)
		if !jsonOutput {
			fmt.Fprintln(cmd.OutOrStdout(), "\n---")
		}
	}
}

// printVideoStatus renders a human-readable status display.
func printVideoStatus(cmd *cobra.Command, resp *api.VideoStatusResponse) {
	fmt.Fprintf(cmd.OutOrStdout(), "Recording: %s\n", resp.ID)
	if resp.Title != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Title:     %s\n", resp.Title)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Status:    %s\n", resp.Status)

	if resp.Duration != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "Duration:  %.0fs\n", *resp.Duration)
	}

	if len(resp.ProcessingSteps) > 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "\nProcessing Steps")
		for name, step := range resp.ProcessingSteps {
			status, _ := step["status"].(string)
			icon := "·"
			switch status {
			case "complete", "completed":
				icon = "✓"
			case "in_progress", "processing":
				icon = "◐"
			case "failed":
				icon = "✗"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "  %s %-20s %s\n", icon, name, status)
		}
	}
}

// runImportList handles listing recordings in the resolved context.
func runImportList(cmd *cobra.Command, jsonOutput bool) error {
	contextType, contextID, client, _, err := resolveImportContext(cmd.Context())
	if err != nil {
		return err
	}

	resp, err := client.ListVideos(contextType, contextID, 50, 0)
	if err != nil {
		return fmt.Errorf("list recordings failed: %w", err)
	}
	if resp == nil {
		return fmt.Errorf("recordings endpoint not available")
	}

	if jsonOutput {
		return cli.PrintJSONTo(cmd.OutOrStdout(), resp)
	}

	if len(resp.Recordings) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No recordings found.")
		return nil
	}

	// table header
	fmt.Fprintf(cmd.OutOrStdout(), "%-40s %-30s %-12s %s\n", "ID", "TITLE", "STATUS", "CREATED")
	fmt.Fprintf(cmd.OutOrStdout(), "%-40s %-30s %-12s %s\n", strings.Repeat("-", 40), strings.Repeat("-", 30), strings.Repeat("-", 12), strings.Repeat("-", 20))
	for _, rec := range resp.Recordings {
		title := rec.Title
		if len(title) > 28 {
			title = title[:28] + ".."
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%-40s %-30s %-12s %s\n", rec.ID, title, rec.Status, rec.CreatedAt.Format("2006-01-02 15:04"))
	}

	if resp.Pagination.HasMore {
		fmt.Fprintf(cmd.OutOrStdout(), "\n(%d of %d shown)\n", len(resp.Recordings), resp.Pagination.Total)
	}
	return nil
}

// notifyImport notifies the cloud about a new import and returns the recording
// ID the server assigns when it routes the file to transcription (empty for
// non-media docs or older servers). Uses team_id since imports target team
// contexts, not project repos. Failures are logged but never block the import.
func notifyImport(teamID, ep string, meta docMeta) string {
	if teamID == "" {
		slog.Debug("skipping import notification, no team_id")
		return ""
	}

	storedToken, err := auth.GetTokenForEndpoint(ep)
	if err != nil || storedToken == nil || storedToken.AccessToken == "" {
		slog.Debug("skipping import notification, no auth token", "error", err)
		return ""
	}

	client := api.NewRepoClientWithEndpoint(ep).WithAuthToken(storedToken.AccessToken)
	recordingID, err := client.NotifyImport(teamID, &meta)
	if err != nil {
		slog.Warn("import cloud notification failed", "error", err)
	}
	return recordingID
}
