package attestpublication

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/smithy-go"
)

const boundaryRunID = "bdd_00000000-0000-7000-8000-000000000001"

func boundaryGrant(pkg Package) Grant {
	return Grant{
		Bucket: "bucket", Key: "staging/key", Region: "us-west-2",
		Credentials:     Credentials{AccessKeyID: "initial", SecretAccessKey: "secret", SessionToken: "token"},
		ExpiresAt:       time.Date(2099, 1, 1, 0, 15, 0, 0, time.UTC),
		IntentExpiresAt: time.Date(2099, 1, 1, 1, 0, 0, 0, time.UTC),
		Binding:         Binding{ManifestSHA256: pkg.ManifestSHA256, ArchiveSHA256: pkg.ArchiveSHA256, ArchiveSizeBytes: pkg.ArchiveSizeBytes},
	}
}

func boundaryRun(pkg Package, status string, grant *Grant) map[string]any {
	data := map[string]any{
		"run_id": boundaryRunID, "status": status, "source_run_id": pkg.Run.SourceRunID,
		"corpus_key": pkg.Run.Corpus.Key, "created_at": "2026-01-01T00:00:00Z", "diagnostics": []any{},
	}
	if grant != nil {
		data["upload"] = grant
	}
	return data
}

func boundaryReply(t *testing.T, w http.ResponseWriter, data any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{"data": data}); err != nil {
		t.Error(err)
	}
}

func boundaryError(w http.ResponseWriter, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`{"error":{"code":"unavailable","message":"try again"}}`))
}

func boundaryClient(t *testing.T, handler http.HandlerFunc) *ControlClient {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return &ControlClient{BaseURL: server.URL, HTTP: server.Client()}
}

// Failure prevented: losing a create response must not allocate a second run on resume.
func TestPublisherBoundaryPendingJournalReplaysOneCreate(t *testing.T) {
	for _, upload := range []bool{true, false} {
		t.Run(fmt.Sprintf("upload=%t", upload), func(t *testing.T) {
			pkg := testPackage(t)
			journal, err := NewPendingJournal(pkg)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "journal.json")
			if err := SaveJournal(path, journal); err != nil {
				t.Fatal(err)
			}
			grant := boundaryGrant(pkg)
			var creates, completions atomic.Int32
			client := boundaryClient(t, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.HasSuffix(r.URL.Path, "/runs"):
					creates.Add(1)
					if r.Header.Get("Idempotency-Key") != journal.IdempotencyKey {
						t.Error("replayed create changed the persisted idempotency key")
					}
					if upload {
						boundaryReply(t, w, boundaryRun(pkg, "uploading", &grant))
					} else {
						boundaryReply(t, w, boundaryRun(pkg, "published", nil))
					}
				case strings.HasSuffix(r.URL.Path, "/completions"):
					completions.Add(1)
					boundaryReply(t, w, boundaryRun(pkg, "published", nil))
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
					boundaryError(w, http.StatusNotFound)
				}
			})
			transfer := &fakeMultipart{}
			result, err := (Publisher{Control: client, Transfer: transfer}).Publish(context.Background(), "repo_test", path, pkg)
			if err != nil || result.Run.Status != "published" || creates.Load() != 1 {
				t.Fatalf("replay result=%+v creates=%d err=%v", result.Run, creates.Load(), err)
			}
			if transfer.completed != upload || completions.Load() != map[bool]int32{true: 1, false: 0}[upload] {
				t.Fatalf("replay repeated or skipped transfer: completed=%t control completions=%d", transfer.completed, completions.Load())
			}
			saved, err := LoadJournal(path)
			if err != nil || !saved.Completed || saved.RunID != boundaryRunID || saved.IdempotencyKey != journal.IdempotencyKey {
				t.Fatalf("replay lost durable binding: %+v, %v", saved, err)
			}
		})
	}
}

