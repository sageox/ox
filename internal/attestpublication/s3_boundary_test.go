package attestpublication

import (
	"bytes"
	"context"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func localS3Grant(t *testing.T, handler http.HandlerFunc) Grant {
	t.Helper()
	grant := testGrant()
	grant.Bucket = "attest-test-bucket"
	grant.Key = "runs/frozen/input +archive.zip"
	grant.Binding.ArchiveSizeBytes = 11
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/"+grant.Bucket+"/"+grant.Key {
			t.Errorf("request escaped granted object: %s", r.URL.Path)
		}
		if r.Header.Get("X-Amz-Security-Token") != grant.Credentials.SessionToken ||
			!strings.Contains(r.Header.Get("Authorization"), "Credential="+grant.Credentials.AccessKeyID+"/") ||
			!strings.Contains(r.Header.Get("Authorization"), "/us-west-2/s3/aws4_request") {
			t.Error("request did not use the scoped grant credentials and region")
		}
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	// Config and credentials files isolate the SDK from a developer's AWS profile.
	configPath := filepath.Join(t.TempDir(), "aws-config")
	if err := os.WriteFile(configPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AWS_CONFIG_FILE", configPath)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", configPath)
	t.Setenv("AWS_PROFILE", "")
	t.Setenv("AWS_DEFAULT_PROFILE", "")
	t.Setenv("AWS_ENDPOINT_URL_S3", server.URL)
	t.Setenv("AWS_IGNORE_CONFIGURED_ENDPOINT_URLS", "false")
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	// A single attempt makes service failures deterministic without retry sleeps.
	t.Setenv("AWS_MAX_ATTEMPTS", "1")
	return grant
}

// A successful upload must preserve the grant's object, part identities and create-only completion precondition on the wire.
func TestS3MultipartPreservesGrantAndMultipartProtocol(t *testing.T) {
	var requests atomic.Int32
	grant := localS3Grant(t, func(w http.ResponseWriter, r *http.Request) {
		call := requests.Add(1)
		w.Header().Set("Content-Type", "application/xml")
		query := r.URL.Query()
		if call > 1 && query.Get("uploadId") != "multipart-1" {
			t.Errorf("upload ID = %q", query.Get("uploadId"))
		}
		switch call {
		case 1:
			if r.Method != http.MethodPost || !query.Has("uploads") {
				t.Errorf("create request = %s %s", r.Method, r.URL.RawQuery)
			}
			_, _ = io.WriteString(w, `<InitiateMultipartUploadResult><UploadId>multipart-1</UploadId></InitiateMultipartUploadResult>`)
		case 2, 3:
			if r.Method != http.MethodGet {
				t.Errorf("list method = %s", r.Method)
			}
			if call == 2 {
				if query.Has("part-number-marker") {
					t.Error("first list started after a part")
				}
				_, _ = io.WriteString(w, `<ListPartsResult><IsTruncated>true</IsTruncated><NextPartNumberMarker>1</NextPartNumberMarker><Part><PartNumber>1</PartNumber><ETag>"first"</ETag><Size>5</Size></Part></ListPartsResult>`)
			} else {
				if query.Get("part-number-marker") != "1" {
					t.Errorf("continuation marker = %q", query.Get("part-number-marker"))
				}
				_, _ = io.WriteString(w, `<ListPartsResult><IsTruncated>false</IsTruncated><Part><PartNumber>2</PartNumber><ETag>"second"</ETag><Size>6</Size></Part></ListPartsResult>`)
			}
		case 4:
			body, err := io.ReadAll(r.Body)
			if err != nil || string(body) != "frozen part" || r.ContentLength != 11 ||
				r.Method != http.MethodPut || query.Get("partNumber") != "3" {
				t.Errorf("part request method=%s number=%s size=%d body=%q error=%v", r.Method, query.Get("partNumber"), r.ContentLength, body, err)
			}
			w.Header().Set("ETag", `"uploaded"`)
		case 5:
			assertS3Completion(t, r, []CompletedPart{{Number: 1, ETag: `"first"`}, {Number: 2, ETag: `"second"`}})
			_, _ = io.WriteString(w, `<CompleteMultipartUploadResult><ETag>"complete"</ETag></CompleteMultipartUploadResult>`)
		default:
			t.Errorf("unexpected S3 request %d", call)
			w.WriteHeader(http.StatusBadRequest)
		}
	})
	transfer := S3Multipart{}
	uploadID, err := transfer.Create(t.Context(), grant)
	if err != nil || uploadID != "multipart-1" {
		t.Fatalf("create = %q, %v", uploadID, err)
	}
	parts, err := transfer.List(t.Context(), grant, uploadID)
	wantParts := []CompletedPart{{Number: 1, ETag: `"first"`, Size: 5}, {Number: 2, ETag: `"second"`, Size: 6}}
	if err != nil || !reflect.DeepEqual(parts, wantParts) {
		t.Fatalf("list = %#v, %v", parts, err)
	}
	part, err := transfer.Upload(t.Context(), grant, uploadID, 3, bytes.NewReader([]byte("frozen part")), 11)
	if err != nil || part != (CompletedPart{Number: 3, ETag: `"uploaded"`, Size: 11}) {
		t.Fatalf("upload = %#v, %v", part, err)
	}
	if err := transfer.Complete(t.Context(), grant, uploadID, parts); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 5 {
		t.Fatalf("requests = %d, want 5", requests.Load())
	}
}

