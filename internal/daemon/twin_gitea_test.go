//go:build slow

package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/go-connections/nat"
	"github.com/sageox/ox/internal/lfs"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcexec "github.com/testcontainers/testcontainers-go/exec"
	"github.com/testcontainers/testcontainers-go/wait"
)

// Digital twin: a disposable Gitea container that emulates the real git+LFS
// remote. One container is lazily started on first use and shared across all
// twin tests. Each test creates its own repo for isolation — they share the
// server, not data.
//
// Lazy init (sync.Once) ensures non-twin slow tests (e.g., whisper) don't pay
// the container startup cost.
var (
	sharedGitea  *giteaFixture
	giteaOnce    sync.Once
	giteaInitErr error
)

// giteaHostPort is fixed so ROOT_URL matches the external access URL.
// Gitea LFS batch responses embed ROOT_URL in action hrefs; a random
// mapped port would cause LFS uploads to hit the wrong address.
//
// WARNING: Parallel test runs on the same machine will fail if this port
// is already in use. Set OX_SKIP_DOCKER=1 to skip these tests.
const giteaHostPort = "13719"

func dockerAvailable() bool {
	_, err := exec.LookPath("docker")
	return err == nil
}

func requireDocker(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("short: skipping Docker-backed tests")
	}
	if os.Getenv("OX_SKIP_DOCKER") != "" {
		t.Skip("short: OX_SKIP_DOCKER set")
	}
	if !dockerAvailable() {
		t.Skip("short: docker not in PATH")
	}
}

// getSharedGitea returns the Gitea digital twin, starting it on first call.
// Skips the test if Docker is unavailable or container startup fails.
func getSharedGitea(t *testing.T) *giteaFixture {
	t.Helper()
	requireDocker(t)

	giteaOnce.Do(func() {
		// fast-fail if port is already in use
		ln, err := net.Listen("tcp", "127.0.0.1:"+giteaHostPort)
		if err != nil {
			giteaInitErr = fmt.Errorf("port %s already in use (parallel test run?): %w", giteaHostPort, err)
			return
		}
		ln.Close()

		g, err := createGiteaFixture()
		if err != nil {
			giteaInitErr = fmt.Errorf("start Gitea digital twin: %w", err)
			return
		}
		sharedGitea = g
	})

	if giteaInitErr != nil {
		t.Skipf("Gitea digital twin not available: %v", giteaInitErr)
	}
	if sharedGitea == nil {
		t.Skip("Gitea digital twin not available")
	}

	// cleanup is handled automatically by testcontainers' Ryuk sidecar
	// when the test process exits — no t.Cleanup needed for the shared container.

	return sharedGitea
}

type giteaFixture struct {
	container  testcontainers.Container
	httpURL    string
	adminUser  string
	adminPass  string
	adminToken string
}

