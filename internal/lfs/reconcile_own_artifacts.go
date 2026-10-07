package lfs

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/internal/gitutil"
)

type reconcileOwnerKey struct{}

// withReconcileOwner records the privacy-safe username of the current coworker,
// the value recording stamps into meta.json, so reconcile can tell which staged
// session artifacts it may upload on the user's behalf.
func withReconcileOwner(ctx context.Context, username string) context.Context {
	return context.WithValue(ctx, reconcileOwnerKey{}, username)
}

func reconcileOwner(ctx context.Context) string {
	owner, _ := ctx.Value(reconcileOwnerKey{}).(string)
	return owner
}

// uploadOwnStagedPlainArtifacts repairs the deadlock where a session finalize
// staged plain content, its LFS upload then failed, and finalize paused while the
// push stayed wedged: the staged plain file makes the pre-validation below refuse,
// and only reconcile can un-wedge the push.
//
// For each staged session content artifact that is not a pointer and belongs to
// the current coworker, the bytes are kept in the Ledger cache, uploaded, recorded
// in meta.json, replaced in the working tree by the pointer, and re-staged. Files
// reconcile may not repair (a teammate's session, a draft) are returned, never
// touched.
func uploadOwnStagedPlainArtifacts(ctx context.Context, ledgerPath string, client *Client, logger *slog.Logger) (refused []string, err error) {
	out, err := gitPlumbing(ctx, ledgerPath, nil, "diff", "--cached", "--name-only", "-z", "--diff-filter=AM", "--", "sessions")
	if err != nil {
		return nil, fmt.Errorf("list staged session files: %w", err)
	}
	owner := reconcileOwner(ctx)
	for _, path := range strings.Split(string(out), "\x00") {
		parts := strings.Split(path, "/")
		if len(parts) != 3 || parts[0] != "sessions" || !isLedgerContentFile(parts[2]) {
			continue
		}
		sessionID, name := parts[1], parts[2]
		staged, err := gitPlumbing(ctx, ledgerPath, nil, "cat-file", "blob", ":"+path)
		if err != nil {
			return refused, fmt.Errorf("read staged %s: %w", path, err)
		}
		if pointerShaped(staged) {
			continue
		}
		sessionDir := filepath.Join(ledgerPath, "sessions", sessionID)
		meta, metaErr := ReadSessionMeta(sessionDir)
		if metaErr != nil || meta == nil || meta.IsDraft() || owner == "" || !strings.EqualFold(meta.Username, owner) {
			refused = append(refused, path)
			continue
		}
		if meta.Files[name].Storage == StorageGit {
			continue // registered as git content: plain bytes are correct here
		}

		cachePath := filepath.Join(ledgerPath, ".sageox", "cache", filepath.FromSlash(path))
		if err := PreserveInCache(cachePath, staged); err != nil {
			return refused, fmt.Errorf("keep %s in the Ledger cache: %w", path, err)
		}
		uploaded, err := UploadBlob(client, staged)
		if err != nil {
			return refused, fmt.Errorf("upload staged %s: %w", path, err)
		}
		ref := uploaded.Ref()
		err = MutateSessionMeta(ctx, sessionDir, func(m *SessionMeta) (*SessionMeta, error) {
			if m == nil {
				return nil, fmt.Errorf("meta.json for %s disappeared", sessionID)
			}
			if m.Files == nil {
				m.Files = map[string]FileRef{}
			}
			m.Files[name] = ref
			return m, nil
		})
		if err != nil {
			return refused, fmt.Errorf("record %s in meta.json: %w", path, err)
		}
		if err := WritePointerFile(filepath.Join(ledgerPath, filepath.FromSlash(path)), uploaded); err != nil {
			return refused, fmt.Errorf("write pointer for %s: %w", path, err)
		}
		if _, err := gitutil.RunGit(ctx, ledgerPath, "add", "--sparse", "--", path, "sessions/"+sessionID+"/meta.json"); err != nil {
			return refused, fmt.Errorf("stage pointer for %s: %w", path, err)
		}
		logger.Info("lfs reconcile: uploaded own staged session artifact and staged its pointer", "path", path, "oid", ref.OID)
	}
	return refused, nil
}
