package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/pkg/sessionprovenance"
	"github.com/stretchr/testify/require"
)

func importExtensionFixture(t *testing.T) (*importCandidate, *sessionprovenance.Record, *lfs.SessionMeta, []byte) {
	t.Helper()
	now := time.Now().Add(-time.Hour).UTC()
	root := t.TempDir()
	importTestGit(t, root, "init")
	path := historyFixture(t, t.TempDir(), "sessions", "019c6d2e-27b0-798d-aaed-b036114dc63a", root, "cli", "first", now)
	old, err := codexImportAdapter.Stream(context.Background(), path, nil)
	require.NoError(t, err)
	original, err := os.ReadFile(path)
	require.NoError(t, err)
	tail := []byte("{\"timestamp\":\"2026-09-16T12:00:00Z\",\"type\":\"response_item\",\"payload\":{\"type\":\"message\",\"role\":\"user\",\"content\":[{\"type\":\"input_text\",\"text\":\"continued\"}]}}\n")
	require.NoError(t, os.WriteFile(path, append(append([]byte{}, original...), tail...), 0600))
	require.NoError(t, os.Chtimes(path, now, now))
	snap, err := codexImportAdapter.Stream(context.Background(), path, nil)
	require.NoError(t, err)
	c := &importCandidate{Snapshot: snap, SessionName: importedName("repo_test", old)}
	raw := []byte("{\"type\":\"user\",\"content\":\"first\"}\n")
	file := lfs.NewFileRef(raw)
	record := &sessionprovenance.Record{Version: 1, Agent: "codex", NativeSessionID: c.NativeID, Generation: c.Generation, Coverage: []sessionprovenance.Coverage{{Start: 0, End: old.Size, SessionName: c.SessionName, RawOID: file.BareOID()}}}
	meta := &lfs.SessionMeta{RepoID: "repo_test", SessionName: c.SessionName, Files: map[string]lfs.FileRef{"raw.jsonl": file}, Source: &sessionprovenance.Source{Version: 1, Agent: "codex", NativeSessionID: c.NativeID, Generation: c.Generation, SnapshotDigest: old.Digest, Ranges: []sessionprovenance.Range{{Start: 0, End: old.Size}}, ImportedAt: &now}}
	return c, record, meta, raw
}

