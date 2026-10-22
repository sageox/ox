package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sageox/ox/internal/useragent"
	"github.com/sageox/ox/internal/version"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testSupportCaseID = "sup_0123456789abcdef01"

// TestSupportSubmission_PreservesGuardedBytes prevents sending a different body
// from the exact JSON bytes approved by the caller's secret guard.
func TestSupportSubmission_PreservesGuardedBytes(t *testing.T) {
	t.Parallel()
	payload := []byte(" {\n  \"description\": \"A quote: \\\"example\\\"\", \"context\": {\"version\":1,\"surface\":\"cli\"}\n}\n")
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/api/v1/support/cases", r.URL.Path)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		assert.Equal(t, "Bearer test-support-token", r.Header.Get("Authorization"))
		assert.Equal(t, "cli", r.Header.Get("X-Client-Type"))
		assert.NotEmpty(t, r.UserAgent())
		assert.Empty(t, r.Header.Get("Idempotency-Key"))
		body, err := io.ReadAll(r.Body)
		assert.NoError(t, err)
		assert.Equal(t, payload, body)
		_, _ = io.WriteString(w, `{"case_id":"`+testSupportCaseID+`"}`)
	}))
	defer server.Close()

	client := NewRepoClientWithEndpoint(server.URL + "/").WithAuthToken("test-support-token")
	caseID, err := client.SubmitSupportCase(context.Background(), payload)
	require.NoError(t, err)
	assert.Equal(t, testSupportCaseID, caseID)
	assert.EqualValues(t, 1, calls.Load())
}

// TestSupportSubmission_ExcludesAmbientIdentity prevents feedback from silently
// disclosing repository identity or arbitrary inherited agent metadata.
func TestSupportSubmission_ExcludesAmbientIdentity(t *testing.T) {
	useragent.ResetForTesting()
	t.Cleanup(useragent.ResetForTesting)
	useragent.SetRepoID("repo-private-sentinel")
	useragent.SetAgentType("private-agent-sentinel")
	useragent.SetAgentVersion("private-version-sentinel")
	t.Setenv("ORCHESTRATOR_ENV", "private-orchestrator-sentinel")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Empty(t, r.Header.Get(useragent.HeaderRepoID))
		assert.Empty(t, r.Header.Get("X-Orchestrator"))
		assert.Equal(t, fmt.Sprintf("ox/%s (%s; %s)", version.Version, runtime.GOOS, runtime.GOARCH), r.UserAgent())
		assert.Equal(t, "cli", r.Header.Get(useragent.HeaderClientType))
		assert.NotEmpty(t, r.Header.Get("traceparent"))
		for _, values := range r.Header {
			assert.NotContains(t, strings.Join(values, " "), "private-")
		}
		_, _ = io.WriteString(w, `{"case_id":"`+testSupportCaseID+`"}`)
	}))
	defer server.Close()
	_, err := NewRepoClientWithEndpoint(server.URL).SubmitSupportCase(context.Background(), []byte(`{"description":"Example feedback"}`))
	require.NoError(t, err)
}

// TestSupportContext_MatchesBackendFields prevents silently dropping product
// version metadata because of a snake_case/camelCase contract mismatch.
func TestSupportContext_MatchesBackendFields(t *testing.T) {
	t.Parallel()
	payload, err := json.Marshal(SupportCaseRequest{
		Description: "An example report",
		Email:       "person@example.com",
		Context: SupportContext{
			Version: 1, Surface: "cli", Intent: "feedback", ProductVersion: "0.1.0", Platform: "test", Category: "bug",
		},
	})
	require.NoError(t, err)
	assert.JSONEq(t, `{"description":"An example report","email":"person@example.com","context":{"version":1,"surface":"cli","intent":"feedback","productVersion":"0.1.0","platform":"test","category":"bug"}}`, string(payload))
	payload, err = json.Marshal(SupportCaseRequest{Description: "An example report", Context: SupportContext{Version: 1, Surface: "cli"}})
	require.NoError(t, err)
	assert.JSONEq(t, `{"description":"An example report","context":{"version":1,"surface":"cli"}}`, string(payload))
}