// Failure prevented: corrupt local state must fail before issuing a new hosted publication.
func TestPublisherBoundaryRejectsUnusableJournal(t *testing.T) {
	for _, name := range []string{"malformed", "changed-package", "missing-idempotency-key", "missing-control"} {
		t.Run(name, func(t *testing.T) {
			pkg := testPackage(t)
			journal, err := NewPendingJournal(pkg)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "journal.json")
			want := ""
			switch name {
			case "changed-package":
				journal.SourceRunID = "another-source"
				want = "frozen package changed"
			case "missing-idempotency-key":
				journal.IdempotencyKey = ""
				want = "no create idempotency key"
			case "missing-control":
				want = "control client is required"
			case "malformed":
				want = "read attest upload journal"
			}
			if err := SaveJournal(path, journal); err != nil {
				t.Fatal(err)
			}
			if name == "malformed" {
				if err := os.WriteFile(path, []byte("interrupted write"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			client := boundaryClient(t, func(w http.ResponseWriter, _ *http.Request) {
				t.Error("invalid local state reached the API")
				boundaryError(w, http.StatusBadRequest)
			})
			if name == "missing-control" {
				client = nil
			}
			_, err = (Publisher{Control: client, Transfer: &fakeMultipart{}}).Publish(context.Background(), "repo_test", path, pkg)
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("got %v, want %q", err, want)
			}
		})
	}
}

// Failure prevented: API outages during resume must preserve the journal for the next invocation.
func TestPublisherBoundaryResumeControlFailuresPreserveJournal(t *testing.T) {
	for _, state := range []string{"pending", "uploading", "completed"} {
		t.Run(state, func(t *testing.T) {
			pkg := testPackage(t)
			journal, err := NewPendingJournal(pkg)
			if err != nil {
				t.Fatal(err)
			}
			if state != "pending" {
				grant := boundaryGrant(pkg)
				journal, err = journal.Bind(Run{RunID: boundaryRunID, Upload: &grant})
				if err != nil {
					t.Fatal(err)
				}
			}
			journal.Completed = state == "completed"
			path := filepath.Join(t.TempDir(), "journal.json")
			if err := SaveJournal(path, journal); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			client := boundaryClient(t, func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				boundaryError(w, http.StatusForbidden)
			})
			_, err = (Publisher{Control: client, Transfer: &fakeMultipart{}}).Publish(context.Background(), "repo_test", path, pkg)
			var apiErr *APIError
			if !errors.As(err, &apiErr) || apiErr.Status != http.StatusForbidden || calls.Load() != 1 {
				t.Fatalf("resume swallowed or retried permission failure: calls=%d err=%v", calls.Load(), err)
			}
			after, err := os.ReadFile(path)
			if err != nil || string(before) != string(after) {
				t.Fatalf("failed resume changed the durable journal: %v", err)
			}
		})
	}
}

type boundaryMultipart struct {
	fakeMultipart
	listErrors []error
	grants     []Grant
}

func (f *boundaryMultipart) List(ctx context.Context, grant Grant, uploadID string) ([]CompletedPart, error) {
	f.grants = append(f.grants, grant)
	if len(f.listErrors) > 0 {
		err := f.listErrors[0]
		f.listErrors = f.listErrors[1:]
		if err != nil {
			return nil, err
		}
	}
	return f.fakeMultipart.List(ctx, grant, uploadID)
}

