package auth

import (
	"github.com/sageox/ox/internal/endpoint"
)

func init() {
	endpoint.LoggedInEndpointsGetter = GetLoggedInEndpoints
}
