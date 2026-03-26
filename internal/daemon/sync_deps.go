package daemon

import (
	"github.com/sageox/ox/internal/gitserver"
)

// CredentialProvider abstracts git credential loading for testability.
type CredentialProvider interface {
	LoadCredentialsForEndpoint(endpoint string) (*gitserver.GitCredentials, error)
}

// realCredentialProvider delegates to the gitserver package functions.
type realCredentialProvider struct{}

func (r *realCredentialProvider) LoadCredentialsForEndpoint(endpoint string) (*gitserver.GitCredentials, error) {
	return gitserver.LoadCredentialsForEndpoint(endpoint)
}