// Failure prevented: expired credentials must renew the same upload without hiding a failed retry.
func TestPublisherBoundaryExpiredCredentialRecovery(t *testing.T) {
	retryFailure := errors.New("multipart retry failed")
	cases := []struct {
		name, code       string
		renewStatus      int
		changedDeadline  bool
		retryFailure     error
		completionStatus int
		wantError        string
	}{
		{name: "expired-token", code: "ExpiredToken"},
		{name: "expired-token-exception", code: "ExpiredTokenException"},
		{name: "request-expired", code: "RequestExpired"},
		{name: "renewal-denied", code: "ExpiredToken", renewStatus: http.StatusForbidden, wantError: "attest API 403"},
		{name: "renewal-changed-deadline", code: "ExpiredToken", changedDeadline: true, wantError: "immutable package binding or intent deadline"},
		{name: "retry-and-reconciliation-fail", code: "ExpiredToken", retryFailure: retryFailure, completionStatus: http.StatusConflict, wantError: retryFailure.Error()},
		{name: "retry-response-lost", code: "ExpiredToken", retryFailure: retryFailure},
		{name: "control-completion-fails", code: "ExpiredToken", completionStatus: http.StatusServiceUnavailable, wantError: "attest API 503"},
		{name: "not-an-expiry", code: "AccessDenied", completionStatus: http.StatusConflict, wantError: "AccessDenied"},
		{name: "ordinary-transfer-error", completionStatus: http.StatusConflict, wantError: "network disconnected"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pkg := testPackage(t)
			grant := boundaryGrant(pkg)
			first := errors.New("network disconnected")
			if tc.code != "" {
				first = fmt.Errorf("list staging upload: %w", &smithy.GenericAPIError{Code: tc.code, Message: "upload rejected"})
			}
			transfer := &boundaryMultipart{listErrors: []error{first, tc.retryFailure}}
			var renewals, completions atomic.Int32
			client := boundaryClient(t, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.HasSuffix(r.URL.Path, "/runs"):
					boundaryReply(t, w, boundaryRun(pkg, "uploading", &grant))
				case strings.HasSuffix(r.URL.Path, "/upload-grants"):
					renewals.Add(1)
					if tc.renewStatus != 0 {
						boundaryError(w, tc.renewStatus)
						return
					}
					renewed := grant
					renewed.Credentials.AccessKeyID = "renewed"
					if tc.changedDeadline {
						renewed.IntentExpiresAt = renewed.IntentExpiresAt.Add(time.Minute)
					}
					boundaryReply(t, w, renewed)
				case strings.HasSuffix(r.URL.Path, "/completions"):
					completions.Add(1)
					if tc.completionStatus != 0 {
						boundaryError(w, tc.completionStatus)
						return
					}
					boundaryReply(t, w, boundaryRun(pkg, "published", nil))
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
					boundaryError(w, http.StatusNotFound)
				}
			})
			path := filepath.Join(t.TempDir(), "journal.json")
			result, err := (Publisher{Control: client, Transfer: transfer}).Publish(context.Background(), "repo_test", path, pkg)
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("got %v, want %q", err, tc.wantError)
				}
			} else if err != nil || result.Run.Status != "published" {
				t.Fatalf("recovery failed: result=%+v err=%v", result.Run, err)
			}
			wantRenewal := tc.code != "" && tc.code != "AccessDenied"
			if renewals.Load() != map[bool]int32{true: 1, false: 0}[wantRenewal] {
				t.Fatalf("renewals=%d for error %v", renewals.Load(), first)
			}
			if wantRenewal && tc.renewStatus == 0 && !tc.changedDeadline {
				if len(transfer.grants) != 2 || transfer.grants[1].Credentials.AccessKeyID != "renewed" || transfer.grants[1].Key != grant.Key {
					t.Fatalf("retry did not use renewed credentials for the original object: %+v", transfer.grants)
				}
			}
			if tc.renewStatus != 0 || tc.changedDeadline {
				if completions.Load() != 0 || len(transfer.grants) != 1 {
					t.Fatal("failed renewal continued into upload or control completion")
				}
			} else if completions.Load() != 1 {
				t.Fatalf("completion calls=%d, want 1", completions.Load())
			}
			journal, loadErr := LoadJournal(path)
			if loadErr != nil || journal.RunID != boundaryRunID || journal.Binding != grant.Binding {
				t.Fatalf("recovery lost its durable binding: %+v err=%v", journal, loadErr)
			}
			if tc.wantError == "" && !journal.Completed {
				t.Fatal("successful upload reconciliation was not durable")
			}
		})
	}
}

