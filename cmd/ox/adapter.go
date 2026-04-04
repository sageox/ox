package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	adapter "github.com/sageox/ox/internal/adapter"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/session/adapters"
	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/spf13/cobra"
)

var adapterCmd = &cobra.Command{
	Use:   "adapter",
	Short: "Manage external adapter binaries",
	Long:  `Discover, install, remove, and inspect ox adapter binaries that connect AI coworkers to ox.`,
}

func init() {
	adapterCmd.GroupID = "diagnostics"
	rootCmd.AddCommand(adapterCmd)

	// subcommands
	adapterCmd.AddCommand(adapterListCmd)
	adapterCmd.AddCommand(adapterInfoCmd)
	adapterCmd.AddCommand(adapterInstallCmd)
	adapterCmd.AddCommand(adapterRemoveCmd)
	adapterCmd.AddCommand(adapterLinkCmd)
	adapterCmd.AddCommand(adapterUnlinkCmd)
	adapterCmd.AddCommand(adapterVerifyCmd)
	adapterCmd.AddCommand(adapterReloadCmd)

	// flags
	adapterListCmd.Flags().Bool("json", false, "output in JSON format")
	adapterInfoCmd.Flags().Bool("json", false, "output in JSON format")
}

// userLocalAdaptersDir returns the platform-specific user adapter install directory.
func userLocalAdaptersDir() (string, error) {
	switch runtime.GOOS {
	case "darwin", "linux":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("cannot determine home directory: %w", err)
		}
		return filepath.Join(home, ".local", "share", "ox", "adapters"), nil
	case "windows":
		appData := os.Getenv("LOCALAPPDATA")
		if appData == "" {
			return "", fmt.Errorf("LOCALAPPDATA not set")
		}
		return filepath.Join(appData, "ox", "adapters"), nil
	default:
		return "", fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}
}

// isBundledAdapter returns true if the adapter binary lives alongside the ox binary.
func isBundledAdapter(binaryPath string) bool {
	oxExe, err := os.Executable()
	if err != nil {
		return false
	}
	// resolve symlinks for accurate comparison
	oxDir := filepath.Dir(oxExe)
	adapterDir := filepath.Dir(binaryPath)

	oxDirResolved, _ := filepath.EvalSymlinks(oxDir)
	adapterDirResolved, _ := filepath.EvalSymlinks(adapterDir)

	if oxDirResolved != "" && adapterDirResolved != "" {
		return oxDirResolved == adapterDirResolved
	}
	return oxDir == adapterDir
}

// --- ox adapter list ---

var adapterListCmd = &cobra.Command{
	Use:   "list",
	Short: "List installed and available adapters",
	RunE:  runAdapterList,
}

// adapterListItem is the combined view of an adapter for list output,
// merging discovered (installed) adapters with registry (available) entries.
type adapterListItem struct {
	Name         string   `json:"name"`
	DisplayName  string   `json:"display_name"`
	Version      string   `json:"version,omitempty"`
	Type         string   `json:"type"`
	Status       string   `json:"status"` // installed, bundled, available
	Capabilities []string `json:"capabilities"`
	BinaryPath   string   `json:"binary_path,omitempty"`
	Repo         string   `json:"repo,omitempty"`
}

