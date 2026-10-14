package adapters

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"

	adapterregistry "github.com/sageox/ox/internal/adapter"
	"github.com/sageox/ox/pkg/adapterprotocol"
	"gopkg.in/yaml.v3"
)

const cursorBinary = "ox-adapter-cursor"

func TestCursorDistributionInventoriesAgree(t *testing.T) {
	t.Parallel()

	root := cursorDistributionRepoRoot(t)
	registry, err := adapterregistry.LoadEmbeddedRegistry()
	if err != nil {
		t.Fatalf("load embedded adapter registry: %v", err)
	}
	cursor := registry.Lookup("cursor")
	if cursor == nil {
		t.Fatal("Cursor adapter missing from embedded registry")
	}
	if !cursor.Bundled || cursor.Binary != cursorBinary || cursor.Repo != "sageox/ox" {
		t.Fatalf("Cursor registry distribution metadata = bundled:%t binary:%q repo:%q",
			cursor.Bundled, cursor.Binary, cursor.Repo)
	}
	wantCapabilities := []string{"hook_installer", "incremental_reader", "serve_mode", "session_reader"}
	gotCapabilities := append([]string(nil), cursor.Capabilities...)
	sort.Strings(gotCapabilities)
	if !reflect.DeepEqual(gotCapabilities, wantCapabilities) {
		t.Fatalf("Cursor registry capabilities = %v, want %v", gotCapabilities, wantCapabilities)
	}

	want := bundledRegistryBinaries(registry)
	release := readGoReleaserInventory(t, root)
	inventories := map[string][]string{
		"Makefile ADAPTERS":       readAssignment(t, filepath.Join(root, "Makefile"), "ADAPTERS :="),
		"install.sh binaries":     readQuotedAssignment(t, filepath.Join(root, "scripts", "install.sh"), "ADAPTER_BINARIES="),
		"GoReleaser builds":       release.builds,
		"Homebrew install stanza": release.brew,
	}
	for name, got := range inventories {
		t.Run(name, func(t *testing.T) {
			sort.Strings(got)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("%s = %v, want bundled registry inventory %v", name, got, want)
			}
		})
	}
}

func TestCursorDoesNotAliasGeneric(t *testing.T) {
	t.Parallel()

	for _, input := range []string{"cursor", "Cursor", "CURSOR"} {
		if got := CanonicalAdapterName(input); got != "cursor" {
			t.Errorf("CanonicalAdapterName(%q) = %q, want cursor", input, got)
		}
	}
}

