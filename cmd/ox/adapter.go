package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
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
	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/session/adapters"
	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/spf13/cobra"
)

// adapterDownloadHosts are the GitHub release-asset hosts permitted as a
// defense-in-depth transport guard (ADR-022 decision 4). This is NOT the primary
// integrity control — the checksum gate is. GitHub rotates CDN hosts, so this
// list errs toward the known asset hosts and never substitutes for the checksum.
var adapterDownloadHosts = map[string]bool{
	"github.com":                           true,
	"objects.githubusercontent.com":        true,
	"release-assets.githubusercontent.com": true,
}

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
	adapterInstallCmd.Flags().Bool("allow-unverified", false,
		"install without a SageOx-curated checksum (required for arbitrary repos and for curated entries that lack a pinned checksum)")
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

// installPlan is the resolved intent for an adapter install: where to fetch it,
// which release to pin, and whether SageOx vouches for the bytes via a checksum.
type installPlan struct {
	owner     string
	repo      string
	tag       string            // pinned release tag (required; never releases/latest)
	checksum  string            // expected sha256 hex for this platform ("" => unverifiable)
	curated   bool              // true => resolved from registry (SageOx is trust anchor)
	platform  string            // e.g. "darwin_arm64"
	checksums map[string]string // full per-platform map (curated only; for diagnostics)
}

// installConfig parameterizes installAdapter so it can be driven by tests against
// an httptest server. Production fills apiBaseURL with the GitHub API root and
// allowedHosts with adapterDownloadHosts.
type installConfig struct {
	plan         installPlan
	apiBaseURL   string          // GitHub API root, e.g. https://api.github.com
	allowedHosts map[string]bool // download-host transport guard (defense-in-depth)
	installDir   string
	// verify is the protocol-conformance check run AFTER the checksum gate. It is
	// indirected so tests can assert it is never reached when the gate fails.
	verify func(binaryPath string) error
	// httpClient performs the API + asset GETs. nil => http.DefaultClient. Tests
	// inject a TLS test server's client so the https transport guard still applies.
	httpClient *http.Client
}

