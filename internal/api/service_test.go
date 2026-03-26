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
	assert.NoError(t, m.NotifyImport("t-123", nil))

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
