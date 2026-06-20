package api

import "encoding/json"

// RepoService abstracts the SageOx repo API for testability.
type RepoService interface {
	RegisterRepo(req *RepoInitRequest) (*RepoInitResponse, error)
	GetDoctorIssues(repoID string) (*DoctorResponse, error)
	NotifyUninstall(repoID, repoSalt string) error
	NotifyImport(teamID string, metadata any) (recordingID string, err error)
	MergeRepo(repoID string, markers map[string]json.RawMessage) (*MergeRepoResponse, *RedirectInfo, error)
	Endpoint() string
	WithAuthToken(token string) *RepoClient
}

// compile-time assertion: RepoClient implements RepoService
var _ RepoService = (*RepoClient)(nil)
