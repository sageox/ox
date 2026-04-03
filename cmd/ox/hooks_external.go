package main

import (
	"fmt"

	"github.com/sageox/ox/internal/session/adapters"
)

// installExternalAdapterHooks discovers the named external adapter binary and
// delegates hook installation to it via the adapter protocol's install-hooks
// subcommand.
func installExternalAdapterHooks(adapterName string, user bool) error {
	ea, err := resolveExternalAdapter(adapterName)
	if err != nil {
		return err
	}

	scope := "project"
	if user {
		scope = "user"
	}

	repoRoot := ""
	if !user {
		repoRoot = findGitRoot()
		if repoRoot == "" {
			return fmt.Errorf("not in a git repository")
		}
	}

	result, err := ea.InstallHooks(repoRoot, scope)
	if err != nil {
		return fmt.Errorf("install-hooks failed: %w", err)
	}

	if !result.Installed {
		return fmt.Errorf("hook installation reported not installed")
	}

	return nil
}

// uninstallExternalAdapterHooks discovers the named external adapter binary and
// delegates hook removal to it via the adapter protocol's uninstall-hooks
// subcommand.
func uninstallExternalAdapterHooks(adapterName string, user bool) error {
	ea, err := resolveExternalAdapter(adapterName)
	if err != nil {
		return err
	}

	scope := "project"
	if user {
		scope = "user"
	}

	repoRoot := ""
	if !user {
		repoRoot = findGitRoot()
	}

	_, err = ea.UninstallHooks(repoRoot, scope)
	if err != nil {
		return fmt.Errorf("uninstall-hooks failed: %w", err)
	}

	return nil
}

// checkExternalAdapterHooks discovers the named external adapter binary and
// delegates hook status checking to it via the adapter protocol's check-hooks
// subcommand.
func checkExternalAdapterHooks(adapterName string, user bool) bool {
	ea, err := resolveExternalAdapter(adapterName)
	if err != nil {
		return false
	}

	scope := "project"
	if user {
		scope = "user"
	}

	repoRoot := ""
	if !user {
		repoRoot = findGitRoot()
	}

	result, err := ea.CheckHooks(repoRoot, scope)
	if err != nil {
		return false
	}

	return result.Installed
}

// listExternalAdapterHooks returns the installation status of an external
// adapter's hooks at both project and user scope.
func listExternalAdapterHooks(adapterName string) map[string]bool {
	return map[string]bool{
		"Project": checkExternalAdapterHooks(adapterName, false),
		"User":    checkExternalAdapterHooks(adapterName, true),
	}
}

// resolveExternalAdapter discovers and returns the ExternalAdapter for the
// given adapter name, triggering external adapter discovery if needed.
func resolveExternalAdapter(adapterName string) (*adapters.ExternalAdapter, error) {
	if err := adapters.RegisterExternalAdapters(); err != nil {
		return nil, fmt.Errorf("adapter discovery failed: %w", err)
	}

	adapter, err := adapters.GetAdapter(adapterName)
	if err != nil {
		return nil, fmt.Errorf("adapter %q not found: %w", adapterName, err)
	}

	ea, ok := adapter.(*adapters.ExternalAdapter)
	if !ok {
		return nil, fmt.Errorf("adapter %q is not an external adapter", adapterName)
	}

	return ea, nil
}
