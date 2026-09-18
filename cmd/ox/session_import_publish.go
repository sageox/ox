package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/identity"
	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/sessionregistration"
	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/sageox/ox/pkg/sessionprovenance"
)

type importJournal struct {
	Version             int               `json:"version"`
	Destination         importDestination `json:"destination"`
	NativeID            string            `json:"native_session_id"`
	Generation          string            `json:"generation"`
	SnapshotDigest      string            `json:"snapshot_digest"`
	SessionName         string            `json:"session_name"`
	PublishedAt         time.Time         `json:"published_at"`
	UploadVerified      bool              `json:"upload_verified"`
	RegistrationPending bool              `json:"registration_pending"`
}

func importJournalPath(ledger, name string) string {
	return filepath.Join(ledger, ".sageox/cache/sessions", name, ".import-journal.json")
}
func writeImportJournal(path string, j *importJournal) error {
	if err := fileutil.AtomicWriteJSON(path, j, 0600); err != nil {
		return err
	}
	if j.RegistrationPending {
		ledger := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(path)))))
		return sessionregistration.Enqueue(ledger, sessionregistration.Job{Endpoint: j.Destination.Endpoint, RepoID: j.Destination.RepoID, SessionName: j.SessionName, SourceDigest: j.SnapshotDigest})
	}
	return nil
}
func importReadBlob(ctx context.Context, ledger, ref, path string) ([]byte, bool, error) {
	s, e := importGit(ctx, ledger, "ls-tree", ref, "--", path)
	if e != nil {
		return nil, false, e
	}
	if s == "" {
		return nil, false, nil
	}
	// Resolve an immutable blob once: bounding a moving ref before reading it
	// would allow a concurrent update to substitute an unbounded object.
	fields := strings.Fields(strings.SplitN(s, "\t", 2)[0])
	if len(fields) != 3 || fields[1] != "blob" || (fields[0] != "100644" && fields[0] != "100755") {
		return nil, true, fmt.Errorf("receipt is not a regular Git blob")
	}
	object := fields[2]
	sizeText, e := importGit(ctx, ledger, "cat-file", "-s", object)
	if e != nil {
		return nil, true, e
	}
	size, e := strconv.ParseInt(sizeText, 10, 64)
	// Match the shared publication verifier's 4MiB metadata limit. These are
	// content-free receipts/pointers; transcript bytes always travel through LFS.
	if e != nil || size < 0 || size > 4*1024*1024 {
		return nil, true, fmt.Errorf("import receipt exceeds metadata limit")
	}
	b, e := exec.CommandContext(ctx, "git", "-C", ledger, "cat-file", "blob", object).Output()
	return b, true, e
}
func readImportRemoteRecord(ctx context.Context, ledger, ref, nativeID string) (*sessionprovenance.Record, error) {
	rel, e := sessionprovenance.Path(nativeID)
	if e != nil {
		return nil, e
	}
	b, ok, e := importReadBlob(ctx, ledger, ref, rel)
	if e != nil || !ok {
		return nil, e
	}
	var r sessionprovenance.Record
	if e = json.Unmarshal(b, &r); e != nil {
		return nil, e
	}
	if e = r.Validate(); e != nil {
		return nil, e
	}
	if r.NativeSessionID != nativeID {
		return nil, fmt.Errorf("remote source identity mismatch")
	}
	// Read from the codex namespace; the contract validates agent shape only.
	if r.Agent != "codex" {
		return nil, fmt.Errorf("remote source agent mismatch")
	}
	return &r, nil
}