// runAdapterInstall acquires and installs an adapter binary.
//
// SECURITY POSTURE (see docs/adr/ADR-022-adapter-security-posture.md):
// Executing adapter code is the intended extension mechanism, not a vulnerability.
// What we harden is the *moment of acquisition*, and only where SageOx is the
// trust anchor. Two paths with different anchors:
//   - curated short-name ("cursor"): SageOx vouches -> integrity check required
//     (pin tag + sha256, verify-before-exec). Tracked in ox-5ihl.
//   - arbitrary github.com/<owner>/<repo>@<tag>: the user vouches -> stays
//     frictionless, but is unverifiable by SageOx and so requires an explicit
//     --allow-unverified opt-in (and an explicit @<tag> pin in the source).
//
// Install ordering is an invariant (ADR-022 decision 5): resolve -> pin tag ->
// GET releases/tags/<tag> (asserting tag_name matches) -> select asset ->
// validate download host -> download while hashing -> constant-time checksum
// compare -> ONLY THEN chmod -> verifyAdapterProtocol -> atomic rename. No
// untrusted byte is made executable or run before the checksum gate.
//
// Once installed, an adapter runs every session as the user; installation IS the
// trust decision. We do not sandbox an installed adapter from the user's own
// session — that is outside the documented threat model (security/SECURITY.md).
func runAdapterInstall(cmd *cobra.Command, args []string) error {
	source := args[0]
	allowUnverified, _ := cmd.Flags().GetBool("allow-unverified")

	platform := fmt.Sprintf("%s_%s", runtime.GOOS, runtime.GOARCH)
	plan, err := resolveAdapterSource(source, platform)
	if err != nil {
		return err
	}

	// A curated entry with no pinned tag has no release to fetch at all (we refuse
	// to fall back to releases/latest — that is the gap ox-5ihl closes), so even
	// --allow-unverified cannot satisfy it. NOTE (ox-5ihl): the external entries
	// (cursor, windsurf, copilot, cline) currently ship WITHOUT a tag/checksum, so
	// they land here until a maintainer pins one — the intended, documented
	// transition.
	if plan.curated && plan.tag == "" {
		return fmt.Errorf("adapter %q has no pinned release tag in the registry yet; "+
			"a maintainer must pin a tag + per-platform checksum before it can be installed (ADR-022/ox-5ihl)",
			plan.repo)
	}

	// Fail-closed gate (ADR-022 decisions 2-3): an install SageOx cannot vouch for
	// — an arbitrary repo, or a curated entry whose registry lacks a checksum for
	// this platform — must not silently chmod+exec untrusted bytes. Require an
	// explicit opt-in.
	if plan.checksum == "" && !allowUnverified {
		if plan.curated {
			return fmt.Errorf("adapter %q has no pinned checksum for platform %s yet; "+
				"either wait for a checksum to be pinned in the registry, or re-run with --allow-unverified to install unverified",
				plan.repo, platform)
		}
		return fmt.Errorf("installing from an arbitrary repo (%s/%s) cannot be verified by SageOx; "+
			"re-run with --allow-unverified to install unverified",
			plan.owner, plan.repo)
	}

	installDir, err := userLocalAdaptersDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(installDir, 0755); err != nil {
		return fmt.Errorf("create adapter directory: %w", err)
	}

	if plan.checksum == "" {
		// allow-unverified path: keep protocol conformance, drop the provenance gate.
		cli.PrintWarning(fmt.Sprintf(
			"installing %s/%s@%s WITHOUT integrity verification (--allow-unverified): "+
				"SageOx cannot vouch for these bytes; you are the trust anchor",
			plan.owner, plan.repo, plan.tag))
	}

	cfg := installConfig{
		plan:         plan,
		apiBaseURL:   "https://api.github.com",
		allowedHosts: adapterDownloadHosts,
		installDir:   installDir,
		verify:       verifyAdapterProtocol,
	}
	return installAdapter(cfg)
}

