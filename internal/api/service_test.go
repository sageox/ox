package api

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMockRepoService_NilFuncsReturnZeroValues(t *testing.T) {
	m := &MockRepoService{}

	resp, err := m.RegisterRepo(&RepoInitRequest{RepoID: "r-123"})
	assert.Nil(t, resp)
	assert.NoError(t, err)

	doc, err := m.GetDoctorIssues("r-123")
	assert.Nil(t, doc)
	assert.NoError(t, err)

	assert.NoError(t, m.NotifyUninstall("r-123", "salt"))
	_, niErr := m.NotifyImport("t-123", nil)
	assert.NoError(t, niErr)

	mr, ri, err := m.MergeRepo("r-123", nil)
	assert.Nil(t, mr)
	assert.Nil(t, ri)
	assert.NoError(t, err)

	assert.Equal(t, "", m.Endpoint())
	assert.Nil(t, m.WithAuthToken("tok"))
}

func TestMockRepoService_RegisterRepo(t *testing.T) {
	want := &RepoInitResponse{RepoID: "r-abc", TeamID: "t-xyz"}
	m := &MockRepoService{
		RegisterRepoFunc: func(req *RepoInitRequest) (*RepoInitResponse, error) {
			require.Equal(t, "r-abc", req.RepoID)
			return want, nil
		},
	}

	got, err := m.RegisterRepo(&RepoInitRequest{RepoID: "r-abc"})
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

func TestMockRepoService_GetDoctorIssues(t *testing.T) {
	want := &DoctorResponse{CheckedAt: "2026-01-01T00:00:00Z"}
	m := &MockRepoService{
		GetDoctorIssuesFunc: func(repoID string) (*DoctorResponse, error) {
			require.Equal(t, "r-123", repoID)
			return want, nil
		},
	}

	got, err := m.GetDoctorIssues("r-123")
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

func TestMockRepoService_NotifyUninstall(t *testing.T) {
	wantErr := fmt.Errorf("uninstall failed")
	m := &MockRepoService{
		NotifyUninstallFunc: func(repoID, repoSalt string) error {
			require.Equal(t, "r-123", repoID)
			require.Equal(t, "salt", repoSalt)
			return wantErr
		},
	}

	err := m.NotifyUninstall("r-123", "salt")
	assert.ErrorIs(t, err, wantErr)
}

func TestMockRepoService_MergeRepo(t *testing.T) {
	wantResp := &MergeRepoResponse{Canonical: "r-winner"}
	wantRedirect := &RedirectInfo{
		Repo: &RedirectMapping{From: "r-old", To: "r-winner"},
	}
	markers := map[string]json.RawMessage{"f": json.RawMessage(`{}`)}

	m := &MockRepoService{
		MergeRepoFunc: func(repoID string, m map[string]json.RawMessage) (*MergeRepoResponse, *RedirectInfo, error) {
			require.Equal(t, "r-123", repoID)
			require.Contains(t, m, "f")
			return wantResp, wantRedirect, nil
		},
	}

	gotResp, gotRedirect, err := m.MergeRepo("r-123", markers)
	require.NoError(t, err)
	assert.Equal(t, wantResp, gotResp)
	assert.Equal(t, wantRedirect, gotRedirect)
}

func TestMockRepoService_Endpoint(t *testing.T) {
	m := &MockRepoService{
		EndpointFunc: func() string {
			return "https://test.sageox.ai"
		},
	}

	assert.Equal(t, "https://test.sageox.ai", m.Endpoint())
}

// TestMockRepoService_AsRepoService demonstrates the consumer pattern: create a
// MockRepoService, override methods, and interact through the RepoService interface.
func TestMockRepoService_AsRepoService(t *testing.T) {
	mock := &MockRepoService{
		RegisterRepoFunc: func(req *RepoInitRequest) (*RepoInitResponse, error) {
			return &RepoInitResponse{RepoID: req.RepoID, TeamID: "team-1"}, nil
		},
		EndpointFunc: func() string { return "https://test.sageox.ai" },
	}

	var svc RepoService = mock

	resp, err := svc.RegisterRepo(&RepoInitRequest{RepoID: "repo-1"})
	require.NoError(t, err)
	assert.Equal(t, "repo-1", resp.RepoID)
	assert.Equal(t, "team-1", resp.TeamID)
	assert.Equal(t, "https://test.sageox.ai", svc.Endpoint())
}

// TestMockRepoService_ErrorInjection demonstrates injecting errors through the
// RepoService interface for testing error handling paths.
func TestMockRepoService_ErrorInjection(t *testing.T) {
	mock := &MockRepoService{
		RegisterRepoFunc: func(_ *RepoInitRequest) (*RepoInitResponse, error) {
			return nil, fmt.Errorf("server unavailable")
		},
		GetDoctorIssuesFunc: func(_ string) (*DoctorResponse, error) {
			return nil, fmt.Errorf("unauthorized")
		},
	}

	var svc RepoService = mock

	resp, err := svc.RegisterRepo(&RepoInitRequest{RepoID: "repo-1"})
	assert.Nil(t, resp)
	assert.EqualError(t, err, "server unavailable")

	doc, err := svc.GetDoctorIssues("repo-1")
	assert.Nil(t, doc)
	assert.EqualError(t, err, "unauthorized")
}

// TestMockRepoService_MergeWorkflow demonstrates a multi-step consumer that uses
// several RepoService methods together, as real merge logic would.
func TestMockRepoService_MergeWorkflow(t *testing.T) {
	mock := &MockRepoService{
		EndpointFunc: func() string { return "https://test.sageox.ai" },
		MergeRepoFunc: func(repoID string, markers map[string]json.RawMessage) (*MergeRepoResponse, *RedirectInfo, error) {
			return &MergeRepoResponse{
					Canonical: "repo-winner",
					Merged:    []string{repoID},
				}, &RedirectInfo{
					Repo: &RedirectMapping{From: repoID, To: "repo-winner"},
				}, nil
		},
		NotifyImportFunc: func(teamID string, metadata any) (string, error) {
			require.Equal(t, "team-1", teamID)
			return "", nil
		},
	}

	var svc RepoService = mock

	// step 1: merge
	mergeResp, redirect, err := svc.MergeRepo("repo-old", map[string]json.RawMessage{
		"config.json": json.RawMessage(`{"version":1}`),
	})
	require.NoError(t, err)
	assert.Equal(t, "repo-winner", mergeResp.Canonical)
	assert.Contains(t, mergeResp.Merged, "repo-old")
	require.NotNil(t, redirect.Repo)
	assert.Equal(t, "repo-winner", redirect.Repo.To)

	// step 2: notify import on the winning repo's team
	_, err = svc.NotifyImport("team-1", map[string]string{"source": "merge"})
	require.NoError(t, err)
}

// TestRepoClient_SatisfiesRepoService documents the compile-time assertion
// that RepoClient implements the RepoService interface.
func TestRepoClient_SatisfiesRepoService(t *testing.T) {
	// mirrors the compile-time assertion in service.go:
	//   var _ RepoService = (*RepoClient)(nil)
	var _ RepoService = (*RepoClient)(nil)
}
