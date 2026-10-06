package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/identity"
	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/lfs/pointer"
	"github.com/sageox/ox/internal/sacred"
)

// CheckSlugSessionPointerRestore finds session artifacts that unpushed Ledger
// commits hold as raw content and rewrites them back to LFS pointers (#1174).
const CheckSlugSessionPointerRestore = "session-pointer-restore"

const pointerRestoreCheckName = "session LFS pointers"

func init() {
	RegisterDoctorCheck(&DoctorCheck{
		Slug:     CheckSlugSessionPointerRestore,
		Name:     pointerRestoreCheckName,
		Category: "Sessions",
		// Suggested: the repair adds one commit. It only swaps raw bytes for the pointer
		// whose OID is the sha256 of those same bytes, so no confirmation is needed.
		FixLevel:    FixLevelSuggested,
		Description: "Restores LFS pointers where unpushed Ledger commits hold raw session content",
		Run:         checkSessionPointerRestore,
	})
}

type pointerRestoreFailure struct {
	Path   string
	Reason string
}

// pointerRestoreReport is the outcome of one scan (and repair) of @{u}..HEAD.
type pointerRestoreReport struct {
	Raw          []string                // artifacts that are raw content at HEAD
	Restored     []string                // rewritten to a pointer in the new commit
	Untracked    []string                // draft-directory artifacts removed from the tree (bytes kept in the cache)
	Unrepairable []pointerRestoreFailure // raw content no pointer or manifest vouches for
	Committed    bool
	// Remaining is what the push validator still reports after the repair; it
	// names the first file that keeps blocking the push.
	Remaining error
}

func checkSessionPointerRestore(fix bool) checkResult {
	ledgerPath := getLedgerPath()
	if ledgerPath == "" {
		return SkippedCheck(pointerRestoreCheckName, "no ledger found", "")
	}
	// a Ledger that is not a git repo has no upstream, which the body reports as a skip
	return runSessionPointerRestore(ledgerPath, fix, newOwnArtifactUploader(ledgerPath))
}

// runSessionPointerRestore is the check body for one resolved Ledger.
func runSessionPointerRestore(ledgerPath string, fix bool, uploader *ownArtifactUploader) checkResult {
	report, err := restoreUnpushedSessionPointers(context.Background(), ledgerPath, fix, uploader)
	switch {
	case errors.Is(err, errNoUpstream):
		return SkippedCheck(pointerRestoreCheckName, "ledger has no upstream branch", "")
	case gitutil.IsRepoLockBusy(err):
		return SkippedCheck(pointerRestoreCheckName, "ledger busy with another ox process", "Rerun `ox doctor --fix-slug="+CheckSlugSessionPointerRestore+"` in a moment")
	case err != nil:
		return FailedCheck(pointerRestoreCheckName, "could not inspect unpushed session commits", err.Error())
	}

	if len(report.Raw) == 0 {
		return PassedCheck(pointerRestoreCheckName, "no raw session content in unpushed commits")
	}
	fixCommand := "ox doctor --fix-slug=" + CheckSlugSessionPointerRestore
	if !fix {
		return FailedCheck(pointerRestoreCheckName,
			fmt.Sprintf("%d session artifact(s) in unpushed commits hold raw content where LFS pointers belong; pushes are refused", len(report.Raw)),
			fmt.Sprintf("Run `%s` to restore the pointers (adds one commit, local copies kept in the cache)", fixCommand))
	}
	if len(report.Unrepairable) > 0 {
		var detail strings.Builder
		fmt.Fprintf(&detail, "restored %d; %d cannot be restored automatically and still block the push:", len(report.Restored), len(report.Unrepairable))
		for _, failure := range report.Unrepairable {
			fmt.Fprintf(&detail, "\n       %s: %s", failure.Path, failure.Reason)
		}
		fmt.Fprintf(&detail, "\n       Re-upload the session from a machine that has its content, or restore the pointer by hand, then rerun `%s`.", fixCommand)
		return FailedCheck(pointerRestoreCheckName, "some session artifacts hold raw content no known LFS object matches", detail.String())
	}
	if report.Remaining != nil {
		return FailedCheck(pointerRestoreCheckName, "restored pointers but the push validator still refuses the tip", report.Remaining.Error())
	}
	return PassedCheck(pointerRestoreCheckName, fmt.Sprintf("restored LFS pointers for %d session artifact(s), untracked %d draft artifact(s)", len(report.Restored), len(report.Untracked)))
}

var errNoUpstream = errors.New("no upstream branch")

