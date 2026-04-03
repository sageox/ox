package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/sageox/ox/pkg/ndjson"
)

var (
	// ErrAdapterTimeout is returned when an adapter binary does not respond within the timeout.
	ErrAdapterTimeout = errors.New("adapter timed out")

	// ErrAdapterCrashed is returned when an adapter process exits unexpectedly.
	ErrAdapterCrashed = errors.New("adapter process crashed")

	// ErrInvalidResponse is returned when an adapter produces invalid JSON.
	ErrInvalidResponse = errors.New("adapter returned invalid response")

	// ErrProtocolMismatch is returned when an adapter's protocol version is incompatible.
	ErrProtocolMismatch = errors.New("adapter protocol version mismatch")
)

// allowlistedEnvVars are exact-match variable names always passed to adapter processes.
var allowlistedEnvVars = map[string]bool{
	"HOME":   true,
	"PATH":   true,
	"TMPDIR": true,
}

// allowlistedEnvPrefixes are prefix patterns; any variable starting with these is passed through.
var allowlistedEnvPrefixes = []string{
	"XDG_",
}

// ExternalAdapter implements Adapter and IncrementalReader by calling an
// external adapter binary via subprocess (one-shot) or serve-mode pipe.
type ExternalAdapter struct {
	binaryPath string
	info       *adapterprotocol.InfoResponse

	// serve-mode state (lazily initialized)
	serveMu   sync.Mutex
	serveCmd  *exec.Cmd
	serveIn   io.WriteCloser
	serveOut  *ndjson.Scanner
	serveEnc  *ndjson.Encoder
	serveSeq  int

	// timeouts
	oneShotTimeout time.Duration
	serveTimeout   time.Duration
}

// NewExternalAdapter creates an ExternalAdapter wrapping the binary at the given path.
// It calls `info` to populate adapter metadata.
func NewExternalAdapter(binaryPath string) (*ExternalAdapter, error) {
	ea := &ExternalAdapter{
		binaryPath:     binaryPath,
		oneShotTimeout: 5 * time.Second,
		serveTimeout:   100 * time.Millisecond,
	}

	// call info to get adapter metadata
	info, err := ea.callInfo()
	if err != nil {
		return nil, fmt.Errorf("adapter info failed: %w", err)
	}
	ea.info = info

	return ea, nil
}

// NewExternalAdapterWithInfo creates an ExternalAdapter with pre-populated info.
// Used when info has already been called (e.g., during discovery).
func NewExternalAdapterWithInfo(binaryPath string, info *adapterprotocol.InfoResponse) *ExternalAdapter {
	return &ExternalAdapter{
		binaryPath:     binaryPath,
		info:           info,
		oneShotTimeout: 5 * time.Second,
		serveTimeout:   100 * time.Millisecond,
	}
}

// Name returns the adapter name from the info response.
func (ea *ExternalAdapter) Name() string {
	if ea.info != nil {
		return ea.info.Name
	}
	return ""
}

// Detect calls the adapter's detect subcommand.
func (ea *ExternalAdapter) Detect() bool {
	out, err := ea.execOneShot("detect")
	if err != nil {
		return false
	}
	var resp adapterprotocol.DetectResponse
	if err := json.Unmarshal(out, &resp); err != nil {
		return false
	}
	return resp.Detected
}

// FindSessionFile calls find-session via one-shot mode.
// In production, serve mode is preferred, but one-shot provides a fallback.
func (ea *ExternalAdapter) FindSessionFile(agentID string, since time.Time) (string, error) {
	params := adapterprotocol.FindSessionParams{
		AgentID:  agentID,
		RepoRoot: os.Getenv("OX_REPO_ROOT"),
		RepoID:   os.Getenv("OX_REPO_ID"),
		TeamID:   os.Getenv("OX_TEAM_ID"),
		Since:    since.UTC().Format(time.RFC3339),
	}

	paramsBytes, err := json.Marshal(params)
	if err != nil {
		return "", fmt.Errorf("marshal params: %w", err)
	}

	out, err := ea.execOneShot("find-session", "--params", string(paramsBytes))
	if err != nil {
		return "", err
	}

	var result adapterprotocol.FindSessionResult
	if err := json.Unmarshal(out, &result); err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidResponse, err)
	}

	if result.SessionFile == "" {
		return "", ErrSessionNotFound
	}
	return result.SessionFile, nil
}

// Read calls the adapter's read subcommand.
func (ea *ExternalAdapter) Read(sessionPath string) ([]RawEntry, error) {
	out, err := ea.execOneShot("read", "--session-file", sessionPath)
	if err != nil {
		return nil, err
	}

	var result adapterprotocol.ReadResult
	if err := json.Unmarshal(out, &result); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidResponse, err)
	}

	return protocolToInternal(result.Entries), nil
}