func TestImportExtensionRequiresExactPrefixAndUnambiguousCoverage(t *testing.T) {
	for _, kind := range []string{"append", "changed_prefix", "truncated", "generation", "deleted", "paused", "multiple", "missing_metadata", "missing_import_proof"} {
		t.Run(kind, func(t *testing.T) {
			c, r, m, _ := importExtensionFixture(t)
			switch kind {
			case "changed_prefix":
				b, e := os.ReadFile(c.Path)
				require.NoError(t, e)
				require.NoError(t, os.WriteFile(c.Path, []byte(strings.Replace(string(b), "first", "other", 1)), 0600))
				info, e := os.Stat(c.Path)
				require.NoError(t, e)
				c.ModifiedAt = info.ModTime()
			case "truncated":
				require.NoError(t, os.Truncate(c.Path, r.Coverage[0].End-1))
			case "generation":
				r.Generation = strings.Repeat("f", 64)
			case "deleted":
				r.Exclusions = []sessionprovenance.Exclusion{{Start: 0, End: -1, Reason: "deleted"}}
			case "paused":
				r.Exclusions = []sessionprovenance.Exclusion{{Start: r.Coverage[0].End, End: c.Size, Reason: "paused"}}
			case "multiple":
				r.Coverage = append(r.Coverage, r.Coverage[0])
			case "missing_metadata":
				m = nil
			case "missing_import_proof":
				m.Source.ImportedAt = nil
			}
			err := validateImportExtension(c, r, m, "repo_test")
			if kind == "append" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestImportExtensionVerifiesRemoteBlobAndRetainsCanonicalName(t *testing.T) {
	_, repo := createBareAndClone(t)
	c, r, m, raw := importExtensionFixture(t)
	dir := filepath.Join(repo, "sessions", c.SessionName)
	require.NoError(t, os.MkdirAll(dir, 0700))
	require.NoError(t, lfs.WriteSessionMetaOnly(dir, m))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "raw.jsonl"), []byte(lfs.FormatPointer(m.Files["raw.jsonl"].OID, int64(len(raw)))), 0600))
	require.NoError(t, session.WriteSourceRecord(repo, r))
	runGit(t, repo, "add", ".")
	runGit(t, repo, "commit", "-m", "prior import fixture")
	var server *httptest.Server
	available := true
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if strings.HasSuffix(req.URL.Path, "/objects/batch") {
			file := m.Files["raw.jsonl"]
			_ = json.NewEncoder(w).Encode(lfs.BatchResponse{Objects: []lfs.BatchResponseObject{{OID: file.BareOID(), Size: file.Size, Actions: &lfs.Actions{Download: &lfs.Action{Href: server.URL + "/blob"}}}}})
			return
		}
		if !available {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write(raw)
	}))
	defer server.Close()
	client := lfs.NewClient(server.URL, "test", "test")
	prior, err := verifyImportExtension(context.Background(), repo, "HEAD", importDestination{RepoID: "repo_test"}, c, client)
	require.NoError(t, err)
	require.Equal(t, m.EffectiveSessionID(), prior.EffectiveSessionID())
	require.Equal(t, c.SessionName, prior.SessionName)
	// A retry after a local source-record commit still proves against the old
	// immutable remote ref, never the just-written local coverage.
	oldRef := runGit(t, repo, "rev-parse", "HEAD")
	r.Coverage[0].End = c.Size
	require.NoError(t, session.WriteSourceRecord(repo, r))
	runGit(t, repo, "add", ".")
	runGit(t, repo, "commit", "-m", "interrupted replacement fixture")
	_, err = verifyImportExtension(context.Background(), repo, oldRef, importDestination{RepoID: "repo_test"}, c, client)
	require.NoError(t, err)
	available = false
	_, err = verifyImportExtension(context.Background(), repo, oldRef, importDestination{RepoID: "repo_test"}, c, client)
	require.ErrorContains(t, err, "verification failed")
}

func TestImportExtensionJournalCrashBoundaries(t *testing.T) {
	c, _, m, _ := importExtensionFixture(t)
	j := importJournal{Version: 1, SnapshotDigest: m.Source.SnapshotDigest, UploadVerified: true}
	require.NoError(t, validateImportJournalTransition(&j, c, m))
	j.UploadVerified = false
	require.Error(t, validateImportJournalTransition(&j, c, m), "unfinished older snapshot cannot be superseded")
	j.SnapshotDigest = c.Digest
	require.NoError(t, validateImportJournalTransition(&j, c, m), "same in-flight snapshot resumes before push")
	j.UploadVerified = true
	require.NoError(t, validateImportJournalTransition(&j, c, nil), "already verified exact snapshot remains idempotent")
	j.SnapshotDigest = m.Source.SnapshotDigest
	require.Error(t, validateImportJournalTransition(&j, c, nil), "missing old remote receipt never authorizes recreation")
}

func TestImportExtensionInvalidatesOnlySelectedProjectionAndDerivedArtifacts(t *testing.T) {
	c, r, _, _ := importExtensionFixture(t)
	r.Extra = map[string]json.RawMessage{"future": json.RawMessage(`{"retain":true}`), "projections": json.RawMessage(`{"other":{"raw_oid":"retain"},"` + c.SessionName + `":{"raw_oid":"obsolete"}}`)}
	r.ProjectionRevision = "obsolete"
	require.NoError(t, invalidateImportProjection(r, c.SessionName))
	require.Empty(t, r.ProjectionRevision)
	require.JSONEq(t, `{"other":{"raw_oid":"retain"}}`, string(r.Extra["projections"]))
	require.JSONEq(t, `{"retain":true}`, string(r.Extra["future"]))
	dir := t.TempDir()
	for _, name := range []string{"raw.jsonl", "summary.json", "summary.md", "session.md", "plan.md", "context-trace.jsonl", ".import-journal.json"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("keep or remove"), 0600))
	}
	require.NoError(t, removeImportDerivedFiles(dir))
	require.FileExists(t, filepath.Join(dir, "raw.jsonl"))
	require.FileExists(t, filepath.Join(dir, ".import-journal.json"))
	require.NoFileExists(t, filepath.Join(dir, "summary.json"))
	require.NoFileExists(t, filepath.Join(dir, "summary.md"))
}