// installAdapter performs the integrity-gated acquisition described on
// runAdapterInstall. It is split out so tests can drive every step (tag
// assertion, host guard, checksum gate, verify ordering) against an httptest
// server. The ordering here is a security invariant — do not reorder.
func installAdapter(cfg installConfig) error {
	plan := cfg.plan
	if plan.tag == "" {
		// defense in depth: resolveAdapterSource must always pin a tag.
		return fmt.Errorf("internal: install plan has no pinned tag")
	}

	client := cfg.httpClient
	if client == nil {
		client = http.DefaultClient
	}

	slog.Info("fetching pinned release", "owner", plan.owner, "repo", plan.repo, "tag", plan.tag, "platform", plan.platform)

	// Fetch the PINNED release (releases/tags/<tag>), never releases/latest, so a
	// retagged/swapped "latest" cannot redirect a pinned install (ADR-022).
	apiURL := fmt.Sprintf("%s/repos/%s/%s/releases/tags/%s", cfg.apiBaseURL, plan.owner, plan.repo, plan.tag)
	resp, err := client.Get(apiURL) //nolint:gosec // fixed api.github.com endpoint; owner/repo/tag from registry (curated) or validated user input (arbitrary-repo path)
	if err != nil {
		return fmt.Errorf("fetch release info: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GitHub API returned %d for %s/%s tag %s (check the tag exists)", resp.StatusCode, plan.owner, plan.repo, plan.tag)
	}

	var release struct {
		TagName string `json:"tag_name"`
		Assets  []struct {
			Name               string `json:"name"`
			BrowserDownloadURL string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return fmt.Errorf("decode release response: %w", err)
	}

	// Assert the response describes the tag we asked for; a mismatch means the API
	// returned a different release than the pin and must be rejected.
	if release.TagName != plan.tag {
		return fmt.Errorf("release tag mismatch: requested %q but GitHub returned %q", plan.tag, release.TagName)
	}

	// find matching asset for current platform
	var downloadURL, assetName string
	for _, asset := range release.Assets {
		if strings.Contains(asset.Name, plan.platform) {
			downloadURL = asset.BrowserDownloadURL
			assetName = asset.Name
			break
		}
	}
	if downloadURL == "" {
		return fmt.Errorf("no release asset found for platform %s in %s/%s@%s", plan.platform, plan.owner, plan.repo, plan.tag)
	}

	// Transport guard (ADR-022 decision 4): defense-in-depth only — the checksum
	// gate below is the real control. Reject a download URL pointing off the known
	// asset hosts (e.g. an attacker-influenced browser_download_url).
	if err := gitutil.ValidateHTTPSHost(downloadURL, cfg.allowedHosts); err != nil {
		return fmt.Errorf("refusing download: %w", err)
	}

	slog.Info("downloading adapter", "asset", assetName, "tag", plan.tag)

	dlResp, err := client.Get(downloadURL) //nolint:gosec // host validated above; bytes checksum-gated below
	if err != nil {
		return fmt.Errorf("download binary: %w", err)
	}
	defer dlResp.Body.Close()

	if dlResp.StatusCode != http.StatusOK {
		return fmt.Errorf("download returned %d", dlResp.StatusCode)
	}

	binaryName := deriveAdapterBinaryName(assetName, plan.platform)
	destPath := filepath.Join(cfg.installDir, binaryName)

	// write to temp file then rename (atomic install)
	tmpFile, err := os.CreateTemp(cfg.installDir, ".ox-adapter-install-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath) // clean up on failure

	// Hash WHILE copying so the bytes we write are exactly the bytes we hash.
	hasher := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmpFile, hasher), dlResp.Body); err != nil {
		tmpFile.Close()
		return fmt.Errorf("write binary: %w", err)
	}
	tmpFile.Close()

	// CHECKSUM GATE (ADR-022 invariant): the file is still 0600 and never executed.
	// On the verified path, a mismatch aborts BEFORE chmod/exec. Constant-time
	// compare avoids leaking where the digests diverge.
	if plan.checksum != "" {
		got := hex.EncodeToString(hasher.Sum(nil))
		want := strings.ToLower(plan.checksum)
		if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
			return fmt.Errorf("checksum mismatch for %s: expected %s, got %s (refusing to install)", assetName, want, got)
		}
		slog.Info("adapter checksum verified", "asset", assetName, "sha256", got)
	}

	// Only past the gate do we make the bytes executable.
	if err := os.Chmod(tmpPath, 0755); err != nil {
		return fmt.Errorf("set executable permission: %w", err)
	}

	// Protocol conformance (NOT provenance — provenance was the checksum gate).
	if err := cfg.verify(tmpPath); err != nil {
		return fmt.Errorf("installed binary failed verification: %w", err)
	}

	if err := os.Rename(tmpPath, destPath); err != nil {
		return fmt.Errorf("install binary: %w", err)
	}

	cli.PrintSuccess(fmt.Sprintf("Installed %s to %s", binaryName, destPath))
	return nil
}

