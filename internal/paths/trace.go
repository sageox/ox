package paths

import "path/filepath"

// TraceSpoolDir holds local, unuploaded Claude Code trace payloads.
func TraceSpoolDir() string { return filepath.Join(CacheDir(), "trace", "spool") }

// TraceStateDir holds the per-user trace receiver's locks and process identity.
func TraceStateDir() string { return filepath.Join(StateDir(), "trace") }

// TraceLogPath is the detached receiver's private diagnostic log.
func TraceLogPath() string { return filepath.Join(TempDir(), "trace.log") }