// ReadMetadata calls the adapter's read-metadata subcommand.
func (ea *ExternalAdapter) ReadMetadata(sessionPath string) (*SessionMetadata, error) {
	out, err := ea.execOneShot("read-metadata", "--session-file", sessionPath)
	if err != nil {
		return nil, err
	}

	var result adapterprotocol.ReadMetadataResult
	if err := json.Unmarshal(out, &result); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidResponse, err)
	}

	return &SessionMetadata{
		AgentVersion: result.AgentVersion,
		Model:        result.Model,
	}, nil
}

// Watch is not supported for external adapters in one-shot mode.
// The daemon uses serve-mode push events instead.
func (ea *ExternalAdapter) Watch(_ context.Context, _ string) (<-chan RawEntry, error) {
	return nil, ErrWatchNotSupported
}

// ReadFromOffset implements IncrementalReader via one-shot subprocess call.
func (ea *ExternalAdapter) ReadFromOffset(path string, offset int64) ([]RawEntry, int64, error) {
	params := adapterprotocol.ReadFromOffsetParams{
		SessionFile: path,
		Offset:      offset,
	}
	paramsBytes, err := json.Marshal(params)
	if err != nil {
		return nil, offset, fmt.Errorf("marshal params: %w", err)
	}

	out, err := ea.execOneShot("read-from-offset", "--params", string(paramsBytes))
	if err != nil {
		return nil, offset, err
	}

	var result adapterprotocol.ReadFromOffsetResult
	if err := json.Unmarshal(out, &result); err != nil {
		return nil, offset, fmt.Errorf("%w: %v", ErrInvalidResponse, err)
	}

	return protocolToInternal(result.Entries), result.NewOffset, nil
}

// Info returns the cached adapter info.
func (ea *ExternalAdapter) Info() *adapterprotocol.InfoResponse {
	return ea.info
}

// BinaryPath returns the path to the adapter binary.
func (ea *ExternalAdapter) BinaryPath() string {
	return ea.binaryPath
}

// Diagnose calls the adapter's diagnose subcommand.
func (ea *ExternalAdapter) Diagnose(repoRoot, scope string) (*adapterprotocol.DiagnoseResult, error) {
	out, err := ea.execOneShot("diagnose", "--repo-root", repoRoot, "--scope", scope)
	if err != nil {
		return nil, err
	}

	var result adapterprotocol.DiagnoseResult
	if err := json.Unmarshal(out, &result); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidResponse, err)
	}

	return &result, nil
}

// InstallHooks calls the adapter's install-hooks subcommand.
func (ea *ExternalAdapter) InstallHooks(repoRoot, scope string) (*adapterprotocol.InstallHooksResponse, error) {
	out, err := ea.execOneShot("install-hooks", "--repo-root", repoRoot, "--scope", scope)
	if err != nil {
		return nil, err
	}

	var result adapterprotocol.InstallHooksResponse
	if err := json.Unmarshal(out, &result); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidResponse, err)
	}

	return &result, nil
}

// CheckHooks calls the adapter's check-hooks subcommand.
func (ea *ExternalAdapter) CheckHooks(repoRoot, scope string) (*adapterprotocol.CheckHooksResponse, error) {
	out, err := ea.execOneShot("check-hooks", "--repo-root", repoRoot, "--scope", scope)
	if err != nil {
		return nil, err
	}

	var result adapterprotocol.CheckHooksResponse
	if err := json.Unmarshal(out, &result); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidResponse, err)
	}

	return &result, nil
}

// --- internal helpers ---

func (ea *ExternalAdapter) callInfo() (*adapterprotocol.InfoResponse, error) {
	out, err := ea.execOneShot("info")
	if err != nil {
		return nil, err
	}

	var info adapterprotocol.InfoResponse
	if err := json.Unmarshal(out, &info); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidResponse, err)
	}

	return &info, nil
}

// execOneShot runs a one-shot subcommand and returns stdout bytes.
func (ea *ExternalAdapter) execOneShot(subcommand string, args ...string) ([]byte, error) {
	cmdArgs := append([]string{subcommand}, args...)
	ctx, cancel := context.WithTimeout(context.Background(), ea.oneShotTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, ea.binaryPath, cmdArgs...)
	cmd.Env = ea.buildEnv()

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return nil, fmt.Errorf("%w: %s %s", ErrAdapterTimeout, ea.binaryPath, subcommand)
	}
	if err != nil {
		// check for error response in stdout (adapter may exit non-zero with a JSON error)
		if stdout.Len() > 0 {
			var errResp struct {
				Error string `json:"error"`
			}
			if json.Unmarshal(stdout.Bytes(), &errResp) == nil && errResp.Error != "" {
				return nil, fmt.Errorf("adapter error: %s", errResp.Error)
			}
		}
		return nil, fmt.Errorf("adapter %s %s failed: %w (stderr: %s)",
			ea.binaryPath, subcommand, err, stderr.String())
	}

	return bytes.TrimSpace(stdout.Bytes()), nil
}