// checkImportTree refuses unrelated staged, untracked, conflicted, or modified
// paths. A journal authorizes only this import's exact paths after a crash.
func checkImportTree(ctx context.Context, ledger, name, nativeID string, resuming bool) error {
	b, e := exec.CommandContext(ctx, "git", "-C", ledger, "status", "--porcelain=v1", "-z", "--untracked-files=all").Output()
	if e != nil {
		return e
	}
	rel, _ := sessionprovenance.Path(nativeID)
	for _, line := range strings.Split(string(b), "\x00") {
		if line == "" {
			continue
		}
		if len(line) < 4 {
			return fmt.Errorf("unrecognized Ledger status")
		}
		status, p := line[:2], line[3:]
		if strings.ContainsAny(status, "URC") || status == "AA" || status == "DD" {
			return fmt.Errorf("ledger has unresolved state; preserve and repair it before importing")
		}
		own := p == rel || p == "sessions/.gitignore" || strings.HasPrefix(p, "sessions/"+name+"/")
		if !resuming || !own {
			return fmt.Errorf("ledger has unrelated pending changes; preserve and repair it before importing")
		}
	}
	return nil
}
func refreshImportLedger(ctx context.Context, ledger string) (string, string, error) {
	branch, e := importGit(ctx, ledger, "symbolic-ref", "--short", "HEAD")
	if e != nil {
		return "", "", e
	}
	if strings.HasPrefix(branch, "-") {
		return "", "", fmt.Errorf("invalid Ledger branch")
	}
	if _, e = importGit(ctx, ledger, "fetch", "--no-tags", "origin", branch); e != nil {
		return "", "", e
	}
	ref, e := importGit(ctx, ledger, "rev-parse", "FETCH_HEAD")
	if e != nil {
		return "", "", e
	}
	return branch, ref, nil
}
func validateImportDestination(root, ledger string, d importDestination) (*lfs.Client, error) {
	token, e := auth.EnsureValidTokenForEndpoint(d.Endpoint, 300)
	if e != nil || token == nil {
		return nil, fmt.Errorf("destination authentication unavailable")
	}
	detail, e := api.NewRepoClientWithEndpoint(d.Endpoint).WithAuthToken(token.AccessToken).GetRepoDetail(d.RepoID)
	if e != nil || detail == nil || detail.IsReadOnly() || detail.Ledger == nil || detail.Ledger.Status != "ready" {
		return nil, fmt.Errorf("destination repository write access could not be verified")
	}
	if err := checkImportTeam(detail, d.TeamID); err != nil {
		return nil, err
	}
	remote, e := importGit(context.Background(), ledger, "remote", "get-url", "origin")
	if e != nil {
		return nil, e
	}
	canonical := func(s string) string {
		u, e := url.Parse(s)
		if e != nil {
			return ""
		}
		u.User = nil
		return strings.TrimSuffix(strings.TrimSuffix(u.String(), "/"), ".git")
	}
	if canonical(remote) == "" || canonical(remote) != canonical(detail.Ledger.RepoURL) {
		return nil, fmt.Errorf("local Ledger remote does not match the authorized repository")
	}
	return getLFSClient(root)
}
func verifyImportReceipt(ctx context.Context, ledger, ref string, d importDestination, c *importCandidate, client *lfs.Client) (bool, error) {
	r, e := readImportRemoteRecord(ctx, ledger, ref, c.NativeID)
	if e != nil {
		return false, e
	}
	if r == nil {
		return false, nil
	}
	if r.Excludes(0, c.Size) {
		return false, fmt.Errorf("native history is excluded by recording intent")
	}
	if r.Generation != c.Generation {
		return false, fmt.Errorf("source generation differs; refresh and review instead of merging coverage")
	}
	for _, coverage := range r.Coverage {
		if coverage.Start != 0 || coverage.End != c.Size {
			continue
		}
		if coverage.SessionName != c.SessionName {
			return false, fmt.Errorf("source maps to a different session; refresh preview")
		}
		b, ok, e := importReadBlob(ctx, ledger, ref, "sessions/"+coverage.SessionName+"/meta.json")
		if e != nil {
			return false, e
		}
		if !ok {
			return false, fmt.Errorf("coverage exists but session metadata is missing; refusing resurrection")
		}
		var meta lfs.SessionMeta
		if e = json.Unmarshal(b, &meta); e != nil {
			return false, e
		}
		raw, ok := meta.Files["raw.jsonl"]
		if !ok || raw.BareOID() != coverage.RawOID || meta.RepoID != d.RepoID || meta.Source == nil || meta.Source.NativeSessionID != c.NativeID || meta.Source.Generation != c.Generation || meta.Source.SnapshotDigest != c.Digest {
			return false, fmt.Errorf("remote session does not match its source receipt")
		}
		pointer, exists, e := importReadBlob(ctx, ledger, ref, "sessions/"+coverage.SessionName+"/raw.jsonl")
		if e != nil {
			return false, e
		}
		if !exists || !strings.Contains(string(pointer), "oid sha256:"+raw.BareOID()+"\n") {
			return false, fmt.Errorf("remote raw pointer is missing or inconsistent")
		}
		batch, e := client.BatchDownloadContext(ctx, []lfs.BatchObject{{OID: raw.BareOID(), Size: raw.Size}})
		if e != nil {
			return false, e
		}
		if len(batch.Objects) != 1 || batch.Objects[0].Error != nil || batch.Objects[0].Actions == nil || batch.Objects[0].Actions.Download == nil {
			return false, fmt.Errorf("remote raw object is unavailable")
		}
		if e = lfs.DownloadToFileContext(ctx, batch.Objects[0].Actions.Download, io.Discard, true, raw.BareOID()); e != nil {
			return false, fmt.Errorf("remote raw object verification failed")
		}
		return true, nil
	}
	if len(r.Coverage) > 0 {
		return false, fmt.Errorf("existing partial coverage requires explicit reconciliation")
	}
	return false, nil
}
func publishImportedSession(ctx context.Context, root, ledger string, d importDestination, c *importCandidate) error {
	if pending, err := pendingLocalDeletion(ledger, d.RepoID, c.NativeID); err != nil {
		return err
	} else if pending {
		return fmt.Errorf("source has pending local deletion or recording exclusion")
	}
	client, e := validateImportDestination(root, ledger, d)
	if e != nil {
		return e
	}
	return publishValidatedImport(ctx, root, ledger, d, c, client)
}

