package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/gitserver"
	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/lfs"
	"github.com/spf13/cobra"
)

var importFlags struct {
	title string
	text  string
	date  string
	force bool
}

var importCmd = &cobra.Command{
	Use:   "import <file>",
	Short: "Import a document into team context",
	Long: `Import a document into team context for onboarding and knowledge sharing.

Documents are stored with LFS-backed content and git-tracked metadata.
AI coworkers extract text before importing for indexing:

  ox import report.pdf --text extracted.md --title "Q1 Report"
  ox import notes.md --date 2026-01-15`,
	Args: cobra.ExactArgs(1),
	RunE: runImport,
}

func init() {
	importCmd.Flags().StringVar(&importFlags.title, "title", "", "document title (default: filename stem)")
	importCmd.Flags().StringVar(&importFlags.text, "text", "", "path to pre-extracted text/markdown for indexing")
	importCmd.Flags().StringVar(&importFlags.date, "date", "", "document date for filing (YYYY-MM-DD, default: file mtime)")
	importCmd.Flags().BoolVar(&importFlags.force, "force", false, "re-import even if content hash already exists")
}

// docMeta is the metadata.json schema for imported documents.
type docMeta struct {
	Version        string              `json:"version"`
	Title          string              `json:"title"`
	SourceFilename string              `json:"source_filename"`
	ContentType    string              `json:"content_type"`
	SourceSize     int64               `json:"source_size"`
	SourceOID      string              `json:"source_oid"`
	CreatedAt      string              `json:"created_at"`
	ImportedAt     string              `json:"imported_at"`
	HasTextExtract bool                `json:"has_text_extract"`
	Path           string              `json:"path"`
	Sidecars       map[string]sidecar  `json:"sidecars"`
}

// sidecar describes an additional derived file associated with an imported document.
// The map key in docMeta.Sidecars is the sidecar type (e.g., "text-extract").
type sidecar struct {
	Filename  string `json:"filename"`
	OID       string `json:"oid"`
	Size      int64  `json:"size"`
	CreatedAt string `json:"created_at"`
}

func runImport(cmd *cobra.Command, args []string) error {
	srcPath := args[0]

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

	projectRoot, err := findProjectRoot()
	if err != nil {
		return fmt.Errorf("not in a SageOx project: %w", err)
	}

	tc := config.FindRepoTeamContext(projectRoot)
	if tc == nil {
		return fmt.Errorf("no team context configured — run 'ox init' first")
	}

	// no sparse checkout expansion needed: git add stages files outside the
	// sparse cone, and we create the directory ourselves. The files may be
	// cleaned up on the next checkout, but they're already pushed by then.
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
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Already imported (id: %s). Use --force to reimport.\n", existing)
			return nil
		}
	}

	// derive directory name from --title or filename stem
	dirName := importFlags.title
	if dirName == "" {
		dirName = inferTitle(srcPath)
	}
	dirSlug := slugify(dirName)

	docDir := filepath.Join(docsBaseDir,
		importDate.Format("2006"),
		importDate.Format("01"),
		importDate.Format("02"),
		dirSlug,
	)
	if _, statErr := os.Stat(docDir); statErr == nil && !importFlags.force {
		return fmt.Errorf("document directory already exists for this date — use --title to differentiate or --force to reimport: %s", docDir)
	}
	if err := os.MkdirAll(docDir, 0o755); err != nil {
		return fmt.Errorf("create doc directory: %w", err)
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
	lfsClient, err := getTeamContextLFSClient(projectRoot, tc)
	if err != nil {
		return fmt.Errorf("create LFS client: %w", err)
	}

	slog.Info("uploading doc to LFS", "doc", dirSlug, "files", len(batchObjects))

	resp, err := lfsClient.BatchUpload(batchObjects)
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

	// write LFS pointer files (preserving original filename)
	srcFilename := filepath.Base(srcPath)
	srcPointerPath := filepath.Join(docDir, srcFilename)
	if err := os.WriteFile(srcPointerPath, []byte(lfs.FormatPointer(srcRef.OID, srcRef.Size)), 0o644); err != nil {
		return fmt.Errorf("write source pointer: %w", err)
	}

	textPointerPath := filepath.Join(docDir, "extracted.md")
	if hasText {
		if err := os.WriteFile(textPointerPath, []byte(lfs.FormatPointer(textRef.OID, textRef.Size)), 0o644); err != nil {
			return fmt.Errorf("write extracted.md pointer: %w", err)
		}
	}

	title := importFlags.title
	if title == "" {
		title = inferTitle(srcPath)
	}

	// build and write metadata.json
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
		HasTextExtract: hasText,
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

	// ensure metadata.json stays out of LFS
	if err := ensureMetadataGitattributes(tc.Path); err != nil {
		slog.Warn("could not update .gitattributes", "error", err, "path", tc.Path)
	}

	ep := endpoint.GetForProject(projectRoot)
	if err := commitAndPushDocImport(tc.Path, ep, dirSlug, metaPath, srcPointerPath, textPointerPath, hasText); err != nil {
		return fmt.Errorf("commit and push: %w", err)
	}

	// fire-and-forget cloud notification
	notifyImport(projectRoot, ep, meta)

	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Imported: %s\nPath: %s\n", title, relDocDir)
	return nil
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
	const marker = "data/**/metadata.json"
	const override = "data/**/metadata.json !filter !diff !merge text"

	content, err := os.ReadFile(gitattrsPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read .gitattributes: %w", err)
	}

	if strings.Contains(string(content), marker) {
		return nil
	}

	existing := strings.TrimRight(string(content), "\n")
	if existing != "" {
		existing += "\n"
	}
	newContent := existing + override + "\n"
	return os.WriteFile(gitattrsPath, []byte(newContent), 0o644)
}