func TestImportExtensionPublishesIntactReplacementAndRecoversPushCrash(t *testing.T) {
	bare, repo := createBareAndClone(t)
	isolatePushEnv(t, repo)
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".git", "info", "exclude"), []byte(".sageox/\n"), 0600))
	c, r, m, raw := importExtensionFixture(t)
	dir := filepath.Join(repo, "sessions", c.SessionName)
	require.NoError(t, os.MkdirAll(dir, 0700))
	require.NoError(t, lfs.WriteSessionMetaOnly(dir, m))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "raw.jsonl"), []byte(lfs.FormatPointer(m.Files["raw.jsonl"].OID, int64(len(raw)))), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "summary.json"), []byte(`{"summary":"obsolete"}`), 0600))
	r.Extra = map[string]json.RawMessage{"projections": json.RawMessage(`{"` + c.SessionName + `":{"raw_oid":"` + m.Files["raw.jsonl"].BareOID() + `"}}`)}
	require.NoError(t, session.WriteSourceRecord(repo, r))
	runGit(t, repo, "add", ".")
	runGit(t, repo, "commit", "-m", "prior import fixture")
	runGit(t, repo, "push")
	d := importDestination{RepoID: "repo_test"}
	cache := filepath.Dir(importJournalPath(repo, c.SessionName))
	require.NoError(t, os.MkdirAll(cache, 0700))
	require.NoError(t, writeImportJournal(importJournalPath(repo, c.SessionName), &importJournal{Version: 1, Destination: d, NativeID: c.NativeID, Generation: c.Generation, SnapshotDigest: m.Source.SnapshotDigest, SessionName: c.SessionName, PublishedAt: *m.Source.ImportedAt, UploadVerified: true}))
	var mu sync.Mutex
	blobs := map[string][]byte{m.Files["raw.jsonl"].BareOID(): raw}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if strings.HasSuffix(req.URL.Path, "/objects/batch") {
			var request struct {
				Operation string            `json:"operation"`
				Objects   []lfs.BatchObject `json:"objects"`
			}
			if err := json.NewDecoder(req.Body).Decode(&request); err != nil {
				w.WriteHeader(400)
				return
			}
			response := lfs.BatchResponse{}
			for _, o := range request.Objects {
				actions := &lfs.Actions{}
				if request.Operation == "download" {
					actions.Download = &lfs.Action{Href: server.URL + "/blob/" + o.OID}
				} else if blobs[o.OID] == nil {
					actions.Upload = &lfs.Action{Href: server.URL + "/blob/" + o.OID}
				}
				response.Objects = append(response.Objects, lfs.BatchResponseObject{OID: o.OID, Size: o.Size, Actions: actions})
			}
			_ = json.NewEncoder(w).Encode(response)
			return
		}
		oid := strings.TrimPrefix(req.URL.Path, "/blob/")
		if req.Method == http.MethodPut {
			b, e := io.ReadAll(req.Body)
			if e != nil {
				w.WriteHeader(500)
				return
			}
			blobs[oid] = b
			return
		}
		b, ok := blobs[oid]
		if !ok {
			w.WriteHeader(404)
			return
		}
		_, _ = w.Write(b)
	}))
	defer server.Close()
	client := lfs.NewClient(server.URL, "test", "test")
	original, err := os.ReadFile(c.Path)
	require.NoError(t, err)
	// Reject the first push after the scoped commit, then retry exactly the same
	// snapshot. The journal must retain the new publication clock and identity.
	hook := filepath.Join(bare, "hooks", "pre-receive")
	require.NoError(t, os.WriteFile(hook, []byte("#!/bin/sh\nexit 1\n"), 0700))
	err = publishValidatedImport(context.Background(), t.TempDir(), repo, d, c, client)
	require.Error(t, err)
	t.Logf("interrupted publication: %v", err)
	var journal importJournal
	b, err := os.ReadFile(importJournalPath(repo, c.SessionName))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(b, &journal))
	require.Equal(t, c.Digest, journal.SnapshotDigest)
	require.False(t, journal.UploadVerified)
	publication := journal.PublishedAt
	require.NoError(t, os.Remove(hook))
	c.Status = "already_uploaded"
	require.NoError(t, publishValidatedImport(context.Background(), t.TempDir(), repo, d, c, client))
	b, err = os.ReadFile(importJournalPath(repo, c.SessionName))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(b, &journal))
	require.True(t, journal.UploadVerified)
	require.Equal(t, publication, journal.PublishedAt)
	published, err := lfs.ReadSessionMeta(dir)
	require.NoError(t, err)
	require.Equal(t, m.EffectiveSessionID(), published.EffectiveSessionID())
	require.Equal(t, m.Source.ImportedAt, published.Source.ImportedAt)
	require.Equal(t, c.Digest, published.Source.SnapshotDigest)
	require.Equal(t, "pending", published.SummaryStatus)
	require.NoFileExists(t, filepath.Join(dir, "summary.json"))
	record, err := session.ReadSourceRecord(repo, c.NativeID)
	require.NoError(t, err)
	require.Len(t, record.Coverage, 1)
	require.Equal(t, c.Size, record.Coverage[0].End)
	require.JSONEq(t, `{}`, string(record.Extra["projections"]))
	mu.Lock()
	content := string(blobs[published.Files["raw.jsonl"].BareOID()])
	mu.Unlock()
	require.Contains(t, content, "first")
	require.Contains(t, content, "continued")
	tip := runGit(t, repo, "rev-parse", "HEAD")
	require.NoError(t, publishValidatedImport(context.Background(), t.TempDir(), repo, d, c, client))
	require.Equal(t, tip, runGit(t, repo, "rev-parse", "HEAD"))
	// A completed older local journal must also converge when a second
	// machine already published this exact extension under the stable name.
	journal.SnapshotDigest = m.Source.SnapshotDigest
	require.NoError(t, writeImportJournal(importJournalPath(repo, c.SessionName), &journal))
	require.NoError(t, publishValidatedImport(context.Background(), t.TempDir(), repo, d, c, client))
	require.Equal(t, tip, runGit(t, repo, "rev-parse", "HEAD"))
	after, err := os.ReadFile(c.Path)
	require.NoError(t, err)
	require.Equal(t, original, after)
}

