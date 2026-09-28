package trace

import "github.com/sageox/ox/internal/paths"

// SpoolDir returns the canonical local trace spool directory.
func SpoolDir() string { return paths.TraceSpoolDir() }

// StateDir returns the canonical receiver state directory.
func StateDir() string { return paths.TraceStateDir() }

// LogPath returns the canonical detached receiver log path.
func LogPath() string { return paths.TraceLogPath() }