func runAdapterList(cmd *cobra.Command, _ []string) error {
	jsonFlag, _ := cmd.Flags().GetBool("json")
	discovered := adapters.ListExternalAdapters()

	// build set of installed adapter names
	installedNames := make(map[string]bool, len(discovered))
	for _, d := range discovered {
		installedNames[d.Name] = true
	}

	var items []adapterListItem

	// add installed adapters first
	for _, a := range discovered {
		status := "installed"
		if isBundledAdapter(a.BinaryPath) {
			status = "bundled"
		}
		items = append(items, adapterListItem{
			Name:         a.Name,
			DisplayName:  a.DisplayName,
			Version:      a.Version,
			Type:         a.Type,
			Status:       status,
			Capabilities: a.Capabilities,
			BinaryPath:   a.BinaryPath,
		})
	}

	// add registry entries that are not installed
	reg, err := adapter.LoadEmbeddedRegistry()
	if err != nil {
		slog.Warn("failed to load adapter registry", "error", err)
	} else {
		for _, entry := range reg.Adapters {
			if installedNames[entry.Name] {
				continue
			}
			items = append(items, adapterListItem{
				Name:         entry.Name,
				DisplayName:  entry.DisplayName,
				Type:         entry.Type,
				Status:       "available",
				Capabilities: entry.Capabilities,
				Repo:         entry.Repo,
			})
		}
	}

	if jsonFlag {
		cli.PrintJSON(items)
		return nil
	}

	if len(items) == 0 {
		fmt.Println("No adapters found.")
		fmt.Println()
		cli.PrintHint("Install adapters with 'ox adapter install <name>'")
		return nil
	}

	// table header
	fmt.Printf("  %-18s %-10s %-14s %-28s %s\n",
		"NAME", "VERSION", "STATUS", "CAPABILITIES", "SOURCE")
	fmt.Printf("  %-18s %-10s %-14s %-28s %s\n",
		"────", "───────", "──────", "────────────", "──────")

	for _, item := range items {
		caps := strings.Join(item.Capabilities, ", ")
		version := item.Version
		if version == "" {
			version = "-"
		}

		source := item.BinaryPath
		if source == "" && item.Repo != "" {
			source = item.Repo
		}

		fmt.Printf("  %-18s %-10s %-14s %-28s %s\n",
			item.Name, version, item.Status, caps, source)
	}
	fmt.Println()

	return nil
}

// --- ox adapter info <name> ---

var adapterInfoCmd = &cobra.Command{
	Use:   "info <name>",
	Short: "Show detailed info for an adapter",
	Args:  cobra.ExactArgs(1),
	RunE:  runAdapterInfo,
}

func runAdapterInfo(cmd *cobra.Command, args []string) error {
	name := args[0]
	jsonFlag, _ := cmd.Flags().GetBool("json")

	// check installed adapters first
	discovered := adapters.ListExternalAdapters()
	var found *adapters.ExternalAdapterInfo
	for i := range discovered {
		if discovered[i].Name == name {
			found = &discovered[i]
			break
		}
	}

	if found != nil {
		if jsonFlag {
			cli.PrintJSON(found)
			return nil
		}

		fmt.Printf("Name:             %s\n", found.Name)
		fmt.Printf("Display Name:     %s\n", found.DisplayName)
		fmt.Printf("Version:          %s\n", found.Version)
		fmt.Printf("Type:             %s\n", found.Type)
		fmt.Printf("Protocol Version: %d\n", found.ProtocolVersion)
		fmt.Printf("Capabilities:     %s\n", strings.Join(found.Capabilities, ", "))
		fmt.Printf("Serve Mode:       %v\n", found.ServeMode)
		fmt.Printf("Binary Path:      %s\n", found.BinaryPath)
		if isBundledAdapter(found.BinaryPath) {
			fmt.Printf("Source:           bundled\n")
		}
		return nil
	}

	// fall back to registry for not-yet-installed adapters
	reg, err := adapter.LoadEmbeddedRegistry()
	if err != nil {
		return fmt.Errorf("adapter %q not found and registry unavailable: %w", name, err)
	}

	entry := reg.Lookup(name)
	if entry == nil {
		return fmt.Errorf("adapter %q not found (run 'ox adapter list' to see available adapters)", name)
	}

	if jsonFlag {
		cli.PrintJSON(entry)
		return nil
	}

	fmt.Printf("Name:             %s\n", entry.Name)
	fmt.Printf("Display Name:     %s\n", entry.DisplayName)
	fmt.Printf("Description:      %s\n", entry.Description)
	fmt.Printf("Type:             %s\n", entry.Type)
	fmt.Printf("Bundled:          %v\n", entry.Bundled)
	fmt.Printf("Binary:           %s\n", entry.Binary)
	fmt.Printf("Repo:             %s\n", entry.Repo)
	fmt.Printf("Capabilities:     %s\n", strings.Join(entry.Capabilities, ", "))
	fmt.Printf("Status:           not installed\n")
	if !entry.Bundled {
		fmt.Printf("\nInstall with:     ox adapter install %s\n", entry.Name)
	}

	return nil
}

// --- ox adapter install <name|url> ---

var adapterInstallCmd = &cobra.Command{
	Use:   "install <name|github-url>",
	Short: "Install an adapter from the registry or a GitHub repository",
	Long: `Install an adapter binary by name (from the built-in registry) or by
GitHub URL (github.com/<owner>/<repo>).

Examples:
  ox adapter install cursor          # install by name from registry
  ox adapter install github.com/sageox/ox-adapters  # install by URL

Downloads the latest release binary for the current platform and installs
it to ~/.local/share/ox/adapters/.`,
	Args: cobra.ExactArgs(1),
	RunE: runAdapterInstall,
}

