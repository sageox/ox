package daemon

import (
	"crypto/sha256"
	"encoding/hex"
)

// pipeName maps the logical socket path to a Windows-safe named pipe. Basing
// the name on the path keeps XDG-isolated test/ephemeral daemons from sharing
// the production pipe while remaining deterministic for clients.
func pipeName(path string) string {
	digest := sha256.Sum256([]byte(path))
	return "sageox-daemon-" + hex.EncodeToString(digest[:8])
}