func assertS3Completion(t *testing.T, r *http.Request, want []CompletedPart) {
	t.Helper()
	if r.Method != http.MethodPost || r.Header.Get("If-None-Match") != "*" || r.Header.Get("X-Amz-Mp-Object-Size") != "11" {
		t.Errorf("completion lost create-only or archive-size condition: method=%s if-none-match=%q size=%q", r.Method, r.Header.Get("If-None-Match"), r.Header.Get("X-Amz-Mp-Object-Size"))
	}
	var document struct {
		Parts []struct {
			Number int32  `xml:"PartNumber"`
			ETag   string `xml:"ETag"`
		} `xml:"Part"`
	}
	if err := xml.NewDecoder(r.Body).Decode(&document); err != nil {
		t.Errorf("completion XML: %v", err)
		return
	}
	got := make([]CompletedPart, 0, len(document.Parts))
	for _, part := range document.Parts {
		got = append(got, CompletedPart{Number: part.Number, ETag: part.ETag})
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("completion parts = %#v, want %#v", got, want)
	}
}

// HEAD absence is recoverable, but access failures and conflicting object sizes must never become successful completion.
func TestS3MultipartCompletionProbeDistinguishesFailure(t *testing.T) {
	for _, test := range []struct {
		name     string
		status   int
		size     int
		wantDone bool
		wantErr  string
	}{
		{"matching object", http.StatusOK, 11, true, ""},
		{"absent object", http.StatusNotFound, 0, false, ""},
		{"wrong archive size", http.StatusOK, 12, false, "immutable archive binding"},
		{"access denied", http.StatusForbidden, 0, false, "403"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var requests atomic.Int32
			grant := localS3Grant(t, func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.Method != http.MethodHead || r.URL.RawQuery != "" {
					t.Errorf("completion probe = %s %s", r.Method, r.URL)
				}
				w.Header().Set("Content-Length", strconv.Itoa(test.size))
				w.WriteHeader(test.status)
			})
			done, err := (S3Multipart{}).Completed(t.Context(), grant)
			if done != test.wantDone || (test.wantErr == "" && err != nil) ||
				(test.wantErr != "" && (err == nil || !strings.Contains(err.Error(), test.wantErr))) {
				t.Fatalf("probe = %v, %v", done, err)
			}
			if requests.Load() != 1 {
				t.Fatalf("requests = %d, want 1", requests.Load())
			}
		})
	}
}

func s3BoundaryOperations() map[string]func(context.Context, Grant) error {
	transfer := S3Multipart{}
	return map[string]func(context.Context, Grant) error{
		"create": func(ctx context.Context, grant Grant) error { _, err := transfer.Create(ctx, grant); return err },
		"list": func(ctx context.Context, grant Grant) error {
			_, err := transfer.List(ctx, grant, "multipart-1")
			return err
		},
		"upload": func(ctx context.Context, grant Grant) error {
			_, err := transfer.Upload(ctx, grant, "multipart-1", 1, strings.NewReader("frozen part"), 11)
			return err
		},
		"complete": func(ctx context.Context, grant Grant) error {
			return transfer.Complete(ctx, grant, "multipart-1", []CompletedPart{{Number: 1, ETag: `"part"`, Size: 11}})
		},
		"probe": func(ctx context.Context, grant Grant) error { _, err := transfer.Completed(ctx, grant); return err },
	}
}