// newOwnArtifactUploader identifies the current coworker the way recording stamps
// meta.json, and uploads through the Ledger's own LFS credentials.
func newOwnArtifactUploader(ledgerPath string) *ownArtifactUploader {
	projectEndpoint := endpoint.GetForProject(findGitRoot())
	return &ownArtifactUploader{
		username: identity.AttributionDisplayName(projectEndpoint, config.GetDisplayName()),
		client:   func() (*lfs.Client, error) { return lfs.NewClientFromLedger(ledgerPath, projectEndpoint) },
	}
}

// ownArtifactUploader lets the repair upload artifacts that were never uploaded.
type ownArtifactUploader struct {
	username string
	client   func() (*lfs.Client, error)
}

// rawSessionArtifact is one content artifact whose HEAD blob is not a pointer.
type rawSessionArtifact struct {
	path    string // ledger-relative, slash-separated
	content []byte // the HEAD blob
}

// restoreUnpushedSessionPointers scans @{u}..HEAD for session artifacts that are
// raw content at HEAD. With fix, each one whose sha256 matches the pointer
// upstream holds at that path, or the OID in its session's meta.json, is rewritten
// to that pointer and committed once through CommitLedgerSnapshot with explicit
// pathspecs. Existing commits are never amended or rewritten; anything nothing
// vouches for is reported and left exactly as it is.
func restoreUnpushedSessionPointers(ctx context.Context, ledgerPath string, fix bool, uploader *ownArtifactUploader) (pointerRestoreReport, error) {
	var report pointerRestoreReport
	upstream, err := ledgerUpstream(ctx, ledgerPath)
	if err != nil {
		return report, err
	}
	raws, err := rawSessionArtifactsAhead(ctx, ledgerPath, upstream)
	if err != nil {
		return report, err
	}
	for _, raw := range raws {
		report.Raw = append(report.Raw, raw.path)
	}
	if len(raws) == 0 || !fix {
		return report, nil
	}

	err = gitutil.WithRepoLock(ctx, ledgerPath, func() error {
		var repairable, pathspecs []string
		for _, raw := range raws {
			if headSessionMeta(ctx, ledgerPath, strings.Split(raw.path, "/")[1]).IsDraft() {
				if reason := untrackDraftArtifact(ctx, ledgerPath, raw); reason != "" {
					report.Unrepairable = append(report.Unrepairable, pointerRestoreFailure{Path: raw.path, Reason: reason})
					continue
				}
				report.Untracked = append(report.Untracked, raw.path)
				pathspecs = append(pathspecs, raw.path)
				continue
			}
			reason, metaPath := restoreOne(ctx, ledgerPath, upstream, raw, uploader)
			if reason != "" {
				report.Unrepairable = append(report.Unrepairable, pointerRestoreFailure{Path: raw.path, Reason: reason})
				continue
			}
			repairable = append(repairable, raw.path)
			pathspecs = append(pathspecs, raw.path)
			if metaPath != "" && !slices.Contains(pathspecs, metaPath) {
				pathspecs = append(pathspecs, metaPath)
			}
		}
		if len(pathspecs) == 0 {
			return nil
		}
		guard := newSessionStageGuard(ledgerPath, filepath.Join(ledgerPath, "sessions"))
		toAdd := slices.DeleteFunc(slices.Clone(pathspecs), func(path string) bool { return slices.Contains(report.Untracked, path) })
		if err := guard.gitPathspec(ctx, toAdd, "add", "--sparse"); err != nil {
			return fmt.Errorf("stage restored pointers: %w", err)
		}
		msg := fmt.Sprintf("doctor: restore LFS pointers for %d session artifacts", len(repairable))
		if len(report.Untracked) > 0 {
			msg = fmt.Sprintf("doctor: restore LFS pointers / untrack draft artifacts for %d session artifacts", len(repairable)+len(report.Untracked))
		}
		if len(report.Untracked) > sacred.MassDeleteThreshold {
			// a deliberate bulk removal: every untracked artifact's bytes are already in the cache,
			// and none of them was ever pushed, so the sacred mass-delete guard has nothing to protect
			defer allowSacredMassDelete()()
		}
		committed, err := gitutil.CommitLedgerSnapshot(ctx, ledgerPath, msg, pathspecs...)
		if err != nil {
			return fmt.Errorf("commit restored pointers: %w", err)
		}
		report.Committed = committed
		report.Restored = repairable
		return nil
	})
	if err != nil {
		return report, err
	}
	// the same validation the push path runs, so a clean result proves the push is unblocked
	report.Remaining = lfs.ValidateUnpushedTip(ctx, ledgerPath, upstream)
	return report, nil
}

