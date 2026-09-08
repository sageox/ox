package gitserver

import (
	"context"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/sageox/ox/internal/repotools"
)

var (
	ErrReadTokenUnavailable = errors.New("read sync requires the current SAGEOX_TOKEN team access token for the selected endpoint")
	ErrUnsafeReadTransport  = errors.New("unsafe ledger read transport configuration")
)

// ValidateReadURL binds discovery to the selected HTTPS authority and repo.
// This is the proposed backend contract, not a derivation from a GitLab URL.
func ValidateReadURL(endpointURL, repoID, readURL string) error {
	ep, err := url.Parse(endpointURL)
	if err != nil || ep.Scheme != "https" || ep.Host == "" || ep.User != nil || ep.RawQuery != "" || ep.Fragment != "" || (ep.Path != "" && ep.Path != "/") {
		return ErrUnsafeReadTransport
	}
	id, err := repotools.ParseRepoID(repoID)
	if err != nil || repoID != "repo_"+id.String() {
		return ErrUnsafeReadTransport
	}
	u, err := url.Parse(readURL)
	if err != nil || u.Scheme != "https" || u.Host != ep.Host || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" || u.Opaque != "" || u.ForceQuery {
		return ErrUnsafeReadTransport
	}
	if u.Path != "/api/v1/cli/repos/"+repoID+"/ledger.git" {
		return ErrUnsafeReadTransport
	}
	return nil
}

// ValidateReadRequestURL permits only the resource and documented read children.
// In particular a same-host sibling repo or receive-pack is never credentialed.
func ValidateReadRequestURL(endpointURL, repoID, readURL, requestURL string) error {
	if err := ValidateReadURL(endpointURL, repoID, readURL); err != nil {
		return err
	}
	base, _ := url.Parse(readURL)
	u, err := url.Parse(requestURL)
	if err != nil || u.Scheme != base.Scheme || u.Host != base.Host || u.User != nil || u.RawPath != "" || u.Fragment != "" || u.Opaque != "" || u.ForceQuery {
		return ErrUnsafeReadTransport
	}
	child := strings.TrimPrefix(u.Path, base.Path)
	if u.Path != base.Path+child {
		return ErrUnsafeReadTransport
	}
	if child == "/info/refs" && (u.RawQuery == "" || u.RawQuery == "service=git-upload-pack") {
		return nil
	}
	if u.RawQuery != "" {
		return ErrUnsafeReadTransport
	}
	switch child {
	case "", "/git-upload-pack", "/info/lfs/objects/batch":
		return nil
	}
	if oid, ok := strings.CutPrefix(child, "/info/lfs/objects/"); ok && len(oid) == 64 && strings.Trim(oid, "0123456789abcdef") == "" {
		return nil
	}
	return ErrUnsafeReadTransport
}

// ReadTransport holds non-secret discovery identity. Commands install a scoped
// helper only for their lifetime, including child Git processes for lazy fetch.
type ReadTransport struct {
	endpoint string
	repoID   string
	readURL  string
}

func NewReadTransport(endpointURL, repoID, readURL string) (*ReadTransport, error) {
	if err := ValidateReadURL(endpointURL, repoID, readURL); err != nil {
		return nil, err
	}
	return &ReadTransport{endpoint: endpointURL, repoID: repoID, readURL: readURL}, nil
}

// Command prepares a Git operation with the currently selected TAT. Callers
// must hold the checkout lock through validation and execution.
func (t *ReadTransport) Command(ctx context.Context, dir string, args ...string) (*exec.Cmd, error) {
	return t.command(ctx, dir, true, args...)
}

// LocalCommand permits local verification without credentials or network access.
// Missing promisor objects fail instead of starting an implicit lazy fetch.
func (t *ReadTransport) LocalCommand(ctx context.Context, dir string, args ...string) (*exec.Cmd, error) {
	return t.command(ctx, dir, false, args...)
}