// Failure prevented: transient create failures need bounded retries with one stable key.
func TestPublisherBoundaryCreateRecoveryIsBounded(t *testing.T) {
	for _, failures := range []int{1, 3} {
		t.Run(fmt.Sprintf("failures=%d", failures), func(t *testing.T) {
			pkg := testPackage(t)
			var calls atomic.Int32
			var sleeps int
			client := boundaryClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Header.Get("Idempotency-Key") != "durable-create-key" {
					t.Error("create retry changed identity")
				}
				if calls.Load() <= int32(failures) {
					boundaryError(w, http.StatusServiceUnavailable)
					return
				}
				boundaryReply(t, w, boundaryRun(pkg, "published", nil))
			})
			publisher := Publisher{Control: client, Sleep: func(time.Duration) { sleeps++ }}
			run, err := publisher.createWithRecovery(context.Background(), "repo_test", "durable-create-key", createRequest(pkg))
			if failures == 1 {
				if err != nil || run.Status != "published" || calls.Load() != 2 || sleeps != 1 {
					t.Fatalf("transient failure did not recover: run=%+v calls=%d sleeps=%d err=%v", run, calls.Load(), sleeps, err)
				}
			} else {
				var apiErr *APIError
				if !errors.As(err, &apiErr) || apiErr.Status != http.StatusServiceUnavailable || calls.Load() != 3 {
					t.Fatalf("unbounded or hidden create failure: calls=%d err=%v", calls.Load(), err)
				}
			}
		})
	}
}