// buildEnv constructs a sanitized environment for the adapter subprocess.
// Only allowlisted variables, OX_* protocol vars, and adapter-declared
// required_env vars are included. Daemon secrets are never leaked.
func (ea *ExternalAdapter) buildEnv() []string {
	var requiredEnv []string
	if ea.info != nil {
		requiredEnv = ea.info.RequiredEnv
	}
	return SanitizedEnv(os.Environ(), requiredEnv)
}

// SanitizedEnv builds a sanitized environment for adapter subprocess execution.
// It filters environ (typically os.Environ()) to only include:
//   - Exact-match allowlisted vars: HOME, PATH, TMPDIR
//   - Prefix-match allowlisted vars: XDG_*
//   - OX_* protocol vars (OX_PROTOCOL_VERSION, OX_REPO_ROOT, OX_REPO_ID, OX_TEAM_ID)
//   - Any additional vars declared in the adapter's required_env list
//
// All other variables (API keys, tokens, secrets) are stripped.
func SanitizedEnv(environ []string, requiredEnv []string) []string {
	// build a set of adapter-declared required env var names for fast lookup
	required := make(map[string]bool, len(requiredEnv))
	for _, name := range requiredEnv {
		required[name] = true
	}

	env := make([]string, 0, len(allowlistedEnvVars)+len(requiredEnv)+4)

	// track which OX_ vars we found so we can inject defaults for missing ones
	foundOX := make(map[string]bool)

	for _, entry := range environ {
		name, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}

		if isAllowlisted(name) || required[name] {
			env = append(env, entry)
		}

		// track OX_ vars we see (they pass through isAllowlisted via the OX_ prefix check)
		if strings.HasPrefix(name, "OX_") {
			foundOX[name] = true
		}
	}

	// always inject OX_PROTOCOL_VERSION if not already present
	if !foundOX["OX_PROTOCOL_VERSION"] {
		env = append(env, fmt.Sprintf("OX_PROTOCOL_VERSION=%d", adapterprotocol.ProtocolVersion))
	}

	return env
}

// isAllowlisted returns true if the variable name matches the allowlist.
func isAllowlisted(name string) bool {
	if allowlistedEnvVars[name] {
		return true
	}
	// OX_* protocol vars are always passed through
	if strings.HasPrefix(name, "OX_") {
		return true
	}
	for _, prefix := range allowlistedEnvPrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// protocolToInternal converts protocol RawEntry slice to internal RawEntry slice.
func protocolToInternal(entries []adapterprotocol.RawEntry) []RawEntry {
	result := make([]RawEntry, len(entries))
	for i, e := range entries {
		ts, _ := time.Parse(time.RFC3339, e.Timestamp)
		result[i] = RawEntry{
			Timestamp:  ts,
			Role:       e.Role,
			Content:    e.Content,
			ToolName:   e.ToolName,
			ToolInput:  e.ToolInput,
			ToolOutput: e.ToolOutput,
			IsError:    e.IsError,
			CallID:     e.CallID,
		}
	}
	return result
}

// Close shuts down any serve-mode process.
func (ea *ExternalAdapter) Close() error {
	ea.serveMu.Lock()
	defer ea.serveMu.Unlock()

	if ea.serveCmd != nil && ea.serveCmd.Process != nil {
		// send shutdown request
		if ea.serveEnc != nil {
			shutReq := adapterprotocol.Request{
				ID:     ea.nextSeqLocked(),
				Method: adapterprotocol.MethodShutdown,
			}
			_ = ea.serveEnc.Encode(shutReq)
		}
		if ea.serveIn != nil {
			ea.serveIn.Close()
		}
		// wait briefly then force-kill
		done := make(chan error, 1)
		go func() { done <- ea.serveCmd.Wait() }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			ea.serveCmd.Process.Kill()
			<-done
		}
		ea.serveCmd = nil
	}
	return nil
}

func (ea *ExternalAdapter) nextSeqLocked() int {
	ea.serveSeq++
	return ea.serveSeq
}

// hasCapability checks if the adapter declares a specific capability.
func (ea *ExternalAdapter) hasCapability(cap string) bool {
	if ea.info == nil {
		return false
	}
	for _, c := range ea.info.Capabilities {
		if c == cap {
			return true
		}
	}
	return false
}
