// Package sessionregistration keeps indexing retries independent of summaries.
package sessionregistration

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/internal/lfs"
)

type Job struct {
	Endpoint     string `json:"endpoint"`
	RepoID       string `json:"repo_id"`
	SessionName  string `json:"session_name"`
	SourceDigest string `json:"source_digest,omitempty"`
}

func jobPath(ledger, name string) (string, error) {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\") {
		return "", fmt.Errorf("invalid session name")
	}
	// Summary finalization prunes the source cache. Registration outlives it.
	return filepath.Join(ledger, ".sageox", "cache", "session-registration", name+".json"), nil
}
func Enqueue(ledger string, job Job) error {
	path, err := jobPath(ledger, job.SessionName)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	return fileutil.WithFileLock(context.Background(), path, func() error {
		return fileutil.AtomicWriteJSON(path, job, 0600)
	})
}

// Retry only clears a job after authenticated metadata proves both the
// canonical conversation and required captured layer exist. HTTP 204 is not proof.
func Retry(ctx context.Context, ledger string, job Job) (bool, error) {
	path, err := jobPath(ledger, job.SessionName)
	if err != nil {
		return false, err
	}
	token, err := auth.EnsureValidTokenForEndpoint(job.Endpoint, 300)
	if err != nil || token == nil {
		return false, fmt.Errorf("registration authentication unavailable")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(job.Endpoint, "/")+"/api/v1/repos/"+url.PathEscape(job.RepoID)+"/sessions/"+url.PathEscape(job.SessionName), nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	req.Header.Set("Cache-Control", "no-cache")
	// Bounded per-job I/O keeps deterministic daemon detection responsive when
	// the service is unavailable. The durable job is retried on the next pass.
	client := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("registration metadata status %d", resp.StatusCode)
	}
	var status struct {
		RepoID           string `json:"repoId"`
		SessionName      string `json:"sessionId"`
		ID               string `json:"id"`
		ProcessingStatus string `json:"processing_status"`
		Source           struct {
			SnapshotDigest string `json:"snapshot_digest"`
		} `json:"source"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&status); err != nil {
		return false, err
	}
	meta := lfs.SessionMeta{RepoID: job.RepoID, SessionName: job.SessionName}
	if status.RepoID != job.RepoID || status.SessionName != job.SessionName || status.ID != meta.EffectiveSessionID() {
		return false, fmt.Errorf("registration identity mismatch")
	}
	if status.ProcessingStatus == "excluded" {
		return false, fmt.Errorf("session source is excluded")
	}
	if status.ProcessingStatus == "ready" && (job.SourceDigest == "" || status.Source.SnapshotDigest == job.SourceDigest) {
		return completeRegistration(ctx, path, job)
	}
	_, err = api.NewRepoClientWithEndpoint(job.Endpoint).WithAuthToken(token.AccessToken).NotifySessionUploaded(api.SessionUploadedNotification{RepoID: job.RepoID, SessionName: job.SessionName, SessionID: meta.EffectiveSessionID()})
	return false, err
}

func completeRegistration(ctx context.Context, path string, expected Job) (bool, error) {
	done := false
	err := fileutil.WithFileLock(ctx, path, func() error {
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			done = true
			return nil
		}
		if err != nil {
			return err
		}
		var current Job
		if err := json.Unmarshal(data, &current); err != nil {
			return err
		}
		// A slow lookup for the old snapshot must not remove a newer import's
		// registration job. Enqueue uses the same cross-process lock.
		if current != expected {
			return nil
		}
		if err := os.Remove(path); err != nil {
			return err
		}
		done = true
		return nil
	})
	return done, err
}

func RetryPending(ctx context.Context, ledger, endpoint, repoID string) error {
	entries, err := os.ReadDir(filepath.Join(ledger, ".sageox", "cache", "session-registration"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(ledger, ".sageox", "cache", "session-registration", entry.Name()))
		if err != nil {
			return err
		}
		var job Job
		if json.Unmarshal(b, &job) != nil || job.Endpoint != endpoint || job.RepoID != repoID {
			continue
		}
		// One unavailable service must not trigger a request for every pending job.
		if _, err := Retry(ctx, ledger, job); err != nil {
			return err
		}
	}
	return nil
}