// findExistingDocByOID scans data/docs/ metadata.json files for a matching source OID.
// Returns the doc directory name if found.
func findExistingDocByOID(docsBaseDir, oid string) (string, bool) {
	var docID string
	var found bool

	_ = filepath.WalkDir(docsBaseDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Name() != "metadata.json" {
			return nil
		}
		data, readErr := os.ReadFile(path)
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
			docID = filepath.Base(filepath.Dir(path))
			found = true
			return filepath.SkipAll
		}
		return nil
	})

	return docID, found
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
	}
	sniffLen := len(content)
	if sniffLen > 512 {
		sniffLen = 512
	}
	return http.DetectContentType(content[:sniffLen])
}

// getTeamContextLFSClient creates an LFS client for the team context repo.
// Fallback chain: cloud API → cached marker → git remote URL.
func getTeamContextLFSClient(projectRoot string, tc *config.TeamContext) (*lfs.Client, error) {
	ep := endpoint.GetForProject(projectRoot)

	creds, err := gitserver.LoadCredentialsForEndpoint(ep)
	if err != nil {
		return nil, fmt.Errorf("load credentials: %w", err)
	}
	if creds == nil {
		return nil, fmt.Errorf("no git credentials found (run 'ox login' first)")
	}
	if creds.Token == "" {
		return nil, fmt.Errorf("git credentials have empty token")
	}

	sageoxDir := filepath.Join(projectRoot, ".sageox")
	repoURL := GetTeamURLWithFallback(sageoxDir, tc.TeamID, ep)
	if repoURL == "" {
		// last resort: read from local git remote
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		out, gitErr := gitutil.RunGit(ctx, tc.Path, "remote", "get-url", "origin")
		if gitErr != nil || strings.TrimSpace(out) == "" {
			return nil, fmt.Errorf("no team context repo URL found (API and git remote both failed)")
		}
		repoURL = strings.TrimSpace(out)
	}

	return lfs.NewClient(repoURL, creds.Username, creds.Token), nil
}