func runAdapterInstall(_ *cobra.Command, args []string) error {
	source := args[0]

	// try registry lookup first (short name like "cursor")
	owner, repo, err := resolveAdapterSource(source)
	if err != nil {
		return err
	}

	installDir, err := userLocalAdaptersDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(installDir, 0755); err != nil {
		return fmt.Errorf("create adapter directory: %w", err)
	}

	platform := fmt.Sprintf("%s_%s", runtime.GOOS, runtime.GOARCH)
	slog.Info("fetching latest release", "owner", owner, "repo", repo, "platform", platform)

	// fetch latest release from GitHub API
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", owner, repo)
	resp, err := http.Get(apiURL) //nolint:gosec // URL constructed from trusted adapter registry
	if err != nil {
		return fmt.Errorf("fetch release info: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GitHub API returned %d for %s/%s (check repo exists and has releases)", resp.StatusCode, owner, repo)
	}

	var release struct {
		Assets []struct {
			Name               string `json:"name"`
			BrowserDownloadURL string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return fmt.Errorf("decode release response: %w", err)
	}

	// find matching asset for current platform
	var downloadURL, assetName string
	for _, asset := range release.Assets {
		if strings.Contains(asset.Name, platform) {
			downloadURL = asset.BrowserDownloadURL
			assetName = asset.Name
			break
		}
	}
	if downloadURL == "" {
		return fmt.Errorf("no release asset found for platform %s in %s/%s", platform, owner, repo)
	}

	slog.Info("downloading adapter", "asset", assetName)

	// download binary
	dlResp, err := http.Get(downloadURL) //nolint:gosec // URL from GitHub API release response
	if err != nil {
		return fmt.Errorf("download binary: %w", err)
	}
	defer dlResp.Body.Close()

	if dlResp.StatusCode != http.StatusOK {
		return fmt.Errorf("download returned %d", dlResp.StatusCode)
	}

	// derive binary name from asset name (strip platform suffix for the installed name)
	binaryName := deriveAdapterBinaryName(assetName, platform)
	destPath := filepath.Join(installDir, binaryName)

	// write to temp file then rename (atomic install)
	tmpFile, err := os.CreateTemp(installDir, ".ox-adapter-install-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath) // clean up on failure

	if _, err := io.Copy(tmpFile, dlResp.Body); err != nil {
		tmpFile.Close()
		return fmt.Errorf("write binary: %w", err)
	}
	tmpFile.Close()

	if err := os.Chmod(tmpPath, 0755); err != nil {
		return fmt.Errorf("set executable permission: %w", err)
	}

	// verify the binary runs info successfully
	if err := verifyAdapterBinary(tmpPath); err != nil {
		return fmt.Errorf("installed binary failed verification: %w", err)
	}

	if err := os.Rename(tmpPath, destPath); err != nil {
		return fmt.Errorf("install binary: %w", err)
	}

	cli.PrintSuccess(fmt.Sprintf("Installed %s to %s", binaryName, destPath))
	return nil
}

// resolveAdapterSource resolves an adapter source string to owner/repo.
// Accepts either a short name (looked up in the embedded registry) or a
// full github.com/<owner>/<repo> URL.
func resolveAdapterSource(source string) (owner, repo string, err error) {
	// if it looks like a GitHub URL, parse directly
	if strings.Contains(source, "/") {
		return parseGitHubRepo(source)
	}

	// short name -- look up in registry
	reg, loadErr := adapter.LoadEmbeddedRegistry()
	if loadErr != nil {
		return "", "", fmt.Errorf("registry unavailable: %w", loadErr)
	}

	entry := reg.Lookup(source)
	if entry == nil {
		return "", "", fmt.Errorf("adapter %q not found in registry (use github.com/<owner>/<repo> for unlisted adapters)", source)
	}

	if entry.Bundled {
		return "", "", fmt.Errorf("adapter %q is bundled with ox and does not need to be installed separately", source)
	}

	parts := strings.SplitN(entry.Repo, "/", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("invalid repo %q in registry for adapter %q", entry.Repo, source)
	}
	return parts[0], parts[1], nil
}

// parseGitHubRepo extracts owner/repo from a GitHub URL or shorthand.
func parseGitHubRepo(source string) (owner, repo string, err error) {
	source = strings.TrimPrefix(source, "https://")
	source = strings.TrimPrefix(source, "http://")
	source = strings.TrimSuffix(source, "/")

	if !strings.HasPrefix(source, "github.com/") {
		return "", "", fmt.Errorf("must start with github.com/")
	}

	parts := strings.SplitN(strings.TrimPrefix(source, "github.com/"), "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("expected github.com/<owner>/<repo>")
	}

	return parts[0], parts[1], nil
}

// deriveAdapterBinaryName strips platform suffix from the asset name.
// e.g. "ox-adapter-foo_darwin_arm64" -> "ox-adapter-foo"
func deriveAdapterBinaryName(assetName, platform string) string {
	name := strings.TrimSuffix(assetName, ".exe")
	name = strings.TrimSuffix(name, "_"+platform)
	// re-add .exe on Windows
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return name
}

// verifyAdapterBinary runs the info subcommand to verify a binary is a valid adapter.
func verifyAdapterBinary(binaryPath string) error {
	cmd := exec.Command(binaryPath, "info")
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("OX_PROTOCOL_VERSION=%d", adapterprotocol.ProtocolVersion),
	)
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("binary did not respond to 'info': %w", err)
	}

	var info adapterprotocol.InfoResponse
	if err := json.Unmarshal(out, &info); err != nil {
		return fmt.Errorf("invalid info response: %w", err)
	}
	if info.Name == "" {
		return fmt.Errorf("info response has empty name")
	}
	if info.ProtocolVersion < adapterprotocol.ProtocolVersion {
		return fmt.Errorf("protocol version %d is below minimum %d", info.ProtocolVersion, adapterprotocol.ProtocolVersion)
	}

	return nil
}