// Each operation must preserve the service failure so the caller cannot advance a failed multipart journal.
func TestS3MultipartPropagatesServiceFailures(t *testing.T) {
	for name, operation := range s3BoundaryOperations() {
		t.Run(name, func(t *testing.T) {
			var requests atomic.Int32
			grant := localS3Grant(t, func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				w.Header().Set("Content-Type", "application/xml")
				w.WriteHeader(http.StatusForbidden)
				_, _ = io.WriteString(w, `<Error><Code>AccessDenied</Code><Message>grant denied</Message></Error>`)
			})
			err := operation(t.Context(), grant)
			if err == nil || !isS3Code(err, "AccessDenied", "Forbidden") {
				t.Fatalf("service error was lost: %v", err)
			}
			if requests.Load() != 1 {
				t.Fatalf("requests = %d, want 1", requests.Load())
			}
		})
	}
}

// Invalid SDK configuration must fail before sending any credential-bearing request.
func TestS3MultipartRejectsInvalidSDKConfiguration(t *testing.T) {
	for name, operation := range s3BoundaryOperations() {
		t.Run(name, func(t *testing.T) {
			var requests atomic.Int32
			grant := localS3Grant(t, func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				w.WriteHeader(http.StatusBadRequest)
			})
			t.Setenv("AWS_MAX_ATTEMPTS", "not-an-integer")
			if err := operation(t.Context(), grant); err == nil || !strings.Contains(err.Error(), "AWS_MAX_ATTEMPTS") {
				t.Fatalf("invalid SDK config error = %v", err)
			}
			if requests.Load() != 0 {
				t.Fatalf("invalid configuration sent %d requests", requests.Load())
			}
		})
	}
}

// A lost completion response is repaired only after the granted object is independently observed with the bound size.
func TestS3MultipartRecoversLostCompletionOnWire(t *testing.T) {
	for _, phase := range []string{"list", "complete"} {
		t.Run(phase, func(t *testing.T) {
			var requests atomic.Int32
			grant := localS3Grant(t, func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.Header().Set("Content-Type", "application/xml")
				switch r.Method {
				case http.MethodGet:
					if phase == "list" {
						w.WriteHeader(http.StatusNotFound)
						_, _ = io.WriteString(w, `<Error><Code>NoSuchUpload</Code></Error>`)
					} else {
						_, _ = io.WriteString(w, `<ListPartsResult><IsTruncated>false</IsTruncated><Part><PartNumber>1</PartNumber><ETag>"part"</ETag><Size>11</Size></Part></ListPartsResult>`)
					}
				case http.MethodPost:
					assertS3Completion(t, r, []CompletedPart{{Number: 1, ETag: `"part"`}})
					w.WriteHeader(http.StatusPreconditionFailed)
					_, _ = io.WriteString(w, `<Error><Code>PreconditionFailed</Code></Error>`)
				case http.MethodHead:
					w.Header().Set("Content-Length", "11")
				default:
					t.Errorf("recovery unexpectedly sent %s", r.Method)
					w.WriteHeader(http.StatusBadRequest)
				}
			})
			dir := t.TempDir()
			archivePath := filepath.Join(dir, "bundle.zip")
			if err := os.WriteFile(archivePath, []byte("frozen part"), 0o600); err != nil {
				t.Fatal(err)
			}
			journalPath := filepath.Join(dir, "upload.json")
			journal := Journal{Version: 1, RunID: "bound-run", ArchivePath: archivePath, Binding: grant.Binding, UploadID: "multipart-1", IntentExpiresAt: time.Now().Add(time.Hour)}
			if err := UploadArchive(t.Context(), S3Multipart{}, grant, journalPath, &journal); err != nil {
				t.Fatal(err)
			}
			stored, err := LoadJournal(journalPath)
			if err != nil || !stored.Completed || stored.UploadID != "multipart-1" || stored.Binding != grant.Binding || stored.RunID != "bound-run" {
				t.Fatalf("recovered journal = %+v, error=%v", stored, err)
			}
			want := int32(2)
			if phase == "complete" {
				want = 3
			}
			if requests.Load() != want {
				t.Fatalf("recovery requests = %d, want %d", requests.Load(), want)
			}
		})
	}
}