// restoreOne rewrites one working-tree artifact to the pointer that the evidence
// names, or returns the reason it cannot. It touches the file only after the
// evidence matches the committed bytes exactly. For an artifact nothing vouches
// for, the current coworker's own sessions are uploaded (the bytes' sha256 is
// the OID) and meta.json records the new FileRef; metaPath is then the extra
// ledger-relative path the repair commit must include.
func restoreOne(ctx context.Context, ledgerPath, upstream string, raw rawSessionArtifact, uploader *ownArtifactUploader) (reason, metaPath string) {
	parts := strings.Split(raw.path, "/")
	sessionID, name := parts[1], strings.Join(parts[2:], "/")
	abs := filepath.Join(ledgerPath, filepath.FromSlash(raw.path))
	meta := headSessionMeta(ctx, ledgerPath, sessionID)

	ref, _, ok := lfs.ResolveRestorableRef(raw.content, name, meta, upstreamPointer(ctx, ledgerPath, upstream, raw.path))
	if !ok {
		if !uploader.owns(meta) {
			return "content sha256 matches no pointer on " + upstream + " and no entry in the session's meta.json", ""
		}
		ref, reason = uploadOwnArtifact(ctx, ledgerPath, sessionID, name, abs, raw.content, uploader)
		if reason != "" {
			return reason, ""
		}
		metaPath = "sessions/" + sessionID + "/meta.json"
	}

	// the new commit is built from the working file, so it must be exactly what HEAD holds
	// (or already the matching pointer); never overwrite someone's newer local edit
	working, err := os.ReadFile(abs)
	switch {
	case err != nil:
		return fmt.Sprintf("working copy unreadable: %v", err), ""
	case bytes.Equal(working, raw.content):
		cachePath := filepath.Join(ledgerPath, ".sageox", "cache", filepath.FromSlash(raw.path))
		if err := lfs.RestorePointer(abs, cachePath, raw.content, ref); err != nil {
			return err.Error(), ""
		}
	case lfs.IsPointerFile(abs):
		current, err := lfs.ReadPointerFile(abs)
		if err != nil || current.BareOID() != ref.BareOID() || current.Size != ref.Size {
			return "working copy is a different pointer than the committed content's OID", ""
		}
	default:
		return "working copy differs from the committed content (uncommitted local edit); commit or discard it first", ""
	}
	return "", metaPath
}

// allowSacredMassDelete sets the guard's documented override for the duration of one
// commit and returns the function that restores the previous value.
func allowSacredMassDelete() func() {
	previous, had := os.LookupEnv(sacred.OverrideEnv)
	_ = os.Setenv(sacred.OverrideEnv, "1")
	return func() {
		if had {
			_ = os.Setenv(sacred.OverrideEnv, previous)
		} else {
			_ = os.Unsetenv(sacred.OverrideEnv)
		}
	}
}

// untrackDraftArtifact removes an artifact a draft session directory must never track.
// A draft holds only meta.json (.claude/rules/cache-only-design.md), so the committed bytes
// go to the ledger cache first and only then leave the index; meta.json is untouched. The
// working file is deleted only when it is exactly the bytes now safe in the cache.
func untrackDraftArtifact(ctx context.Context, ledgerPath string, raw rawSessionArtifact) string {
	cachePath := filepath.Join(ledgerPath, ".sageox", "cache", filepath.FromSlash(raw.path))
	if err := lfs.PreserveInCache(cachePath, raw.content); err != nil {
		return err.Error()
	}
	if out, err := exec.CommandContext(ctx, "git", "-C", ledgerPath, "rm", "--cached", "-q", "--sparse", "--", raw.path).CombinedOutput(); err != nil {
		return fmt.Sprintf("cannot untrack draft artifact: %v: %s", err, strings.TrimSpace(string(out)))
	}
	abs := filepath.Join(ledgerPath, filepath.FromSlash(raw.path))
	if working, err := os.ReadFile(abs); err == nil && bytes.Equal(working, raw.content) {
		_ = os.Remove(abs)
	}
	return ""
}

// owns reports whether the session's author is the current coworker. The author
// is meta.json's privacy-safe username, the same value recording stamps into it.
func (u *ownArtifactUploader) owns(meta *lfs.SessionMeta) bool {
	return u != nil && u.username != "" && meta != nil && strings.EqualFold(meta.Username, u.username)
}