// The authorized destination is fixed before acquiring the Ledger transaction
// lock. Tests exercise this same transaction against an isolated Git/LFS server.
func publishValidatedImport(ctx context.Context, root, ledger string, d importDestination, c *importCandidate, client *lfs.Client) error {
	return session.WithPublicationLock(ctx, ledger, c.SessionName, func() error {
		return publishLockedImport(ctx, root, ledger, d, c, client)
	})
}

func publishLockedImport(ctx context.Context, root, ledger string, d importDestination, c *importCandidate, client *lfs.Client) error {
	journalPath := importJournalPath(ledger, c.SessionName)
	cache := filepath.Dir(journalPath)
	var j importJournal
	resuming := false
	b, e := os.ReadFile(journalPath)
	if e == nil {
		if e = json.Unmarshal(b, &j); e != nil {
			return e
		}
		if j.Version != 1 || j.Destination != d || j.NativeID != c.NativeID || j.Generation != c.Generation || j.SessionName != c.SessionName {
			return fmt.Errorf("import journal destination or source changed")
		}
		resuming = j.SnapshotDigest == c.Digest
	} else if !errors.Is(e, os.ErrNotExist) {
		return e
	}
	return gitutil.WithRepoLock(ctx, ledger, func() error {
		if pending, err := pendingLocalDeletion(ledger, d.RepoID, c.NativeID); err != nil {
			return err
		} else if pending {
			return fmt.Errorf("source has pending local deletion or recording exclusion")
		}
		if e := checkImportTree(ctx, ledger, c.SessionName, c.NativeID, resuming); e != nil {
			return e
		}
		branch, ref, e := refreshImportLedger(ctx, ledger)
		if e != nil {
			return e
		}
		extension, e := verifyImportExtension(ctx, ledger, ref, d, c, client)
		if e != nil {
			return e
		}
		verified := false
		if extension == nil {
			verified, e = verifyImportReceipt(ctx, ledger, ref, d, c, client)
		}
		if e != nil {
			return e
		}
		// Another machine may already have extended this canonical session.
		// A fresh exact remote receipt can retire a completed older journal;
		// an unfinished transaction still needs explicit reconciliation.
		if !verified || !j.UploadVerified {
			if e = validateImportJournalTransition(&j, c, extension); e != nil {
				return e
			}
		}
		if verified {
			if err := finishVerifiedImportConvergence(ctx, ledger, branch, ref, d, c, client, resuming); err != nil {
				return err
			}
			if !resuming {
				j = importJournal{Version: 1, Destination: d, NativeID: c.NativeID, Generation: c.Generation, SnapshotDigest: c.Digest, SessionName: c.SessionName, PublishedAt: time.Now().UTC()}
				if e = os.MkdirAll(cache, 0700); e != nil {
					return e
				}
			}
			j.UploadVerified = true
			j.RegistrationPending = true
			if e = writeImportJournal(journalPath, &j); e != nil {
				return e
			}
			if cached, err := lfs.ReadSessionMeta(cache); err == nil && cached.Source != nil && cached.Source.SnapshotDigest == c.Digest && cached.SummaryStatus == "pending" {
				if e = session.WriteNeedsSummaryMarker(cache, filepath.Join(cache, "raw.jsonl"), filepath.Join(ledger, "sessions", c.SessionName)); e != nil {
					return e
				}
			}
			return retryImportRegistration(ctx, root, ledger, &j)
		}
		if err := checkUnverifiedImportResume(c.Status, resuming, &j); err != nil {
			return err
		}
		// Fast-forward only: privacy records never pass through a positional resolver.
		if _, e = importGit(ctx, ledger, "merge", "--ff-only", ref); e != nil {
			return fmt.Errorf("ledger diverged; import remains pending for explicit reconciliation")
		}
		current, e := session.ReadSourceRecord(ledger, c.NativeID)
		if e != nil {
			return e
		}
		if current != nil {
			if current.Excludes(0, c.Size) {
				return fmt.Errorf("source is excluded")
			}
			if current.Generation != "" && current.Generation != c.Generation {
				return fmt.Errorf("source generation differs")
			}
			for _, cov := range current.Coverage {
				if cov.SessionName != c.SessionName || cov.Start != 0 || (cov.End != c.Size && (extension == nil || cov.End != extension.Source.Ranges[0].End)) {
					return fmt.Errorf("source coverage needs explicit reconciliation")
				}
			}
		}
		if !resuming {
			if e = os.MkdirAll(cache, 0700); e != nil {
				return e
			}
			j = importJournal{Version: 1, Destination: d, NativeID: c.NativeID, Generation: c.Generation, SnapshotDigest: c.Digest, SessionName: c.SessionName, PublishedAt: time.Now().UTC(), RegistrationPending: true}
			if e = writeImportJournal(journalPath, &j); e != nil {
				return e
			}
		}
		// Conversion is regenerated from the verified source snapshot after any crash,
		// avoiding a second independently durable cursor and partial-output dedup.
		if _, e = session.NewRawStreamWriter(io.Discard, root); e != nil {
			return e
		}
		rawPath := filepath.Join(cache, "raw.jsonl")
		w, e := session.NewRawSnapshotWriter(rawPath, root)
		if e != nil {
			return e
		}
		if e = w.WriteRaw(map[string]any{"type": "header", "version": "1.0", "agent_type": "codex", "started_at": c.StartedAt, "source_native_session_id": c.NativeID}); e != nil {
			_ = w.CloseAndSync()
			return e
		}
		snap, e := codexImportAdapter.Stream(ctx, c.Path, func(raw adapterprotocol.RawEntry) error { entry := importEntry(raw); return w.WriteEntry(&entry) })
		closeErr := w.CloseAndSync()
		if e != nil {
			return e
		}
		if closeErr != nil {
			return closeErr
		}
		if snap.Generation != c.Generation || snap.Digest != c.Digest || snap.Size != c.Size {
			return fmt.Errorf("source changed since preview; preview again")
		}
		meta := lfs.NewSessionMeta(c.SessionName, identity.AttributionDisplayName(d.Endpoint, config.GetDisplayName()), "", "codex", c.StartedAt).RepoID(d.RepoID).UserID(d.UserID).EntryCount(c.Entries).Title(c.Title).Build()
		meta.Source = &sessionprovenance.Source{Version: 1, Agent: "codex", NativeSessionID: c.NativeID, Generation: c.Generation, SnapshotDigest: c.Digest, Ranges: []sessionprovenance.Range{{Start: 0, End: c.Size}}, ParserVersion: codexImportAdapter.ParserVersion(), ParentSessionID: c.ParentID, CapturedAt: c.StartedAt, ImportedAt: &j.PublishedAt}
		meta.PublishedAt = &j.PublishedAt
		meta.SummaryStatus = "pending"
		meta.ProcessingStatus = "pending"
		meta.SessionID = meta.EffectiveSessionID()
		if extension != nil {
			meta.SessionID = extension.EffectiveSessionID()
			meta.Source.ImportedAt = extension.Source.ImportedAt
			if e = removeImportDerivedFiles(cache); e != nil {
				return e
			}
		}
		if e = lfs.WriteSessionMetaOnly(cache, meta); e != nil {
			return e
		}
		refs, e := lfs.UploadSessionFilesContext(ctx, client, cache, nil)
		if e != nil {
			return e
		}
		meta.Files = refs
		sessionDir := filepath.Join(ledger, "sessions", c.SessionName)
		if e = os.MkdirAll(sessionDir, 0700); e != nil {
			return e
		}
		if extension != nil {
			if e = removeImportDerivedFiles(sessionDir); e != nil {
				return e
			}
		}
		if e = lfs.WriteSessionMetaOnly(sessionDir, meta); e != nil {
			return e
		}
		if _, e = lfs.WritePointerFiles(sessionDir, lfs.AssertUploadedManifest(refs)); e != nil {
			return e
		}
		if e = ensureSessionsGitignore(filepath.Join(ledger, "sessions")); e != nil {
			return e
		}
		if current == nil {
			current = &sessionprovenance.Record{Version: 1, Agent: "codex", NativeSessionID: c.NativeID}
		}
		if extension != nil {
			if e = invalidateImportProjection(current, c.SessionName); e != nil {
				return e
			}
		}
		current.Generation = c.Generation
		current.UpdatedAt = j.PublishedAt
		current.Coverage = []sessionprovenance.Coverage{{Start: 0, End: c.Size, SessionName: c.SessionName, RawOID: refs["raw.jsonl"].BareOID()}}
		if e = session.WriteSourceRecord(ledger, current); e != nil {
			return e
		}
		sourcePath, _ := sessionprovenance.Path(c.NativeID)
		scope := []string{"sessions/" + c.SessionName, sourcePath, "sessions/.gitignore"}
		if _, e = importGit(ctx, ledger, append([]string{"add", "--"}, scope...)...); e != nil {
			return e
		}
		if pending, err := pendingLocalDeletion(ledger, d.RepoID, c.NativeID); err != nil {
			return err
		} else if pending {
			return fmt.Errorf("source has pending local deletion or recording exclusion")
		}
		if _, e = gitutil.CommitLedgerSnapshot(ctx, ledger, "import Codex session "+c.NativeID, scope...); e != nil {
			return e
		}
		_, pushErr := importGit(ctx, ledger, "push", "origin", "HEAD:"+branch)
		_, ref, e = refreshImportLedger(ctx, ledger)
		if e != nil {
			return e
		}
		verified, e = verifyImportReceipt(ctx, ledger, ref, d, c, client)
		if e != nil {
			return e
		}
		if !verified {
			if pushErr != nil {
				return fmt.Errorf("publication remains pending after push failure: %w", pushErr)
			}
			return fmt.Errorf("remote publication could not be verified")
		}
		if err := finishVerifiedImportConvergence(ctx, ledger, branch, ref, d, c, client, true); err != nil {
			return err
		}
		j.UploadVerified = true
		if e = writeImportJournal(journalPath, &j); e != nil {
			return e
		}
		if e = session.WriteNeedsSummaryMarker(cache, rawPath, sessionDir); e != nil {
			return e
		}
		return retryImportRegistration(ctx, root, ledger, &j)
	})
}

// Upload success and registration success are independent. Keep the journal
// pending after a 204 notification until the server proves its layer exists.
func retryImportRegistration(ctx context.Context, root, ledger string, j *importJournal) error {
	job := sessionregistration.Job{Endpoint: j.Destination.Endpoint, RepoID: j.Destination.RepoID, SessionName: j.SessionName, SourceDigest: j.SnapshotDigest}
	if err := sessionregistration.Enqueue(ledger, job); err != nil {
		return err
	}
	ready, _ := sessionregistration.Retry(ctx, ledger, job)
	if ready {
		j.RegistrationPending = false
		return writeImportJournal(importJournalPath(ledger, j.SessionName), j)
	}
	return nil
}

// A local receipt can precede its first push. Only the matching durable journal
// authorizes completing that transaction; a previously verified missing remote
// session is a deletion/repair case, never permission to recreate it.
func checkUnverifiedImportResume(status string, resuming bool, journal *importJournal) error {
	if status == "already_uploaded" && (!resuming || journal.UploadVerified) {
		return fmt.Errorf("local coverage has no remote receipt; refusing an automatic replacement")
	}
	return nil
}
