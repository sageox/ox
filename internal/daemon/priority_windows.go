package daemon

// lowerDaemonPriority is a no-op on Windows, which has no setpriority(2).
// Priority classes exist there, but nothing indicates the daemon competes with
// foreground work on Windows, so it is not worth the risk of changing them.
func lowerDaemonPriority() error { return nil }

// daemonNiceness is reported in the startup log; nothing is changed on Windows.
const daemonNiceness = 0
