package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sageox/ox/internal/docs"
	"github.com/sageox/ox/internal/version"
	"github.com/spf13/cobra"
)

var docsCmd = &cobra.Command{
	Use:    "docs",
	Short:  "Generate CLI reference documentation",
	Hidden: true,
	RunE:   runDocsGenerate,
}

func runDocsGenerate(cmd *cobra.Command, args []string) error {
	output, _ := cmd.Flags().GetString("output")

	fmt.Printf("Generating documentation to %s...\n", output)
	prepareDocsCommandTree(rootCmd)

	// Create temp directory for raw Cobra output
	tmpDir, err := os.MkdirTemp("", "ox-docs-*")
	if err != nil {
		return fmt.Errorf("failed to create temp directory: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	// disable auto-gen tag
	rootCmd.DisableAutoGenTag = true

	// Generate raw markdown to temp directory
	if err := docs.Generate(rootCmd, tmpDir); err != nil {
		return fmt.Errorf("failed to generate docs: %w", err)
	}

	// Clean output directory (but preserve .gitkeep)
	gitkeepPath := filepath.Join(output, ".gitkeep")
	hasGitkeep := false
	if _, err := os.Stat(gitkeepPath); err == nil {
		hasGitkeep = true
	}

	// Remove existing MDX files
	entries, _ := os.ReadDir(output)
	for _, entry := range entries {
		if entry.Name() != ".gitkeep" {
			os.RemoveAll(filepath.Join(output, entry.Name()))
		}
	}

	// Post-process to hierarchical MDX structure
	if err := docs.PostProcess(tmpDir, output); err != nil {
		return fmt.Errorf("failed to post-process docs: %w", err)
	}

	// Restore .gitkeep if it existed
	if hasGitkeep {
		if err := os.WriteFile(gitkeepPath, []byte{}, 0644); err != nil {
			return fmt.Errorf("failed to restore .gitkeep: %w", err)
		}
	}

	// Sync package.json version with CLI version
	docsDir := filepath.Dir(output)
	if err := syncPackageVersion(docsDir); err != nil {
		return fmt.Errorf("failed to sync package.json version: %w", err)
	}

	fmt.Println("Documentation generated successfully")
	return nil
}

// prepareDocsCommandTree makes documentation generation independent of local
// feature flags and cached server settings. Reference docs describe the public,
// released CLI surface; experimental commands stay out until their gates are
// removed from the command registration itself.
func prepareDocsCommandTree(root *cobra.Command) {
	root.CompletionOptions.DisableDefaultCmd = true
	for _, child := range root.Commands() {
		switch child.Name() {
		case "attest", "scout", "memory", "completion":
			root.RemoveCommand(child)
		case "carts", "cart-analyze":
			child.Hidden = true
		}
	}
}

// syncPackageVersion updates docs/package.json version to match CLI version
func syncPackageVersion(docsDir string) error {
	pkgPath := filepath.Join(docsDir, "package.json")

	data, err := os.ReadFile(pkgPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // no package.json, nothing to sync
		}
		return err
	}

	var pkg map[string]any
	if err := json.Unmarshal(data, &pkg); err != nil {
		return err
	}

	if pkg["version"] == version.Version {
		return nil // already in sync
	}

	pkg["version"] = version.Version

	updated, err := json.MarshalIndent(pkg, "", "  ")
	if err != nil {
		return err
	}

	// add trailing newline
	updated = append(updated, '\n')

	fmt.Printf("Synced package.json version to %s\n", version.Version)
	return os.WriteFile(pkgPath, updated, 0644)
}

func init() {
	docsCmd.Flags().String("output", "docs/reference", "output directory for generated docs (default: docs/reference)")
	rootCmd.AddCommand(docsCmd)
}