// TestSupportSubmission_RejectsUntrustworthyReceipts prevents reporting success
// for HTML, truncated bodies, ambiguous IDs, or an unrelated API response.
func TestSupportSubmission_RejectsUntrustworthyReceipts(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		body string
	}{
		{name: "empty"},
		{name: "html", body: "<html>private-response-sentinel</html>"},
		{name: "null", body: "null"},
		{name: "array", body: `[{"case_id":"` + testSupportCaseID + `"}]`},
		{name: "missing id", body: `{}`},
		{name: "empty id", body: `{"case_id":""}`},
		{name: "numeric id", body: `{"case_id":42}`},
		{name: "wrong prefix", body: `{"case_id":"usr_0123456789abcdef01"}`},
		{name: "uppercase hex", body: `{"case_id":"sup_0123456789ABCDEF01"}`},
		{name: "short id", body: `{"case_id":"sup_0123456789abcdef0"}`},
		{name: "long id", body: `{"case_id":"sup_0123456789abcdef012"}`},
		{name: "id whitespace", body: `{"case_id":"` + testSupportCaseID + ` "}`},
		{name: "wrong key case", body: `{"CASE_ID":"` + testSupportCaseID + `"}`},
		{name: "unknown field", body: `{"case_id":"` + testSupportCaseID + `","report":"private-response-sentinel"}`},
		{name: "duplicate id", body: `{"case_id":"` + testSupportCaseID + `","case_id":"` + testSupportCaseID + `"}`},
		{name: "trailing json", body: `{"case_id":"` + testSupportCaseID + `"}{}`},
		{name: "trailing text", body: `{"case_id":"` + testSupportCaseID + `"}private-response-sentinel`},
		{name: "truncated", body: `{"case_id":"` + testSupportCaseID + `"`},
		{name: "oversize", body: `{"case_id":"` + testSupportCaseID + `"}` + strings.Repeat(" ", maxSupportResponseBytes)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, tt.body)
			}))
			defer server.Close()
			caseID, err := NewRepoClientWithEndpoint(server.URL).SubmitSupportCase(context.Background(), []byte(`{"description":"private-report-sentinel"}`))
			require.Error(t, err)
			assert.Empty(t, caseID)
			assert.NotContains(t, err.Error(), "private-response-sentinel")
			assert.NotContains(t, err.Error(), "private-report-sentinel")
		})
	}
}

// TestSupportSubmission_RefusalsNeverRetryOrEchoReports prevents duplicate email
// delivery, response-body disclosure, and mistaking a redirected GET for a POST.
func TestSupportSubmission_RefusalsNeverRetryOrEchoReports(t *testing.T) {
	t.Parallel()
	for _, status := range []int{201, 202, 204, 301, 302, 303, 307, 308, 400, 401, 403, 404, 409, 413, 422, 426, 429, 500, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			t.Parallel()
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				assert.Equal(t, "/api/v1/support/cases", r.URL.Path)
				w.Header().Set("Location", "/redirect-target")
				w.WriteHeader(status)
				_, _ = io.WriteString(w, `{"error":{"code":"private-response-sentinel","message":"private-report-sentinel test-support-token"},"case_id":"`+testSupportCaseID+`"}`)
			}))
			defer server.Close()
			client := NewRepoClientWithEndpoint(server.URL).WithAuthToken("test-support-token")
			caseID, err := client.SubmitSupportCase(context.Background(), []byte(`{"description":"private-report-sentinel"}`))
			require.Error(t, err)
			assert.Empty(t, caseID)
			assert.EqualValues(t, 1, calls.Load())
			assert.NotContains(t, err.Error(), "private-report-sentinel")
			assert.NotContains(t, err.Error(), "private-response-sentinel")
			assert.NotContains(t, err.Error(), "test-support-token")
			switch status {
			case http.StatusUnauthorized:
				assert.ErrorIs(t, err, ErrUnauthorized)
			case http.StatusUpgradeRequired:
				assert.ErrorIs(t, err, ErrVersionUnsupported)
			default:
				var statusError *HTTPStatusError
				require.ErrorAs(t, err, &statusError)
				assert.Equal(t, status, statusError.StatusCode)
			}
		})
	}
}

