package auth

// ExportBearerForEndpoint returns the bearer to attach to a telemetry (OTLP)
// export for ep, or "" when nothing should be sent.
//
// This is the ONLY token accessor an exporter should use. GetTokenForEndpoint
// hands back whatever is on disk, expired or not, and an exporter that sends
// it just gets a 401 per batch: the ox-daemon and buzz agents did exactly
// that for a week at ~15k rejects/hour in prod (sageox-9naj9). It is also
// deliberately NOT EnsureValidTokenForEndpoint — a refresh is a network call
// in the export path, and a background batch racing the foreground command's
// own refresh can revoke the token it just minted. The exporter yields instead:
// the next API call refreshes on its own, and the export after that picks up
// the new token from disk.
//
// Env-sourced tokens (SAGEOX_TOKEN) have no expiry the client can check —
// their ExpiresAt is a synthetic TTL — so they are returned as-is and the
// server's 401 is the source of truth; the exporter's round tripper is what
// stops re-sending one the server has already rejected.
func ExportBearerForEndpoint(ep string) string {
	tok, err := GetTokenForEndpoint(ep)
	return exportBearer(ep, tok, err)
}

func exportBearer(ep string, tok *StoredToken, err error) string {
	if err != nil || tok == nil || tok.AccessToken == "" {
		return ""
	}
	if isEnvToken(ep, tok) {
		return tok.AccessToken
	}
	// Zero buffer on purpose: a token good for one more second is still good,
	// and dropping early would lose the tail of every short command's trace.
	if tok.IsExpired(0) {
		return ""
	}
	return tok.AccessToken
}
