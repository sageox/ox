package api

import "encoding/json"

// MockRepoService implements RepoService with configurable function fields for testing.
type MockRepoService struct {
	RegisterRepoFunc    func(req *RepoInitRequest) (*RepoInitResponse, error)
	GetDoctorIssuesFunc func(repoID string) (*DoctorResponse, error)
	NotifyUninstallFunc func(repoID, repoSalt string) error
	NotifyImportFunc    func(teamID string, metadata any) (string, error)
	MergeRepoFunc       func(repoID string, markers map[string]json.RawMessage) (*MergeRepoResponse, *RedirectInfo, error)
	EndpointFunc        func() string
	WithAuthTokenFunc   func(token string) *RepoClient
}

// compile-time assertion: MockRepoService implements RepoService
var _ RepoService = (*MockRepoService)(nil)

func (m *MockRepoService) RegisterRepo(req *RepoInitRequest) (*RepoInitResponse, error) {
	if m.RegisterRepoFunc != nil {
		return m.RegisterRepoFunc(req)
	}
	return nil, nil
}

func (m *MockRepoService) GetDoctorIssues(repoID string) (*DoctorResponse, error) {
	if m.GetDoctorIssuesFunc != nil {
		return m.GetDoctorIssuesFunc(repoID)
	}
	return nil, nil
}

func (m *MockRepoService) NotifyUninstall(repoID, repoSalt string) error {
	if m.NotifyUninstallFunc != nil {
		return m.NotifyUninstallFunc(repoID, repoSalt)
	}
	return nil
}

func (m *MockRepoService) NotifyImport(teamID string, metadata any) (string, error) {
	if m.NotifyImportFunc != nil {
		return m.NotifyImportFunc(teamID, metadata)
	}
	return "", nil
}

func (m *MockRepoService) MergeRepo(repoID string, markers map[string]json.RawMessage) (*MergeRepoResponse, *RedirectInfo, error) {
	if m.MergeRepoFunc != nil {
		return m.MergeRepoFunc(repoID, markers)
	}
	return nil, nil, nil
}

func (m *MockRepoService) Endpoint() string {
	if m.EndpointFunc != nil {
		return m.EndpointFunc()
	}
	return ""
}

func (m *MockRepoService) WithAuthToken(token string) *RepoClient {
	if m.WithAuthTokenFunc != nil {
		return m.WithAuthTokenFunc(token)
	}
	return nil
}
