package gitutil

// GitHTTPTimeoutFlags returns git config flags that bound DNS/TCP/TLS connection
// time and detect stalled transfers. Without these, git inherits the OS DNS resolver
// timeout (~13 min on macOS) which blocks background operations.
//
//   - http.connectTimeout=10: fail DNS+TCP+TLS within 10s
//   - http.lowSpeedLimit=1000: minimum bytes/sec during transfer
//   - http.lowSpeedTime=15: abort if below lowSpeedLimit for 15s
func GitHTTPTimeoutFlags() []string {
	return []string{
		"-c", "http.connectTimeout=10",
		"-c", "http.lowSpeedLimit=1000",
		"-c", "http.lowSpeedTime=15",
	}
}
