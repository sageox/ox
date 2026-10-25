package attestpublication

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBuildPackageRejectsRunDotSegments(t *testing.T) {
	for _, name := range []string{
		"run-feature-parent-segment", "run-feature-dot-segment",
		"run-evidence-parent-segment", "run-evidence-dot-segment",
	} {
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("contract/testdata/conformance/invalid", name+".json"))
			if err != nil {
				t.Fatal(err)
			}
			export := t.TempDir()
			if err := os.WriteFile(filepath.Join(export, runFilename), raw, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := BuildPackage(export, filepath.Join(t.TempDir(), "package.zip")); err == nil {
				t.Fatal("accepted a run path with a dot segment")
			}
		})
	}
}