func (t *ReadTransport) command(ctx context.Context, dir string, network bool, args ...string) (*exec.Cmd, error) {
	token := ""
	if network {
		token = os.Getenv("SAGEOX_TOKEN")
		if token == "" {
			return nil, ErrReadTokenUnavailable
		}
	}
	env := readGitEnv(dir, token)
	if err := t.validateConfig(ctx, dir, env); err != nil {
		return nil, err
	}
	helper := DefaultHelperCommand() + " --read-endpoint=" + readShellQuote(t.endpoint) + " --read-repo=" + readShellQuote(t.repoID) + " --read-url=" + readShellQuote(t.readURL)
	gitArgs := []string{
		"-c", "credential.helper=", "-c", "credential.helper=" + helper,
		"-c", "credential.useHttpPath=true", "-c", "credential.username=ox",
		"-c", "credential.interactive=false", "-c", "core.askPass=",
		"-c", "core.hooksPath=" + os.DevNull, "-c", "core.fsmonitor=false",
		"-c", "http.followRedirects=false", "-c", "http.sslVerify=true",
		"-c", "http.extraHeader=", "-c", "http.proxy=",
		"-c", "protocol.allow=never", "-c", "protocol.https.allow=always",
		"-c", "protocol.version=2", "-c", "fetch.recurseSubmodules=false",
		"-c", "submodule.recurse=false", "-c", "gc.auto=0", "-c", "maintenance.auto=false",
	}
	if !network {
		gitArgs = append(gitArgs, "-c", "protocol.https.allow=never")
		env = append(env, "GIT_NO_LAZY_FETCH=1")
	}
	cmd := exec.CommandContext(ctx, "git", append(gitArgs, args...)...)
	cmd.Dir, cmd.Env = dir, env
	configureReadProcess(cmd)
	cmd.WaitDelay = 5 * time.Second
	return cmd, nil
}

// Ignore inherited Git behavior, tracing, prompts, proxies and .netrc. The
// token stays solely in the existing SAGEOX_TOKEN environment channel.
func readGitEnv(dir, token string) []string {
	env := make([]string, 0, len(os.Environ()))
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		upper := strings.ToUpper(key)
		if strings.HasPrefix(upper, "GIT_") || upper == "HOME" || upper == "CURL_HOME" || upper == "NETRC" || upper == "SAGEOX_TOKEN" || strings.HasSuffix(upper, "_PROXY") {
			continue
		}
		env = append(env, entry)
	}
	env = append(env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_SYSTEM="+os.DevNull, "GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=", "GIT_LFS_SKIP_SMUDGE=1", "HOME="+os.DevNull,
		"GIT_CEILING_DIRECTORIES="+filepath.Dir(dir))
	if token != "" {
		env = append(env, "SAGEOX_TOKEN="+token)
	}
	return env
}

// Reject executable/config redirect surfaces instead of trying to override an
// unbounded collection of URL-specific config keys. Managed read checkouts only
// need Git's ordinary repository, sparse-checkout, branch and origin metadata.
func (t *ReadTransport) validateConfig(ctx context.Context, dir string, env []string) error {
	gitDir := filepath.Join(dir, ".git")
	info, err := os.Lstat(gitDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil // clone staging directory, before .git exists
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrUnsafeReadTransport
	}
	for _, name := range []string{"config", "config.worktree"} {
		configPath := filepath.Join(gitDir, name)
		info, err := os.Lstat(configPath)
		if errors.Is(err, os.ErrNotExist) && name == "config.worktree" {
			continue
		}
		if err != nil || !info.Mode().IsRegular() {
			return ErrUnsafeReadTransport
		}
		cmd := exec.CommandContext(ctx, "git", "config", "--file", configPath, "--no-includes", "--null", "--list")
		cmd.Env = env
		configureReadProcess(cmd)
		cmd.WaitDelay = 5 * time.Second
		out, err := cmd.Output()
		if err != nil {
			return ErrUnsafeReadTransport
		}
		for _, line := range strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00") {
			key, value, _ := strings.Cut(line, "\n")
			if !t.safeConfig(key, value) {
				return ErrUnsafeReadTransport
			}
		}
	}
	return nil
}

func (t *ReadTransport) safeConfig(key, value string) bool {
	switch key {
	case "", "core.repositoryformatversion", "core.filemode", "core.bare", "core.logallrefupdates", "core.ignorecase", "core.precomposeunicode", "core.sparsecheckout", "core.sparsecheckoutcone", "extensions.worktreeconfig", "extensions.partialclone", "remote.origin.promisor", "remote.origin.partialclonefilter", "user.name", "user.email", "commit.gpgsign", "tag.gpgsign":
		return true
	case "remote.origin.url":
		return value == t.readURL
	case "remote.origin.fetch":
		branch, destination, ok := strings.Cut(strings.TrimPrefix(value, "+refs/heads/"), ":")
		return ok && strings.HasPrefix(value, "+refs/heads/") && branch != "" && destination == "refs/remotes/origin/"+branch
	}
	if strings.HasPrefix(key, "branch.") {
		return (strings.HasSuffix(key, ".remote") && value == "origin") || (strings.HasSuffix(key, ".merge") && strings.HasPrefix(value, "refs/heads/"))
	}
	return false
}

func readShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
