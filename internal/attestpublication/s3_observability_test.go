package attestpublication

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace/noop"
)

func TestS3ClientRecordsPhysicalAttemptSpans(t *testing.T) {
	for _, test := range []struct {
		name        string
		respond     func(http.ResponseWriter, int32)
		wantError   bool
		wantSuccess int
		wantFailure int
	}{
		{
			name: "retryable status",
			respond: func(w http.ResponseWriter, call int32) {
				if call == 1 {
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				w.WriteHeader(http.StatusOK)
			},
			wantSuccess: 1,
			wantFailure: 1,
		},
		{
			name: "transport reset",
			respond: func(w http.ResponseWriter, call int32) {
				if call == 1 {
					resetConnection(w)
					return
				}
				w.WriteHeader(http.StatusOK)
			},
			wantSuccess: 1,
			wantFailure: 1,
		},
		{
			name: "exhausted transport reset",
			respond: func(w http.ResponseWriter, _ int32) {
				resetConnection(w)
			},
			wantError:   true,
			wantSuccess: 0,
			wantFailure: 2,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			spans := tracetest.NewSpanRecorder()
			provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans))
			otel.SetTracerProvider(provider)
			t.Cleanup(func() {
				_ = provider.Shutdown(context.Background())
				otel.SetTracerProvider(noop.NewTracerProvider())
			})

			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				test.respond(w, calls.Add(1))
			}))
			t.Cleanup(server.Close)
			client, err := s3Client(t.Context(), testGrant(), func(options *s3.Options) {
				options.BaseEndpoint = aws.String(server.URL)
				options.UsePathStyle = true
				options.Retryer = retry.NewStandard(func(retryOptions *retry.StandardOptions) {
					retryOptions.MaxAttempts = 2
					retryOptions.Backoff = retry.BackoffDelayerFunc(func(int, error) (time.Duration, error) { return 0, nil })
				})
			})
			if err != nil {
				t.Fatalf("new S3 client: %v", err)
			}
			_, err = client.HeadObject(t.Context(), &s3.HeadObjectInput{Bucket: aws.String("private-attest-bucket"), Key: aws.String("private/evidence/key")})
			if !test.wantError && err != nil {
				t.Fatalf("HeadObject: %v", err)
			}
			if test.wantError && err == nil {
				t.Fatal("HeadObject succeeded after exhausted transport retries")
			}
			if got := calls.Load(); got != 2 {
				t.Fatalf("S3 requests = %d, want 2", got)
			}

			attempts := attestS3AttemptSpans(spans.Ended())
			if got := countAttemptOutcome(attempts, "success"); got != test.wantSuccess {
				t.Fatalf("successful physical attempt spans = %d, want %d", got, test.wantSuccess)
			}
			if got := countAttemptOutcome(attempts, "failure"); got != test.wantFailure {
				t.Fatalf("failed physical attempt spans = %d, want %d", got, test.wantFailure)
			}
			for _, span := range attempts {
				if got := spanAttribute(span, "attest.s3.operation"); got != "HeadObject" {
					t.Fatalf("attempt operation = %q, want HeadObject", got)
				}
				assertAttemptSpanDoesNotContain(t, span, "private-attest-bucket", "private/evidence", server.URL, "private-test-secret")
			}
		})
	}
}

func testGrant() Grant {
	return Grant{
		Region: "us-west-2",
		Credentials: Credentials{
			AccessKeyID:     "private-test-id",
			SecretAccessKey: "private-test-secret",
			SessionToken:    "private-test-token",
		},
	}
}

func resetConnection(w http.ResponseWriter) {
	conn, _, err := w.(http.Hijacker).Hijack()
	if err != nil {
		panic(err)
	}
	_ = conn.Close()
}

func attestS3AttemptSpans(spans []sdktrace.ReadOnlySpan) []sdktrace.ReadOnlySpan {
	var attempts []sdktrace.ReadOnlySpan
	for _, span := range spans {
		if span.Name() == s3AttemptSpanName {
			attempts = append(attempts, span)
		}
	}
	return attempts
}

func countAttemptOutcome(spans []sdktrace.ReadOnlySpan, want string) int {
	count := 0
	for _, span := range spans {
		if spanAttribute(span, "attest.s3.outcome") == want {
			count++
		}
	}
	return count
}

func spanAttribute(span sdktrace.ReadOnlySpan, key string) string {
	for _, attribute := range span.Attributes() {
		if string(attribute.Key) == key {
			return attribute.Value.AsString()
		}
	}
	return ""
}

func assertAttemptSpanDoesNotContain(t *testing.T, span sdktrace.ReadOnlySpan, forbidden ...string) {
	t.Helper()
	for _, attribute := range span.Attributes() {
		for _, value := range forbidden {
			if strings.Contains(string(attribute.Key), value) || strings.Contains(attribute.Value.AsString(), value) {
				t.Fatalf("attempt span contains forbidden value %q", value)
			}
		}
	}
}
