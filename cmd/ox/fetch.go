package main

import (
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/gitserver"
	"github.com/sageox/ox/internal/lfs"
	"github.com/spf13/cobra"
)

var fetchCmd = &cobra.Command{
	Use:   "fetch <path>",
	Short: "Fetch content for a stub file",
	Long: `Fetch the real content behind an LFS pointer (stub) file.

Pass a path to a local stub file — the file's OID, size, and source repo
are detected automatically. Downloads the content to a local cache and
prints the absolute cache path so you can Read it.

The cache mirrors the source repo's directory structure with original
filenames preserved. Subsequent fetches of the same file are instant
(cache hit).

Examples:
  ox fetch ~/.sageox/data/teams/<id>/discussions/2026-03-31/keyframes/frame-003.jpg
  ox fetch ~/.sageox/data/teams/<id>/discussions/2026-03-31/keyframes/frame-003.jpg -o photo.jpg
  ox fetch <ledger>/sessions/2026-03-31T14-32-ryan/raw.jsonl --stdout | jq .`,
	Args:   cobra.ExactArgs(1),
	Hidden: true,
	RunE:   runFetch,
}

var (
	fetchOutputFlag string
	fetchStdoutFlag bool
	fetchVerifyFlag bool
)

func init() {
	fetchCmd.Flags().StringVarP(&fetchOutputFlag, "output", "o", "", "write output to file instead of cache")
	fetchCmd.Flags().BoolVar(&fetchStdoutFlag, "stdout", false, "write binary content to stdout instead of cache")
	fetchCmd.Flags().BoolVar(&fetchVerifyFlag, "verify", true, "verify SHA256 digest of downloaded content")
	rootCmd.AddCommand(fetchCmd)
}

func runFetch(cmd *cobra.Command, args []string) error {
	pointerPath, err := filepath.Abs(args[0])
	if err != nil {
		return fmt.Errorf("resolve path: %w", err)
	}

	if _, err := os.Stat(pointerPath); err != nil {
		return fmt.Errorf("file not found: %s", pointerPath)
	}

	if !lfs.IsPointerFile(pointerPath) {
		return fmt.Errorf("not a stub file: %s\nExpected an LFS pointer file (starts with \"version https://git-lfs\")", pointerPath)
	}

	ref, err := lfs.ReadPointerFile(pointerPath)
	if err != nil {
		return fmt.Errorf("read stub file: %w", err)
	}

	oid := strings.TrimPrefix(ref.OID, "sha256:")

	// reject non-hex OIDs early
	if len(oid) != 64 || !isHex(oid) {
		return fmt.Errorf("invalid OID: expected 64-char hex SHA256, got %q", oid)
	}

	repoRoot, err := detectRepo(pointerPath)
	if err != nil {
		return fmt.Errorf("detect source repo: %w", err)
	}

	// compute relative path within the repo
	relPath, err := filepath.Rel(repoRoot, pointerPath)
	if err != nil {
		return fmt.Errorf("compute relative path: %w", err)
	}

	// check cache
	cachePath := filepath.Join(repoRoot, ".sageox", "cache", relPath)
	if isCacheHit(cachePath, ref.Size) {
		if fetchStdoutFlag {
			return streamFileToStdout(cmd, cachePath)
		}
		fmt.Fprintln(cmd.OutOrStdout(), cachePath)
		return nil
	}

	// create LFS client for the detected repo
	projectRoot, _ := findProjectRoot()
	client, err := newFetchLFSClient(projectRoot, repoRoot)
	if err != nil {
		return hydrateHint(err)
	}

	// download
	resp, err := client.BatchDownload([]lfs.BatchObject{{OID: oid, Size: ref.Size}})
	if err != nil {
		return hydrateHint(fmt.Errorf("LFS batch download: %w", err))
	}

	if len(resp.Objects) == 0 {
		return fmt.Errorf("object not found: %s", oid)
	}

	obj := resp.Objects[0]
	if obj.Error != nil {
		return fmt.Errorf("download error %d: %s", obj.Error.Code, obj.Error.Message)
	}
	if obj.Actions == nil || obj.Actions.Download == nil {
		return fmt.Errorf("no download action for object %s", oid)
	}

	// stream to stdout
	if fetchStdoutFlag {
		return lfs.DownloadToFile(obj.Actions.Download, cmd.OutOrStdout(), fetchVerifyFlag, oid)
	}

	// determine output path
	outPath := cachePath
	if fetchOutputFlag != "" {
		outPath, err = filepath.Abs(fetchOutputFlag)
		if err != nil {
			return fmt.Errorf("resolve output path: %w", err)
		}
	}

	// stream to file (no full-blob buffering)
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}

	tmpPath := outPath + ".tmp"
	f, err := os.Create(tmpPath)
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}

	if err := lfs.DownloadToFile(obj.Actions.Download, f, fetchVerifyFlag, oid); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("download failed: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("close temp file: %w", err)
	}

	// atomic rename
	if err := os.Rename(tmpPath, outPath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename to final path: %w", err)
	}

	fmt.Fprintln(cmd.OutOrStdout(), outPath)
	return nil
}

// detectRepo walks up from the pointer file path to find the git repo root,
// which must be a known SageOx repo (team context or ledger).
func detectRepo(pointerPath string) (string, error) {
	dir := filepath.Dir(pointerPath)
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", fmt.Errorf("not inside a git repository: %s", pointerPath)
}

// newFetchLFSClient creates an LFS client for any SageOx-managed repo
// (team context or ledger). Credentials come from the project's endpoint.
func newFetchLFSClient(projectRoot, repoRoot string) (*lfs.Client, error) {
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

	repoURL, err := gitserver.GetBareRemoteURL(repoRoot)
	if err != nil {
		return nil, fmt.Errorf("get remote URL: %w", err)
	}
	if repoURL == "" {
		return nil, fmt.Errorf("repo has no remote URL: %s", repoRoot)
	}

	return lfs.NewClient(repoURL, creds.Username, creds.Token), nil
}

// isCacheHit returns true if the cached file exists and its size matches.
func isCacheHit(cachePath string, expectedSize int64) bool {
	info, err := os.Stat(cachePath)
	if err != nil {
		return false
	}
	return info.Size() == expectedSize
}

// isHex returns true if s contains only hexadecimal characters.
func isHex(s string) bool {
	_, err := hex.DecodeString(s)
	return err == nil
}

// streamFileToStdout writes a cached file to stdout.
func streamFileToStdout(cmd *cobra.Command, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open cached file: %w", err)
	}
	defer f.Close()
	_, err = io.Copy(cmd.OutOrStdout(), f)
	return err
}
