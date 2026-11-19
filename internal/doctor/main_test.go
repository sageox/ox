package doctor

import (
	"os"
	"testing"

	"github.com/sageox/ox/internal/testguard"
)

func TestMain(m *testing.M) {
	os.Exit(testguard.RunWithDataSandbox(m))
}
