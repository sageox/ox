package main

import (
	"fmt"
	"os"

	"example.com/acme/internal/config"
	"example.com/acme/internal/upload"
)

func main() {
	cfg := config.Load()
	client := upload.NewClient(cfg.ArtifactStoreURL)
	if err := client.Upload(os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, "upload failed:", err)
		os.Exit(1)
	}
}