func createGiteaFixture() (*giteaFixture, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	httpURL := "http://localhost:" + giteaHostPort

	req := testcontainers.ContainerRequest{
		Image:        "gitea/gitea:1.22",
		ExposedPorts: []string{"3000/tcp"},
		Env: map[string]string{
			"GITEA__server__ROOT_URL":         httpURL + "/",
			"GITEA__server__LFS_START_SERVER": "true",
			"GITEA__server__OFFLINE_MODE":     "true",
			"INSTALL_LOCK":                    "true",
			"GITEA__security__INSTALL_LOCK":   "true",
		},
		HostConfigModifier: func(hc *container.HostConfig) {
			hc.PortBindings = nat.PortMap{
				"3000/tcp": []nat.PortBinding{{HostIP: "0.0.0.0", HostPort: giteaHostPort}},
			}
		},
		WaitingFor: wait.ForHTTP("/api/v1/version").
			WithPort("3000/tcp").
			WithStartupTimeout(60 * time.Second),
	}

	ctr, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		return nil, fmt.Errorf("start container: %w", err)
	}

	// create admin user (exec as git user to avoid root check)
	exitCode, reader, err := ctr.Exec(ctx, []string{
		"gitea", "admin", "user", "create",
		"--admin",
		"--username", "testadmin",
		"--password", "testpass123",
		"--email", "admin@test.local",
		"--must-change-password=false",
	}, tcexec.WithUser("git"))
	if err != nil {
		_ = ctr.Terminate(context.Background())
		return nil, fmt.Errorf("exec admin user create: %w", err)
	}
	out, _ := io.ReadAll(reader)
	if exitCode != 0 {
		_ = ctr.Terminate(context.Background())
		return nil, fmt.Errorf("gitea admin user create failed (exit %d): %s", exitCode, string(out))
	}

	// create API token via HTTP
	var token string
	var lastTokenErr string
	for attempt := 0; attempt < 10; attempt++ {
		body, _ := json.Marshal(map[string]interface{}{
			"name":   fmt.Sprintf("test-token-%d", attempt),
			"scopes": []string{"all"},
		})
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
			httpURL+"/api/v1/users/testadmin/tokens", bytes.NewReader(body))
		if err != nil {
			lastTokenErr = fmt.Sprintf("create request: %v", err)
			continue
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.SetBasicAuth("testadmin", "testpass123")

		resp, err := http.DefaultClient.Do(httpReq)
		if err != nil {
			lastTokenErr = fmt.Sprintf("http do: %v", err)
			time.Sleep(1 * time.Second)
			continue
		}

		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode != http.StatusCreated {
			lastTokenErr = fmt.Sprintf("HTTP %d: %s", resp.StatusCode, string(respBody[:min(len(respBody), 200)]))
			time.Sleep(1 * time.Second)
			continue
		}

		var result map[string]interface{}
		if err := json.Unmarshal(respBody, &result); err != nil {
			lastTokenErr = fmt.Sprintf("decode: %v", err)
			continue
		}
		token, _ = result["sha1"].(string)
		if token == "" {
			// Gitea 1.22+ may use "token" field instead of "sha1"
			token, _ = result["token"].(string)
		}
		break
	}
	if token == "" {
		_ = ctr.Terminate(context.Background())
		return nil, fmt.Errorf("failed to create admin API token: %s", lastTokenErr)
	}

	return &giteaFixture{
		container:  ctr,
		httpURL:    httpURL,
		adminUser:  "testadmin",
		adminPass:  "testpass123",
		adminToken: token,
	}, nil
}

func (g *giteaFixture) createRepo(t *testing.T, name string) string {
	t.Helper()

	body, _ := json.Marshal(map[string]interface{}{
		"name":           name,
		"auto_init":      true,
		"default_branch": "main",
	})

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		g.httpURL+"/api/v1/user/repos", bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "token "+g.adminToken)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	var result map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))

	return fmt.Sprintf("http://%s:%s@localhost:%s/%s/%s.git",
		g.adminUser, g.adminPass, giteaHostPort, g.adminUser, name)
}

func (g *giteaFixture) cloneRepo(t *testing.T, cloneURL, destDir string) {
	t.Helper()

	cmd := exec.Command("git", "clone", cloneURL, destDir)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git clone failed: %s", string(out))

	gitConfig(t, destDir)
}

func (g *giteaFixture) pushFromTempClone(t *testing.T, cloneURL, filename, content string) {
	t.Helper()

	tmp := t.TempDir()
	cloneDir := filepath.Join(tmp, "push-clone")

	cmd := exec.Command("git", "clone", cloneURL, cloneDir)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git clone failed: %s", string(out))

	gitConfig(t, cloneDir)

	filePath := filepath.Join(cloneDir, filename)
	require.NoError(t, os.MkdirAll(filepath.Dir(filePath), 0o755))
	require.NoError(t, os.WriteFile(filePath, []byte(content), 0o644))

	cmd = exec.Command("git", "-C", cloneDir, "add", filename)
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "git add failed: %s", string(out))

	cmd = exec.Command("git", "-C", cloneDir, "commit", "-m", "add "+filename)
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "git commit failed: %s", string(out))

	cmd = exec.Command("git", "-C", cloneDir, "push")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "git push failed: %s", string(out))
}

// createPrivateRepo creates a private Gitea repo (requires auth for all operations).
func (g *giteaFixture) createPrivateRepo(t *testing.T, name string) string {
	t.Helper()

	body, _ := json.Marshal(map[string]interface{}{
		"name":           name,
		"auto_init":      true,
		"default_branch": "main",
		"private":        true,
	})

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		g.httpURL+"/api/v1/user/repos", bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "token "+g.adminToken)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	return fmt.Sprintf("http://%s:%s@localhost:%s/%s/%s.git",
		g.adminUser, g.adminPass, giteaHostPort, g.adminUser, name)
}

func (g *giteaFixture) lfsClient(t *testing.T, repoName string) *lfs.Client {
	t.Helper()
	repoURL := fmt.Sprintf("http://localhost:%s/%s/%s.git", giteaHostPort, g.adminUser, repoName)
	return lfs.NewClient(repoURL, g.adminUser, g.adminToken)
}
