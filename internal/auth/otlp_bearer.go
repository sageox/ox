package auth

import (
	"log/slog"
	"sync"
)

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
	return exportBearerNoting(ep, tok, err)
}

// exportBearerNoting is ExportBearerForEndpoint after the disk read: decide,
// then record the yield state. Only a usable bearer ends an expired-token
// pause. Logged-out / unreadable yields "" with expired=false, and must NOT
// flip the state, or a logout after an expiry would log "resumed: fresh
// token on disk" with no token on disk.
func exportBearerNoting(ep string, tok *StoredToken, err error) string {
	bearer, expired := exportBearer(ep, tok, err)
	if expired || bearer != "" {
		noteExportYield(ep, expired)
	}
	return bearer
}

// exportBearer is the pure decision. expired reports that a stored (non-env)
// token was present but past its expiry, i.e. the exporter is yielding for a
// reason the process cannot fix itself; a nil/empty token is "logged out"
// and is not reported, since that is a state the user chose.
func exportBearer(ep string, tok *StoredToken, err error) (bearer string, expired bool) {
	if err != nil || tok == nil || tok.AccessToken == "" {
		return "", false
	}
	if isEnvToken(ep, tok) {
		return tok.AccessToken, false
	}
	// Zero buffer on purpose: a token good for one more second is still good,
	// and dropping early would lose the tail of every short command's trace.
	if tok.IsExpired(0) {
		return "", true
	}
	return tok.AccessToken, false
}

// exportYield tracks, per endpoint, whether the exporter is currently yielding
// on an expired stored token, so the transition is logged once each way
// rather than once per batch. Yielding is silent on the wire by design (the
// batch is dropped client-side), which is exactly why the transition must be
// loud in the log: a daemon never refreshes its own token, so "traces stopped"
// is otherwise indistinguishable from "nothing happened".
var exportYield struct {
	mu      sync.Mutex
	expired map[string]bool
}

// noteExportYield records the current yield state for ep and logs on change.
// It returns true when the state changed.
func noteExportYield(ep string, expired bool) bool {
	exportYield.mu.Lock()
	defer exportYield.mu.Unlock()
	if exportYield.expired == nil {
		exportYield.expired = make(map[string]bool)
	}
	was := exportYield.expired[ep]
	if was == expired {
		return false
	}
	exportYield.expired[ep] = expired
	if expired {
		slog.Warn("otel export paused: stored token expired; dropping batches until a refresh lands on disk",
			"endpoint", ep)
	} else {
		slog.Warn("otel export resumed: fresh token on disk", "endpoint", ep)
	}
	return true
}
