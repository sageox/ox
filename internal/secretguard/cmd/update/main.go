// Command update refreshes the complete bundled offline credential catalog.
// Gitleaks intentionally follows latest automatically; do not replace this
// with the historical hand-ported detector generator, which drops rule IDs.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/sageox/ox/internal/secretguard"
)

func main() {
	data, err := secretguard.LatestCatalog(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := os.WriteFile("internal/secretguard/gitleaks.toml", data, 0644); err != nil {
		fmt.Fprintln(os.Stderr, "cannot update bundled credential catalog")
		os.Exit(1)
	}
}
