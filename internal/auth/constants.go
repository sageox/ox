package auth

// ClientID is the OAuth client identifier for the ox CLI
const ClientID = "ox"

// DefaultScopes are the OAuth scopes requested during authentication
var DefaultScopes = []string{"user:profile", "sageox:write"}

// Device Flow endpoints (RFC 8628)
const (
	DeviceCodeEndpoint  = "/api/auth/device/code"  //nolint:gosec // not a credential
	DeviceTokenEndpoint = "/api/v1/device/token" //nolint:gosec // not a credential
	UserInfoEndpoint    = "/oauth2/userinfo"
)

// OAuth 2.0 endpoints
const (
	TokenEndpoint  = "/oauth2/token" //nolint:gosec // not a credential
	RevokeEndpoint = "/oauth2/revoke"
)
