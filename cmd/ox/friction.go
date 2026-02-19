package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/daemon"
	"github.com/sageox/ox/internal/uxfriction"
	"github.com/sageox/ox/internal/uxfriction/adapters"
	"github.com/spf13/cobra"
)

//go:embed default_catalog.json
var defaultCatalogJSON []byte

var (
	frictionHandler *uxfriction.Handler
	frictionCatalog *uxfriction.FrictionCatalog
)

// initFriction initializes the friction handling system.
// Should be called after rootCmd is fully initialized.
//
// The catalog is loaded eagerly at startup for simplicity. The catalog is small
// (typically <10KB) and loading is fast (<1ms). If catalog becomes large,
// consider lazy loading on first error.
//
// Loading order:
// 1. Embedded default catalog (bundled with CLI release)
// 2. Cache file (user's cached catalog from server sync)
//
// The cache file can contain updated patterns from the server, but the embedded
// default provides a baseline for offline/first-run scenarios.
func initFriction(rootCmd *cobra.Command) {
	// create catalog
	frictionCatalog = uxfriction.NewFrictionCatalog()

	// load embedded default catalog first (baseline patterns)
	if len(defaultCatalogJSON) > 0 {
		var defaultData uxfriction.CatalogData
		if err := json.Unmarshal(defaultCatalogJSON, &defaultData); err == nil {
			_ = frictionCatalog.Update(defaultData)
		}
	}

	// overlay with cache if available (may contain server updates)
	if data, err := loadCatalogCache(); err == nil && data != nil {
		_ = frictionCatalog.Update(*data)
	}

	// create Cobra adapter
	adapter := adapters.NewCobraAdapter(rootCmd)

	// create handler
	frictionHandler = uxfriction.NewHandler(adapter, frictionCatalog)
}

// getCatalogCachePath returns the path to the friction catalog cache file.
// Uses os.UserCacheDir() for cross-platform support.
func getCatalogCachePath() string {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		// fallback to home directory
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		cacheDir = filepath.Join(homeDir, ".cache")
	}
	return filepath.Join(cacheDir, "sageox", "friction-catalog.json")
}

// loadCatalogCache reads and unmarshals the friction catalog from the cache file.
// Returns nil, err if the file doesn't exist or is invalid.
func loadCatalogCache() (*uxfriction.CatalogData, error) {
	cachePath := getCatalogCachePath()
	if cachePath == "" {
		return nil, fmt.Errorf("could not determine cache path")
	}

	data, err := os.ReadFile(cachePath)
	if err != nil {
		return nil, err
	}

	var catalogData uxfriction.CatalogData
	if err := json.Unmarshal(data, &catalogData); err != nil {
		return nil, err
	}

	return &catalogData, nil
}

// Note: handleFriction was removed in favor of executeWithFrictionRecovery in main.go
// which provides auto-execute support for high-confidence catalog matches.

// sendFrictionEvent sends a friction event to the daemon for aggregation and upload.
// The send is synchronous with a 5ms IPC timeout. On a local Unix socket, 5ms is
// ample for connect+write. If the daemon is unreachable, we fail fast and lose the
// event (acceptable per fire-and-forget principle).
//
// This avoids the race condition where a background goroutine gets killed by os.Exit()
// before it can complete the IPC send.
func sendFrictionEvent(event *uxfriction.FrictionEvent) {
	if event == nil {
		return
	}

	// respect telemetry opt-out at the CLI layer (defense in depth —
	// daemon also checks, but we skip the IPC entirely if disabled)
	if os.Getenv("DO_NOT_TRACK") == "1" {
		return
	}
	if strings.ToLower(os.Getenv("SAGEOX_FRICTION")) == "false" {
		return
	}
	if userCfg, err := config.LoadUserConfig(""); err == nil && !userCfg.IsTelemetryEnabled() {
		return
	}

	client := daemon.NewClientWithTimeout(5 * time.Millisecond)

	// convert uxfriction event to daemon payload
	payload := daemon.FrictionPayload{
		Timestamp:  event.Timestamp,
		Kind:       string(event.Kind),
		Command:    event.Command,
		Subcommand: event.Subcommand,
		Actor:      event.Actor,
		AgentType:  event.AgentType,
		PathBucket: event.PathBucket,
		Input:      event.Input,
		ErrorMsg:   event.ErrorMsg,
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return
	}

	// fire-and-forget: ignore all errors
	_ = client.SendOneWay(daemon.Message{
		Type:    daemon.MsgTypeFriction,
		Payload: data,
	})
}
