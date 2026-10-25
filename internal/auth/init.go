package auth

import (
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/gitserver"
)

func init() {
	endpoint.LoggedInEndpointsGetter = GetLoggedInEndpoints
	gitserver.TeamCoworkerGetter = func() (ep, name, id string) {
		ep = EnvTokenEndpoint()
		if c, _ := TeamCoworker(ep); c != nil {
			name, id = c.Name(), c.ID
		}
		return ep, name, id
	}
}