// uploadOwnArtifact uploads content, then records its FileRef in the working
// meta.json. Order matters: with the manifest written first, a failure before
// the pointer lands leaves a state the next run resolves from the manifest.
func uploadOwnArtifact(ctx context.Context, ledgerPath, sessionID, name, abs string, content []byte, uploader *ownArtifactUploader) (lfs.FileRef, string) {
	if working, err := os.ReadFile(abs); err != nil || !bytes.Equal(working, content) {
		return lfs.FileRef{}, "working copy differs from the committed content (uncommitted local edit); commit or discard it first"
	}
	client, err := uploader.client()
	if err != nil {
		return lfs.FileRef{}, fmt.Sprintf("never uploaded and cannot upload now: %v", err)
	}
	uploaded, err := lfs.UploadBlob(client, content)
	if err != nil {
		return lfs.FileRef{}, fmt.Sprintf("never uploaded and upload failed: %v", err)
	}
	ref := uploaded.Ref()
	sessionDir := filepath.Join(ledgerPath, "sessions", sessionID)
	err = lfs.MutateSessionMeta(ctx, sessionDir, func(m *lfs.SessionMeta) (*lfs.SessionMeta, error) {
		if m == nil {
			return nil, errors.New("meta.json missing")
		}
		if m.Files == nil {
			m.Files = map[string]lfs.FileRef{}
		}
		m.Files[name] = ref
		return m, nil
	})
	if err != nil {
		return lfs.FileRef{}, fmt.Sprintf("uploaded but could not record the OID in meta.json: %v", err)
	}
	return ref, ""
}

func ledgerUpstream(ctx context.Context, ledgerPath string) (string, error) {
	out, err := exec.CommandContext(ctx, "git", "-C", ledgerPath, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}").Output()
	if err != nil {
		return "", errNoUpstream
	}
	return strings.TrimSpace(string(out)), nil
}

// sessionBlobsAhead calls visit for every regular file under sessions/ that HEAD adds or
// changes relative to upstream, with its blob content: the tree delta the push validator reads.
func sessionBlobsAhead(ctx context.Context, ledgerPath, upstream string, visit func(path string, blob []byte) error) error {
	raw, err := exec.CommandContext(ctx, "git", "-C", ledgerPath, "diff-tree", "-r", "-z", "--no-renames", "--raw", upstream, "HEAD", "--", "sessions/").Output()
	if err != nil {
		return fmt.Errorf("diff unpushed tree: %w", err)
	}
	tokens := strings.Split(string(raw), "\x00")
	for i := 0; i+1 < len(tokens); i += 2 {
		fields := strings.Fields(strings.TrimPrefix(tokens[i], ":"))
		path := tokens[i+1]
		if len(fields) < 5 || strings.HasPrefix(fields[4], "D") || fields[1] != "100644" && fields[1] != "100755" {
			continue
		}
		blob, err := exec.CommandContext(ctx, "git", "-C", ledgerPath, "cat-file", "blob", fields[3]).Output()
		if err != nil {
			return fmt.Errorf("read unpushed blob %s: %w", path, err)
		}
		if err := visit(path, blob); err != nil {
			return err
		}
	}
	return nil
}

// rawSessionArtifactsAhead lists content artifacts that HEAD changes relative to
// upstream and holds as raw content.
func rawSessionArtifactsAhead(ctx context.Context, ledgerPath, upstream string) ([]rawSessionArtifact, error) {
	var found []rawSessionArtifact
	err := sessionBlobsAhead(ctx, ledgerPath, upstream, func(path string, blob []byte) error {
		parts := strings.Split(path, "/")
		if len(parts) < 3 || parts[0] != "sessions" || !lfs.IsContentArtifact(strings.Join(parts[2:], "/")) {
			return nil
		}
		if _, _, perr := pointer.Parse(string(blob)); perr == nil {
			return nil
		}
		if meta := headSessionMeta(ctx, ledgerPath, parts[1]); meta.StoredInGit(strings.Join(parts[2:], "/")) {
			return nil // Storage=git: raw content is the correct state
		}
		found = append(found, rawSessionArtifact{path: path, content: blob})
		return nil
	})
	return found, err
}

// headSessionMeta reads the session manifest from HEAD, not the working tree, so
// the evidence is exactly what the unpushed commits hold.
func headSessionMeta(ctx context.Context, ledgerPath, sessionID string) *lfs.SessionMeta {
	out, err := exec.CommandContext(ctx, "git", "-C", ledgerPath, "show", "HEAD:sessions/"+sessionID+"/meta.json").Output()
	if err != nil {
		return nil
	}
	var meta lfs.SessionMeta
	if json.Unmarshal(out, &meta) != nil {
		return nil
	}
	return &meta
}

// upstreamPointer returns the pointer the upstream branch holds at path, or nil.
func upstreamPointer(ctx context.Context, ledgerPath, upstream, path string) *lfs.FileRef {
	out, err := exec.CommandContext(ctx, "git", "-C", ledgerPath, "show", upstream+":"+path).Output()
	if err != nil {
		return nil
	}
	oid, size, err := lfs.ParsePointer(string(out))
	if err != nil {
		return nil
	}
	return &lfs.FileRef{Storage: lfs.StorageLFS, OID: oid, Size: size}
}
