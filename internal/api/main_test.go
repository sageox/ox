package api

import (
	"os"
	"testing"

	"github.com/sageox/ox/internal/testutil/httpisolate"
)

func TestMain(m *testing.M) {
	httpisolate.DefaultTransport()
	os.Exit(m.Run())
}