func TestCursorLocalArchiveAndDisposableInstall(t *testing.T) {
	if testing.Short() {
		t.Skip("builds local release-shaped binaries")
	}

	root := cursorDistributionRepoRoot(t)
	runMake(t, root, "", "build")
	stage := filepath.Join(root, "bin")

	archivePath := filepath.Join(t.TempDir(), "ox_local_test.tar.gz")
	runCommand(t, root, "tar", "-czf", archivePath, "-C", stage, "ox", cursorBinary)
	archiveMembers := strings.Fields(string(runCommand(t, root, "tar", "-tzf", archivePath)))
	sort.Strings(archiveMembers)
	if want := []string{"ox", cursorBinary}; !reflect.DeepEqual(archiveMembers, want) {
		t.Fatalf("archive members = %v, want %v", archiveMembers, want)
	}

	archiveInstallDir := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(archiveInstallDir, 0o755); err != nil {
		t.Fatalf("create disposable archive prefix: %v", err)
	}
	runCommand(t, root, "tar", "-xzf", archivePath, "-C", archiveInstallDir)
	t.Setenv("PATH", archiveInstallDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	assertResolvesAndRuns(t, "ox", "version")
	infoOutput := assertResolvesAndRuns(t, cursorBinary, "info")
	assertCursorRuntimeInfo(t, infoOutput)

	installDir := filepath.Join(t.TempDir(), "bin")
	runMake(t, root, installDir, "install")
	unrelatedPath := filepath.Join(installDir, "coworker-owned-tool")
	if err := os.WriteFile(unrelatedPath, []byte("preserve me"), 0o755); err != nil {
		t.Fatalf("write unrelated executable: %v", err)
	}

	t.Setenv("PATH", installDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	assertResolvesAndRuns(t, "ox", "version")
	infoOutput = assertResolvesAndRuns(t, cursorBinary, "info")
	assertCursorRuntimeInfo(t, infoOutput)

	runMake(t, root, installDir, "uninstall")
	for _, name := range []string{"ox", cursorBinary} {
		if _, err := os.Stat(filepath.Join(installDir, name)); !os.IsNotExist(err) {
			t.Errorf("%s remains after make uninstall: %v", name, err)
		}
	}
	if got, err := os.ReadFile(unrelatedPath); err != nil || string(got) != "preserve me" {
		t.Fatalf("unrelated file after make uninstall = %q, %v", got, err)
	}
}

func assertCursorRuntimeInfo(t *testing.T, infoOutput []byte) {
	t.Helper()
	var info struct {
		Name         string                        `json:"name"`
		Capabilities []string                      `json:"capabilities"`
		SkillTargets []adapterprotocol.SkillTarget `json:"skill_targets"`
		ServeMode    bool                          `json:"serve_mode"`
	}
	if err := json.Unmarshal(infoOutput, &info); err != nil {
		t.Fatalf("parse Cursor info output %q: %v", infoOutput, err)
	}
	wantCapabilities := []string{"hook_installer", "incremental_reader", "serve_mode", "session_reader"}
	gotCapabilities := append([]string(nil), info.Capabilities...)
	sort.Strings(gotCapabilities)
	if info.Name != "cursor" || !reflect.DeepEqual(gotCapabilities, wantCapabilities) || !info.ServeMode {
		t.Fatalf("Cursor runtime info = name:%q capabilities:%v serve_mode:%t", info.Name, gotCapabilities, info.ServeMode)
	}
	wantTargets := []adapterprotocol.SkillTarget{{
		Key:        "agents-project",
		Root:       ".agents/skills",
		Format:     adapterprotocol.SkillFormatAgentSkillsV1,
		Scope:      adapterprotocol.SkillScopeProject,
		LinkPolicy: adapterprotocol.SkillLinkPolicyReject,
	}}
	if !reflect.DeepEqual(info.SkillTargets, wantTargets) {
		t.Fatalf("Cursor skill targets = %+v, want %+v", info.SkillTargets, wantTargets)
	}
}

type releaseInventories struct {
	builds []string
	brew   []string
}

func readGoReleaserInventory(t *testing.T, root string) releaseInventories {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, ".config", "goreleaser.yml"))
	if err != nil {
		t.Fatalf("read GoReleaser config: %v", err)
	}
	var config struct {
		Builds []struct {
			ID     string `yaml:"id"`
			Main   string `yaml:"main"`
			Binary string `yaml:"binary"`
		} `yaml:"builds"`
		Archives []struct {
			IDs []string `yaml:"ids"`
		} `yaml:"archives"`
		Brews []struct {
			Install string `yaml:"install"`
		} `yaml:"brews"`
		Release struct {
			Header string `yaml:"header"`
		} `yaml:"release"`
	}
	if err := yaml.Unmarshal(data, &config); err != nil {
		t.Fatalf("parse GoReleaser config: %v", err)
	}

	inventory := releaseInventories{}
	for _, build := range config.Builds {
		if !strings.HasPrefix(build.Binary, "ox-adapter-") {
			continue
		}
		if build.ID != build.Binary || build.Main != "./cmd/"+build.Binary {
			t.Errorf("GoReleaser build %q = id:%q main:%q", build.Binary, build.ID, build.Main)
		}
		inventory.builds = append(inventory.builds, build.Binary)
	}
	for _, archive := range config.Archives {
		if len(archive.IDs) > 0 && !containsString(archive.IDs, cursorBinary) {
			t.Errorf("GoReleaser archive ids exclude %s: %v", cursorBinary, archive.IDs)
		}
	}
	if !strings.Contains(config.Release.Header, "cursor") {
		t.Error("GoReleaser release header does not list Cursor among bundled adapters")
	}
	for _, brew := range config.Brews {
		for _, line := range strings.Split(brew.Install, "\n") {
			line = strings.TrimSpace(line)
			const prefix = `bin.install "`
			if !strings.HasPrefix(line, prefix) || !strings.HasSuffix(line, `"`) {
				continue
			}
			name := strings.TrimSuffix(strings.TrimPrefix(line, prefix), `"`)
			if strings.HasPrefix(name, "ox-adapter-") {
				inventory.brew = append(inventory.brew, name)
			}
		}
	}
	return inventory
}

func bundledRegistryBinaries(registry *adapterregistry.Registry) []string {
	var binaries []string
	for _, entry := range registry.BundledAdapters() {
		binaries = append(binaries, entry.Binary)
	}
	sort.Strings(binaries)
	return binaries
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func readAssignment(t *testing.T, path, prefix string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, prefix) {
			return strings.Fields(strings.TrimSpace(strings.TrimPrefix(line, prefix)))
		}
	}
	t.Fatalf("assignment %q missing from %s", prefix, path)
	return nil
}

func readQuotedAssignment(t *testing.T, path, prefix string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		value := strings.TrimPrefix(line, prefix)
		value = strings.Trim(value, `"`)
		return strings.Fields(value)
	}
	t.Fatalf("assignment %q missing from %s", prefix, path)
	return nil
}

func cursorDistributionRepoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
}

func runMake(t *testing.T, root, installDir, target string) {
	t.Helper()
	args := []string{target}
	if installDir != "" {
		args = append([]string{"GOBIN=" + installDir}, args...)
	}
	cmd := exec.Command("make", args...)
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("make %s: %v\n%s", target, err, out)
	}
}

func runCommand(t *testing.T, dir, name string, args ...string) []byte {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run %s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
	return out
}

func assertResolvesAndRuns(t *testing.T, name string, args ...string) []byte {
	t.Helper()
	path, err := exec.LookPath(name)
	if err != nil {
		t.Fatalf("resolve %s from disposable prefix: %v", name, err)
	}
	cmd := exec.Command(path, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run %s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
	return out
}