// commitAndPushDocImport stages, commits, and pushes imported document files.
func commitAndPushDocImport(tcPath, ep, docID, metaPath, srcPointerPath, textPointerPath string, hasText bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	filesToAdd := []string{metaPath, srcPointerPath}
	if hasText {
		filesToAdd = append(filesToAdd, textPointerPath)
	}

	// include .gitattributes if it exists
	gitattrsPath := filepath.Join(tcPath, ".gitattributes")
	if _, err := os.Stat(gitattrsPath); err == nil {
		filesToAdd = append(filesToAdd, gitattrsPath)
	}

	addArgs := append([]string{"-C", tcPath, "add"}, filesToAdd...)
	addCmd := exec.CommandContext(ctx, "git", addArgs...)
	if out, err := addCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git add failed: %s: %w", string(out), err)
	}

	commitMsg := fmt.Sprintf("import: doc %s", docID)
	commitCmd := exec.CommandContext(ctx, "git", "-C", tcPath, "commit", "--no-verify", "-m", commitMsg)
	if out, err := commitCmd.CombinedOutput(); err != nil {
		if strings.Contains(string(out), "nothing to commit") {
			return nil
		}
		return fmt.Errorf("git commit failed: %s: %w", string(out), err)
	}

	return pushTeamContext(context.Background(), tcPath, ep)
}

// pushTeamContext pushes team context changes to remote with conflict retry.
// Takes endpoint explicitly since team context path lacks .sageox/ for discovery.
// Same retry semantics as pushLedger: 3 attempts, pull --rebase on rejection.
func pushTeamContext(ctx context.Context, tcPath, ep string) error {
	if err := gitutil.IsSafeForGitOps(tcPath); err != nil {
		return fmt.Errorf("team context blocked: %w", err)
	}

	gitutil.StripLFSConfig(tcPath)

	if ep != "" {
		if err := gitserver.RefreshRemoteCredentials(tcPath, ep); err != nil {
			slog.Debug("remote credential refresh skipped before push", "error", err)
		}
	}

	const maxRetries = 3
	const opTimeout = 60 * time.Second

	permanentPatterns := []string{
		"Permission denied",
		"could not read Username",
		"Authentication failed",
		"invalid credentials",
		"repository not found",
	}

	for attempt := 1; attempt <= maxRetries; attempt++ {
		attemptCtx, cancel := context.WithTimeout(ctx, opTimeout)
		outStr, err := gitutil.RunGit(attemptCtx, tcPath, "push", "--quiet")
		cancel()
		if err == nil {
			return nil
		}

		for _, pattern := range permanentPatterns {
			if strings.Contains(outStr, pattern) {
				return fmt.Errorf("git push failed (not retryable): %s", outStr)
			}
		}

		if attempt == maxRetries {
			return fmt.Errorf("git push failed after %d attempts: %s", maxRetries, outStr)
		}

		slog.Info("push failed, retrying", "attempt", attempt, "output", outStr)

		if strings.Contains(outStr, "non-fast-forward") || strings.Contains(outStr, "rejected") {
			if gitutil.IsRebaseInProgress(tcPath) {
				abortCtx, abortCancel := context.WithTimeout(ctx, opTimeout)
				_, _ = gitutil.RunGit(abortCtx, tcPath, "rebase", "--abort")
				abortCancel()
			}

			pullCtx, pullCancel := context.WithTimeout(ctx, opTimeout)
			pullOut, pullErr := gitutil.RunGit(pullCtx, tcPath, "pull", "--rebase", "--quiet")
			pullCancel()
			if pullErr != nil {
				abortCtx, abortCancel := context.WithTimeout(ctx, opTimeout)
				_, _ = gitutil.RunGit(abortCtx, tcPath, "rebase", "--abort")
				abortCancel()
				return fmt.Errorf("git pull --rebase failed during retry: %s", pullOut)
			}
		}

		time.Sleep(time.Duration(attempt) * time.Second)
	}

	return nil
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

// notifyImport sends a fire-and-forget notification to the cloud about a new import.
// Failures are logged but never block the import.
func notifyImport(projectRoot, ep string, meta docMeta) {
	projCfg, err := config.LoadProjectConfig(projectRoot)
	if err != nil || projCfg.RepoID == "" {
		slog.Debug("skipping import notification, no repo_id", "error", err)
		return
	}

	storedToken, err := auth.GetTokenForEndpoint(ep)
	if err != nil || storedToken == nil || storedToken.AccessToken == "" {
		slog.Debug("skipping import notification, no auth token", "error", err)
		return
	}

	client := api.NewRepoClientForProject(projectRoot).WithAuthToken(storedToken.AccessToken)
	if err := client.NotifyImport(projCfg.RepoID, &meta); err != nil {
		slog.Warn("import cloud notification failed", "error", err)
	}
}
