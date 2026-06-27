package auth

// ClientID is the OAuth client identifier for the ox CLI
const ClientID = "ox"

// DefaultScopes are the OAuth scopes requested during authentication
var DefaultScopes = []string{"user:profile", "sageox:write"}

// Device Flow endpoints (RFC 8628)
const (
	DeviceCodeEndpoint  = "/api/auth/device/code" //nolint:gosec // not a credential
	DeviceTokenEndpoint = "/api/v1/device/token"  //nolint:gosec // not a credential
	UserInfoEndpoint    = "/oauth2/userinfo"
)

// OAuth 2.0 endpoints
const (
	TokenEndpoint  = "/oauth2/token" //nolint:gosec // not a credential
	RevokeEndpoint = "/oauth2/revoke"
)

// PATPrefix marks a SageOx Personal Access Token (oxp_<body>_<crc>). PATs carry
// no OAuth identity, so they validate against AuthMeEndpoint, not UserInfoEndpoint.
const PATPrefix = "oxp_" //nolint:gosec // not a credential

// AuthMeEndpoint introspects the caller's token (including PATs) and returns
// {user, token_type}. It accepts `Authorization: Bearer oxp_…`, where
// /oauth2/userinfo (OAuth/opaque tokens only) returns "Token not found".
const AuthMeEndpoint = "/api/v1/auth/me" //nolint:gosec // not a credential
