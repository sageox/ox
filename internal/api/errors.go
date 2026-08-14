package api

import "errors"

// ErrVersionUnsupported is returned when the server indicates the CLI version is no longer supported
var ErrVersionUnsupported = errors.New("CLI version no longer supported by server")

// ErrUnauthorized is returned when the API returns 401 Unauthorized
var ErrUnauthorized = errors.New("authentication required: run 'ox login' first")

// ErrCLISettingsUnsupported is returned by older servers that have not yet
// deployed the optional CLI settings endpoint. Callers must fall back to local
// defaults rather than treating this capability gap as a sync failure.
var ErrCLISettingsUnsupported = errors.New("CLI settings endpoint unsupported by server")

// ErrForbidden is returned when the API returns 403 Forbidden
var ErrForbidden = errors.New("access denied: you are not a member of this team — request an invite URL from a team admin")

// ErrReadOnly is returned when the user has viewer (read-only) access to a public repo
var ErrReadOnly = errors.New("read-only access: you are a viewer on this public repo")