func TestImportExtensionPreviewKeepsCanonicalNameAndDefersActiveSource(t *testing.T) {
	c, r, m, _ := importExtensionFixture(t)
	ledger := t.TempDir()
	t.Setenv("CODEX_HOME", filepath.Dir(filepath.Dir(c.Path)))
	require.NoError(t, os.MkdirAll(filepath.Join(ledger, "sessions", c.SessionName), 0700))
	require.NoError(t, lfs.WriteSessionMetaOnly(filepath.Join(ledger, "sessions", c.SessionName), m))
	require.NoError(t, session.WriteSourceRecord(ledger, r))
	report, err := scanImport(context.Background(), c.CWD, ledger, importDestination{RepoID: "repo_test"}, &importOptions{}, time.Now())
	require.NoError(t, err)
	require.Len(t, report.Candidates, 1)
	require.Equal(t, "ready", report.Candidates[0].Status)
	require.Equal(t, c.SessionName, report.Candidates[0].SessionName)
	now := time.Now()
	require.NoError(t, os.Chtimes(c.Path, now, now))
	report, err = scanImport(context.Background(), c.CWD, ledger, importDestination{RepoID: "repo_test"}, &importOptions{}, now)
	require.NoError(t, err)
	require.Len(t, report.Candidates, 1)
	require.Equal(t, "active", report.Candidates[0].Status)
}