// --- ox adapter remove <name> ---

var adapterRemoveCmd = &cobra.Command{
	Use:   "remove <name>",
	Short: "Remove an installed adapter",
	Args:  cobra.ExactArgs(1),
	RunE:  runAdapterRemove,
}

func runAdapterRemove(_ *cobra.Command, args []string) error {
	name := args[0]

	discovered := adapters.ListExternalAdapters()
	var found *adapters.ExternalAdapterInfo
	for i := range discovered {
		if discovered[i].Name == name {
			found = &discovered[i]
			break
		}
	}

	if found == nil {
		return fmt.Errorf("adapter %q not found", name)
	}

	if isBundledAdapter(found.BinaryPath) {
		return fmt.Errorf("cannot remove bundled adapter %q (bundled adapters ship with ox)", name)
	}

	if err := os.Remove(found.BinaryPath); err != nil {
		return fmt.Errorf("remove %s: %w", found.BinaryPath, err)
	}

	cli.PrintSuccess(fmt.Sprintf("Removed adapter %q (%s)", name, found.BinaryPath))
	return nil
}

// --- ox adapter link <path> ---

var adapterLinkCmd = &cobra.Command{
	Use:   "link <path>",
	Short: "Symlink a local adapter binary for development",
	Long: `Create a symlink in the adapter directory pointing to a locally-built binary.
The binary must exist and respond to the 'info' subcommand.

This is the recommended workflow for adapter development:
  go build -o ./bin/ox-adapter-myagent ./cmd/ox-adapter-myagent
  ox adapter link ./bin/ox-adapter-myagent`,
	Args: cobra.ExactArgs(1),
	RunE: runAdapterLink,
}

func runAdapterLink(_ *cobra.Command, args []string) error {
	sourcePath, err := filepath.Abs(args[0])
	if err != nil {
		return fmt.Errorf("resolve path: %w", err)
	}

	// verify source binary exists
	fi, err := os.Stat(sourcePath)
	if err != nil {
		return fmt.Errorf("binary not found at %s: %w", sourcePath, err)
	}
	if fi.IsDir() {
		return fmt.Errorf("%s is a directory, not a binary", sourcePath)
	}

	// derive link name from binary filename (check prefix before running binary)
	binaryName := filepath.Base(sourcePath)
	if !strings.HasPrefix(binaryName, "ox-adapter-") {
		return fmt.Errorf("binary name must start with 'ox-adapter-' (got %q)", binaryName)
	}

	// verify it is a valid adapter
	if err := verifyAdapterBinary(sourcePath); err != nil {
		return fmt.Errorf("binary validation failed: %w", err)
	}

	installDir, err := userLocalAdaptersDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(installDir, 0755); err != nil {
		return fmt.Errorf("create adapter directory: %w", err)
	}

	linkPath := filepath.Join(installDir, binaryName)

	// remove existing symlink if present
	if existing, err := os.Lstat(linkPath); err == nil {
		if existing.Mode()&os.ModeSymlink != 0 {
			os.Remove(linkPath)
		} else {
			return fmt.Errorf("%s already exists and is not a symlink (use 'ox adapter remove' first)", linkPath)
		}
	}

	if err := os.Symlink(sourcePath, linkPath); err != nil {
		return fmt.Errorf("create symlink: %w", err)
	}

	cli.PrintSuccess(fmt.Sprintf("Linked: %s -> %s", linkPath, sourcePath))
	return nil
}

