package api

// plan_activity_test.go covers NotifyPlanActivity (see repo.go): the client
// half of the caller-driven plan_id -> plan index (bead sageox-gqgkg).

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNotifyPlanActivity_Success(t *testing.T) {
	t.Parallel()
	var receivedPath, receivedAuth string
	var receivedBody PlanActivityNotification

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		receivedAuth = r.Header.Get("Authorization")
		require.NoError(t, json.NewDecoder(r.Body).Decode(&receivedBody))
		w.WriteHeader(http.StatusOK)
	}))
	defer mockServer.Close()

	client := &RepoClient{
		baseURL:    mockServer.URL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		authToken:  "test-auth-token",
	}

	err := client.NotifyPlanActivity("team_abc", "pln_xyz", PlanActivityNotification{Event: "approved", Status: "approved"})
	require.NoError(t, err)

	assert.Equal(t, "/api/v1/teams/team_abc/plans/pln_xyz/activity", receivedPath)
	assert.Equal(t, "Bearer test-auth-token", receivedAuth)
	assert.Equal(t, "approved", receivedBody.Event)
	assert.Equal(t, "approved", receivedBody.Status)
}

// The repo id rides the body when the project has one, and no key is sent
// when it does not, so an older server and a team plan both see what they did.
func TestNotifyPlanActivity_SendsRepoIDOnlyWhenKnown(t *testing.T) {
	t.Parallel()
	var bodies []map[string]any
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		bodies = append(bodies, body)
		w.WriteHeader(http.StatusOK)
	}))
	defer mockServer.Close()
	client := &RepoClient{baseURL: mockServer.URL, httpClient: &http.Client{Timeout: 10 * time.Second}}

	require.NoError(t, client.NotifyPlanActivity("team_abc", "pln_xyz",
		PlanActivityNotification{Event: "created", RepoID: "repo_01961b1e-4c2f-7000-8000-00000000a001"}))
	require.NoError(t, client.NotifyPlanActivity("team_abc", "pln_xyz", PlanActivityNotification{Event: "revised"}))

	require.Len(t, bodies, 2)
	assert.Equal(t, "repo_01961b1e-4c2f-7000-8000-00000000a001", bodies[0]["repo_id"])
	_, has := bodies[1]["repo_id"]
	assert.False(t, has, "no repo_id key when the project has none")
}

// TestNotifyPlanActivity_NotFoundIsNotAnError mirrors the stub's contract: an
// older/undeployed server 404ing on this endpoint must not surface as an
// error to a fire-and-forget caller.
func TestNotifyPlanActivity_NotFoundIsNotAnError(t *testing.T) {
	t.Parallel()
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer mockServer.Close()

	client := &RepoClient{baseURL: mockServer.URL, httpClient: &http.Client{Timeout: 10 * time.Second}}
	err := client.NotifyPlanActivity("team_abc", "pln_xyz", PlanActivityNotification{Event: "worked"})
	require.NoError(t, err)
}

func TestNotifyPlanActivity_ServerErrorReturnsError(t *testing.T) {
	t.Parallel()
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer mockServer.Close()

	client := &RepoClient{baseURL: mockServer.URL, httpClient: &http.Client{Timeout: 10 * time.Second}}
	err := client.NotifyPlanActivity("team_abc", "pln_xyz", PlanActivityNotification{Event: "realized"})
	if err == nil {
		t.Fatal("want an error on 500")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error = %v, want it to mention the status code", err)
	}
}

func TestNotifyPlanActivity_NoAuthTokenOmitsHeader(t *testing.T) {
	t.Parallel()
	var sawAuthHeader bool
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuthHeader = r.Header.Get("Authorization") != ""
		w.WriteHeader(http.StatusOK)
	}))
	defer mockServer.Close()

	client := &RepoClient{baseURL: mockServer.URL, httpClient: &http.Client{Timeout: 10 * time.Second}}
	require.NoError(t, client.NotifyPlanActivity("team_abc", "pln_xyz", PlanActivityNotification{Event: "abandoned"}))
	assert.False(t, sawAuthHeader, "no Authorization header should be sent with no auth token configured")
}