// TestSupportSubmission_BoundsRequestBeforeSending prevents malformed or
// oversized feedback from crossing the submission boundary.
func TestSupportSubmission_BoundsRequestBeforeSending(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		assert.Empty(t, r.Header.Get("Authorization"))
		_, _ = io.WriteString(w, `{"case_id":"`+testSupportCaseID+`"}`)
	}))
	defer server.Close()
	client := NewRepoClientWithEndpoint(server.URL)
	for _, payload := range [][]byte{nil, []byte(" \n"), []byte(`{"description":`), []byte(strings.Repeat("a", MaxSupportRequestBytes+1))} {
		_, err := client.SubmitSupportCase(context.Background(), payload)
		require.Error(t, err)
	}
	assert.Zero(t, calls.Load())
	payload := []byte(`{"description":"` + strings.Repeat("a", MaxSupportRequestBytes-len(`{"description":""}`)) + `"}`)
	require.Len(t, payload, MaxSupportRequestBytes)
	_, err := client.SubmitSupportCase(context.Background(), payload)
	require.NoError(t, err)
	assert.EqualValues(t, 1, calls.Load())
}

// TestSupportSubmission_TransportFailuresStayPrivate prevents transport errors
// from exposing credential-bearing URLs and prevents timeout retries.
func TestSupportSubmission_TransportFailuresStayPrivate(t *testing.T) {
	t.Parallel()
	t.Run("invalid endpoint", func(t *testing.T) {
		t.Parallel()
		_, err := NewRepoClientWithEndpoint("://test-support-token").SubmitSupportCase(context.Background(), []byte(`{}`))
		require.Error(t, err)
		assert.NotContains(t, err.Error(), "test-support-token")
	})
	t.Run("network unavailable", func(t *testing.T) {
		t.Parallel()
		server := httptest.NewServer(http.NotFoundHandler())
		server.Close()
		_, err := NewRepoClientWithEndpoint(server.URL).SubmitSupportCase(context.Background(), []byte(`{}`))
		require.Error(t, err)
		assert.EqualError(t, err, "could not reach the support API")
	})
	t.Run("canceled", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := NewRepoClientWithEndpoint("http://example.invalid").SubmitSupportCase(ctx, []byte(`{}`))
		assert.ErrorIs(t, err, context.Canceled)
	})
	t.Run("timeout", func(t *testing.T) {
		t.Parallel()
		var calls atomic.Int32
		release := make(chan struct{})
		server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
			calls.Add(1)
			<-release
		}))
		defer server.Close()
		defer close(release)
		_, err := NewRepoClientWithEndpoint(server.URL).WithTimeout(30*time.Millisecond).SubmitSupportCase(context.Background(), []byte(`{}`))
		require.EqualError(t, err, "support submission timed out")
		assert.EqualValues(t, 1, calls.Load())
	})
	t.Run("truncated response", func(t *testing.T) {
		t.Parallel()
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Length", "1000")
			_, _ = io.WriteString(w, "private-response-sentinel")
		}))
		defer server.Close()
		_, err := NewRepoClientWithEndpoint(server.URL).SubmitSupportCase(context.Background(), []byte(`{}`))
		require.Error(t, err)
		assert.NotContains(t, err.Error(), "private-response-sentinel")
		assert.False(t, errors.Is(err, io.ErrUnexpectedEOF))
	})
}