// --- ox adapter unlink <name> ---

var adapterUnlinkCmd = &cobra.Command{
	Use:   "unlink <name>",
	Short: "Remove a symlinked adapter",
	Long:  `Remove a symlink from the adapter directory. Only removes symlinks, not real binaries.`,
	Args:  cobra.ExactArgs(1),
	RunE:  runAdapterUnlink,
}

func runAdapterUnlink(_ *cobra.Command, args []string) error {
	name := args[0]

	installDir, err := userLocalAdaptersDir()
	if err != nil {
		return err
	}

	binaryName := "ox-adapter-" + name
	if runtime.GOOS == "windows" {
		binaryName += ".exe"
	}
	linkPath := filepath.Join(installDir, binaryName)

	fi, err := os.Lstat(linkPath)
	if err != nil {
		return fmt.Errorf("adapter %q not found at %s", name, linkPath)
	}

	if fi.Mode()&os.ModeSymlink == 0 {
		return fmt.Errorf("%s is not a symlink (use 'ox adapter remove' to remove installed binaries)", linkPath)
	}

	if err := os.Remove(linkPath); err != nil {
		return fmt.Errorf("remove symlink: %w", err)
	}

	cli.PrintSuccess(fmt.Sprintf("Unlinked adapter %q (%s)", name, linkPath))
	return nil
}

// --- ox adapter verify <name> ---

var adapterVerifyCmd = &cobra.Command{
	Use:   "verify <name>",
	Short: "Run compliance tests against an adapter",
	Long: `Run the adapter protocol compliance test suite against a named adapter.
Verifies the adapter correctly implements info, detect, and serve-mode commands.`,
	Args: cobra.ExactArgs(1),
	RunE: runAdapterVerify,
}

func runAdapterVerify(_ *cobra.Command, args []string) error {
	name := args[0]

	discovered := adapters.ListExternalAdapters()
	var found *adapters.ExternalAdapterInfo
	for i := range discovered {
		if discovered[i].Name == name {
			found = &discovered[i]
			break
		}
	}

	if found == nil {
		return fmt.Errorf("adapter %q not found (run 'ox adapter list' to see discovered adapters)", name)
	}

	fmt.Printf("Running compliance suite against %s (%s)...\n\n", found.Name, found.BinaryPath)

	// run compliance via go test with the adapter binary set as an env var
	projectRoot, err := findProjectRoot()
	if err != nil {
		return fmt.Errorf("find project root: %w", err)
	}

	cmd := exec.Command("go", "test",
		"./internal/adapterprotocol/compliance/",
		"-tags", "compliance",
		"-v",
		"-count=1",
	)
	cmd.Dir = projectRoot
	cmd.Env = append(os.Environ(),
		"OX_ADAPTER_BINARY="+found.BinaryPath,
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("compliance suite failed: %w", err)
	}

	cli.PrintSuccess(fmt.Sprintf("Adapter %q passed all compliance tests", name))
	return nil
}

// --- ox adapter reload ---

var adapterReloadCmd = &cobra.Command{
	Use:   "reload",
	Short: "Signal daemon to re-scan adapter directories",
	RunE:  runAdapterReload,
}

func runAdapterReload(_ *cobra.Command, _ []string) error {
	// the daemon re-discovers adapters on each hook call, so no IPC needed yet
	cli.PrintInfo("The daemon will re-discover adapters on the next hook invocation.")
	cli.PrintHint("Adapters are scanned from: OX_ADAPTER_PATH, ox binary directory, ~/.local/share/ox/adapters/")
	return nil
}