// Failure prevented: cancellation or status-read failure must end polling instead of hanging the CLI.
func TestPublisherBoundaryPollingStopsOnFailure(t *testing.T) {
	pkg := testPackage(t)
	client := boundaryClient(t, func(w http.ResponseWriter, _ *http.Request) {
		boundaryError(w, http.StatusForbidden)
	})
	publisher := Publisher{Control: client, Sleep: func(time.Duration) {}}
	_, err := publisher.poll(context.Background(), "repo_test", Run{RunID: boundaryRunID, Status: "processing"})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusForbidden {
		t.Fatalf("status read failure was hidden: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	publisher.Sleep = nil
	if _, err := publisher.poll(ctx, "repo_test", Run{RunID: boundaryRunID, Status: "uploading"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("poll ignored cancellation: %v", err)
	}
	if _, err := publisher.createWithRecovery(ctx, "repo_test", "key", createRequest(pkg)); !errors.Is(err, context.Canceled) {
		t.Fatalf("create retry ignored cancellation: %v", err)
	}
}

// Failure prevented: a lost journal write must stop publication before its state becomes unrecoverable.
func TestPublisherBoundaryJournalWriteFailureStopsProgress(t *testing.T) {
	for _, mode := range []string{"new-upload", "new-terminal", "pending-upload", "pending-terminal", "reconciled-upload"} {
		t.Run(mode, func(t *testing.T) {
			pkg := testPackage(t)
			path := filepath.Join(t.TempDir(), "journal.json")
			if strings.HasPrefix(mode, "pending-") {
				journal, err := NewPendingJournal(pkg)
				if err != nil {
					t.Fatal(err)
				}
				if err := SaveJournal(path, journal); err != nil {
					t.Fatal(err)
				}
			}
			grant := boundaryGrant(pkg)
			transfer := &boundaryMultipart{}
			if mode == "reconciled-upload" {
				transfer.listErrors = []error{errors.New("multipart response lost")}
			}
			var calls atomic.Int32
			client := boundaryClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				completion := strings.HasSuffix(r.URL.Path, "/completions")
				if completion || mode != "reconciled-upload" {
					// A directory at the journal name fails atomic replacement on all platforms.
					if err := os.Remove(path); err != nil {
						t.Error(err)
					}
					if err := os.Mkdir(path, 0o700); err != nil {
						t.Error(err)
					}
				}
				if completion || strings.HasSuffix(mode, "terminal") {
					boundaryReply(t, w, boundaryRun(pkg, "published", nil))
				} else {
					boundaryReply(t, w, boundaryRun(pkg, "uploading", &grant))
				}
			})
			_, err := (Publisher{Control: client, Transfer: transfer}).Publish(context.Background(), "repo_test", path, pkg)
			if err == nil {
				t.Fatal("publication succeeded despite its journal being unwritable")
			}
			wantCalls := int32(1)
			if mode == "reconciled-upload" {
				wantCalls = 2
			}
			if calls.Load() != wantCalls || transfer.completed {
				t.Fatalf("publication continued after persistence failure: calls=%d transfer completed=%t", calls.Load(), transfer.completed)
			}
		})
	}
}

// Failure prevented: missing archive bytes must never allocate a new hosted run.
func TestPublisherBoundaryRejectsMissingArchiveBeforeCreate(t *testing.T) {
	for _, parentIsFile := range []bool{false, true} {
		t.Run(fmt.Sprintf("parent-is-file=%t", parentIsFile), func(t *testing.T) {
			pkg := testPackage(t)
			if err := os.Remove(pkg.ArchivePath); err != nil {
				t.Fatal(err)
			}
			if parentIsFile {
				parent := filepath.Dir(pkg.ArchivePath)
				if err := os.Remove(parent); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(parent, []byte("not a directory"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			client := boundaryClient(t, func(w http.ResponseWriter, _ *http.Request) {
				t.Error("missing archive reached the control plane")
				boundaryError(w, http.StatusBadRequest)
			})
			_, err := (Publisher{Control: client, Transfer: &fakeMultipart{}}).Publish(context.Background(), "repo_test", filepath.Join(t.TempDir(), "journal.json"), pkg)
			if err == nil {
				t.Fatal("missing archive was accepted")
			}
		})
	}
}

// Failure prevented: a resumed multipart upload must use renewed credentials before touching S3.
func TestPublisherBoundaryBoundJournalRenewsBeforeTransfer(t *testing.T) {
	pkg := testPackage(t)
	grant := boundaryGrant(pkg)
	journal, err := NewJournal(Run{RunID: boundaryRunID, Upload: &grant}, pkg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "journal.json")
	if err := SaveJournal(path, journal); err != nil {
		t.Fatal(err)
	}
	var renewals atomic.Int32
	client := boundaryClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/upload-grants"):
			renewals.Add(1)
			grant.Credentials.AccessKeyID = "renewed"
			boundaryReply(t, w, grant)
		case strings.HasSuffix(r.URL.Path, "/completions"):
			boundaryReply(t, w, boundaryRun(pkg, "published", nil))
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			boundaryError(w, http.StatusNotFound)
		}
	})
	transfer := &boundaryMultipart{}
	result, err := (Publisher{Control: client, Transfer: transfer}).Publish(context.Background(), "repo_test", path, pkg)
	if err != nil || result.Run.Status != "published" || renewals.Load() != 1 || len(transfer.grants) != 1 || transfer.grants[0].Credentials.AccessKeyID != "renewed" {
		t.Fatalf("bound resume failed: result=%+v renewals=%d grants=%+v err=%v", result.Run, renewals.Load(), transfer.grants, err)
	}
}

// Failure prevented: Ctrl-C during retry backoff must not wait for another control request.
func TestPublisherBoundaryCreateBackoffHonorsCancellation(t *testing.T) {
	pkg := testPackage(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls atomic.Int32
	client := boundaryClient(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		// Cancel after the response is processed, while the real one-second backoff waits.
		timer := time.AfterFunc(25*time.Millisecond, cancel)
		t.Cleanup(func() { timer.Stop() })
		boundaryError(w, http.StatusServiceUnavailable)
	})
	_, err := (Publisher{Control: client}).createWithRecovery(ctx, "repo_test", "durable-key", createRequest(pkg))
	if !errors.Is(err, context.Canceled) || calls.Load() != 1 {
		t.Fatalf("retry backoff ignored cancellation: calls=%d err=%v", calls.Load(), err)
	}
}