// resolveAdapterSource resolves an adapter source string to an installPlan.
// Accepts either a short name (looked up in the embedded registry) or a full
// github.com/<owner>/<repo>[@<tag>] URL.
//
// This split is the adapter trust boundary (ADR-022): a short name means
// "install what SageOx curated under this name" (SageOx is the trust anchor, so
// the curated path carries a registry tag pin + per-platform checksum), while a
// github.com/<owner>/<repo>@<tag> URL means "install this repo I explicitly
// named at this tag" (the user is the trust anchor; SageOx has no checksum, so
// the caller must pass --allow-unverified). Keep the two paths distinguishable —
// do not collapse them into one install flow with one trust policy.
func resolveAdapterSource(source, platform string) (installPlan, error) {
	// if it looks like a GitHub URL, parse directly (user-as-trust-anchor path)
	if strings.Contains(source, "/") {
		owner, repo, tag, err := parseGitHubRepo(source)
		if err != nil {
			return installPlan{}, err
		}
		// The arbitrary path requires an explicit @<tag>: without a pin there is
		// nothing reproducible to install and nothing for the user to vouch for.
		if tag == "" {
			return installPlan{}, fmt.Errorf("installing from an arbitrary repo requires an explicit tag: use github.com/%s/%s@<tag>", owner, repo)
		}
		return installPlan{owner: owner, repo: repo, tag: tag, curated: false, platform: platform}, nil
	}

	// short name -- look up in registry (curated, SageOx-as-trust-anchor path)
	reg, loadErr := adapter.LoadEmbeddedRegistry()
	if loadErr != nil {
		return installPlan{}, fmt.Errorf("registry unavailable: %w", loadErr)
	}

	entry := reg.Lookup(source)
	if entry == nil {
		return installPlan{}, fmt.Errorf("adapter %q not found in registry (use github.com/<owner>/<repo>@<tag> for unlisted adapters)", source)
	}

	if entry.Bundled {
		return installPlan{}, fmt.Errorf("adapter %q is bundled with ox and does not need to be installed separately", source)
	}

	parts := strings.SplitN(entry.Repo, "/", 2)
	if len(parts) != 2 {
		return installPlan{}, fmt.Errorf("invalid repo %q in registry for adapter %q", entry.Repo, source)
	}

	plan := installPlan{
		owner:     parts[0],
		repo:      parts[1],
		tag:       entry.Tag,
		curated:   true,
		platform:  platform,
		checksums: entry.Checksums,
		checksum:  entry.Checksums[platform], // "" when unpinned -> fail-closed upstream
	}
	// A curated entry with no tag pin cannot fetch releases/tags/<tag>; treat it as
	// unverifiable (fail-closed unless --allow-unverified). We cannot fall back to
	// releases/latest — that is precisely the gap ADR-022/ox-5ihl closes. When the
	// tag is missing we leave plan.tag empty AND blank the checksum so the caller's
	// fail-closed branch fires with a clear message rather than a tag assertion.
	if plan.tag == "" {
		plan.checksum = ""
	}
	return plan, nil
}

// parseGitHubRepo extracts owner, repo, and an optional @<tag> from a GitHub URL
// or shorthand: github.com/<owner>/<repo>[@<tag>]. tag is "" when absent; the
// caller decides whether a missing tag is acceptable (the arbitrary path requires
// one — ADR-022).
func parseGitHubRepo(source string) (owner, repo, tag string, err error) {
	source = strings.TrimPrefix(source, "https://")
	source = strings.TrimPrefix(source, "http://")
	source = strings.TrimSuffix(source, "/")

	if !strings.HasPrefix(source, "github.com/") {
		return "", "", "", fmt.Errorf("must start with github.com/")
	}

	rest := strings.TrimPrefix(source, "github.com/")
	// split off an optional @<tag> suffix (on the repo segment)
	if at := strings.LastIndex(rest, "@"); at != -1 {
		tag = rest[at+1:]
		rest = rest[:at]
	}

	parts := strings.SplitN(rest, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", "", fmt.Errorf("expected github.com/<owner>/<repo>[@<tag>]")
	}

	return parts[0], parts[1], tag, nil
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

// verifyAdapterProtocol runs the info subcommand to verify a binary is a valid adapter.
//
// IMPORTANT: this is PROTOCOL-CONFORMANCE verification, NOT provenance. It answers
// "is this a runnable ox adapter?", never "is this the binary SageOx curated?".
// The old name (verifyAdapterBinary) misled reviewers into reading it as an
// integrity check — it is not, and was never intended to be (ADR-022 decision 5).
// Provenance is the checksum gate in installAdapter (ox-5ihl), which runs BEFORE
// this is ever reached on the verified path.
func verifyAdapterProtocol(binaryPath string) error {
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

	// verify it is a valid adapter (protocol conformance; dev link, not acquisition)
	if err := verifyAdapterProtocol(sourcePath); err != nil {
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
